package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProbeTimeout bounds Probe, so that `marginalia mcp` starts quickly when
// the app is not running.
const ProbeTimeout = 500 * time.Millisecond

// bridgeRequestTimeout bounds one relayed message.
const bridgeRequestTimeout = 5 * time.Minute

// AppBaseURL returns the base URL ("http://host:port") under which the app's
// API listener bound to host:port is reached from this machine. Wildcard
// listen addresses are dialled on loopback. Append "/healthz" for Probe and
// "/mcp" for Bridge.
func AppBaseURL(host string, port int) string {
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// Probe reports whether a Marginalia app answers healthURL (its
// GET /healthz, which replies {"ok": true}) within ProbeTimeout. Any
// Marginalia answers it, so it says nothing about who answered: use
// ProbeApp before sending a token.
func Probe(ctx context.Context, healthURL string) bool {
	ok, _ := health(ctx, healthURL)
	return ok
}

// health fetches healthURL within ProbeTimeout and returns its "ok" and
// "proof" fields.
func health(ctx context.Context, healthURL string) (ok bool, proof string) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false, ""
	}
	transport := newTransport()
	transport.DisableKeepAlives = true
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, ""
	}
	var body struct {
		OK    bool   `json:"ok"`
		Proof string `json:"proof"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil {
		return false, ""
	}
	return body.OK, body.Proof
}

// HealthNonceMax is the longest nonce GET /healthz answers with a proof.
const HealthNonceMax = 128

// HealthProof is what GET /healthz?nonce=<nonce> adds as "proof" when it is
// served on TCP port port by an app whose API token is token:
// hex(HMAC-SHA256(token, "marginalia-healthz:<port>:<nonce>")). It lets a
// client check that the server holds the token without sending it, and the
// port keeps a server on another port from relaying the challenge to the
// real app.
func HealthProof(token, nonce string, port int) string {
	mac := hmac.New(sha256.New, []byte(token))
	fmt.Fprintf(mac, "marginalia-healthz:%d:%s", port, nonce)
	return hex.EncodeToString(mac.Sum(nil))
}

// AppState is what ProbeApp found at an address.
type AppState int

const (
	// AppDown: nothing answers, or not Marginalia.
	AppDown AppState = iota
	// AppForeign: a Marginalia answers but cannot prove it holds the
	// token (another installation or an impostor).
	AppForeign
	// AppUp: the app holding token answers; it is safe to send the token.
	AppUp
)

// ProbeApp asks the API at baseURL ("http://host:port") to prove, through
// GET /healthz with a fresh nonce, that it holds token, without the token
// ever being sent. It waits at most ProbeTimeout.
func ProbeApp(ctx context.Context, baseURL, token string) AppState {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return AppDown
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return AppDown
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return AppDown
	}
	challenge := hex.EncodeToString(nonce)
	ok, proof := health(ctx, strings.TrimRight(baseURL, "/")+"/healthz?nonce="+challenge)
	switch {
	case !ok:
		return AppDown
	case token != "" && hmac.Equal([]byte(proof), []byte(HealthProof(token, challenge, portNum))):
		return AppUp
	default:
		return AppForeign
	}
}

// Bridge relays a stdio MCP session (newline-delimited JSON-RPC on in/out,
// as RunStdio) to a running app's Streamable HTTP endpoint mcpURL,
// authenticating with "Authorization: Bearer <token>". Each line is POSTed
// unchanged, so request ids are preserved; replies are written back one per
// line, 202 Accepted (notifications) writes nothing, and an SSE response
// writes one line per event. When the app cannot be reached or refuses a
// message (HTTP error without a JSON-RPC body), every request in that
// message is answered with a JSON-RPC error (code -32000) carrying the
// reason, and notifications are dropped. An Mcp-Session-Id issued by the
// endpoint is sent back on later requests.
//
// Return values are those of RunStdio.
func Bridge(ctx context.Context, in io.Reader, out io.Writer, mcpURL, token string) error {
	f := NewForwarder(mcpURL, token)
	defer f.Close()
	return serveLines(ctx, in, out, f.p.forward)
}

// Failure tells whether Forwarder.Forward delivered a message.
type Failure int

const (
	// Delivered: the app answered (its reply may be a JSON-RPC error).
	Delivered Failure = iota
	// Unreachable: nothing accepted the connection, so the message was
	// certainly not handled.
	Unreachable
	// Unauthorized: the server refused the token (HTTP 401) before handling
	// the message.
	Unauthorized
	// Failed: any other failure; the message may have been handled.
	Failed
)

// Forwarder relays single messages to an app's /mcp endpoint, as Bridge
// does for a whole session.
type Forwarder struct {
	p         *proxy
	transport *http.Transport
}

// NewForwarder returns a Forwarder for the endpoint mcpURL that
// authenticates with token. Close it when done.
func NewForwarder(mcpURL, token string) *Forwarder {
	transport := newTransport()
	return &Forwarder{
		p:         &proxy{url: mcpURL, token: token, client: &http.Client{Transport: transport}},
		transport: transport,
	}
}

// Forward relays msg and returns what to write back, like a Handler, and
// whether it was delivered. When it was not, reply holds the JSON-RPC
// errors Bridge would answer with.
func (f *Forwarder) Forward(ctx context.Context, msg []byte) (reply []byte, ok bool, failure Failure) {
	return f.p.send(ctx, msg)
}

// Close closes the idle connections.
func (f *Forwarder) Close() { f.transport.CloseIdleConnections() }

// RunStdioFunc is RunStdio with a handler that also gets the session's
// context.
func RunStdioFunc(ctx context.Context, in io.Reader, out io.Writer, handle func(ctx context.Context, msg []byte) (reply []byte, ok bool)) error {
	return serveLines(ctx, in, out, handle)
}

// ErrorReplies answers every request in msg (a message or a batch) with a
// JSON-RPC error (code -32000) carrying reason; notifications get nothing
// (ok is false when nothing is to be written).
func ErrorReplies(msg []byte, reason string) (reply []byte, ok bool) {
	return failureReplies(msg, reason)
}

// newTransport is a dedicated transport that never goes through an HTTP
// proxy: the app listens on this machine. It opens a new connection per
// message: a kept-alive one outlives an app that quit, and the POST sent on it
// fails with EOF, which cannot be told apart from a message the app may have
// handled. A fresh connection to a stopped app is refused instead
// (Unreachable), so the caller can safely look for the app again.
func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DisableKeepAlives = true
	return t
}

// proxy forwards messages to an app's /mcp endpoint.
type proxy struct {
	url    string
	token  string
	client *http.Client

	mu      sync.Mutex
	session string // Mcp-Session-Id issued by the endpoint, if any
}

// forward relays one message and returns what to write back.
func (p *proxy) forward(ctx context.Context, msg []byte) ([]byte, bool) {
	reply, ok, _ := p.send(ctx, msg)
	return reply, ok
}

// fail returns the failure replies for msg with the failure kind.
func fail(msg []byte, failure Failure, reason string) ([]byte, bool, Failure) {
	reply, ok := failureReplies(msg, reason)
	return reply, ok, failure
}

// send is forward that also reports whether the message was delivered.
func (p *proxy) send(ctx context.Context, msg []byte) ([]byte, bool, Failure) {
	ctx, cancel := context.WithTimeout(ctx, bridgeRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(msg))
	if err != nil {
		return fail(msg, Unreachable, fmt.Sprintf("invalid Marginalia MCP URL %q: %v", p.url, err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	p.mu.Lock()
	if p.session != "" {
		req.Header.Set("Mcp-Session-Id", p.session)
	}
	p.mu.Unlock()

	resp, err := p.client.Do(req)
	if err != nil {
		failure := Failed
		if opErr := (*net.OpError)(nil); errors.As(err, &opErr) && opErr.Op == "dial" {
			failure = Unreachable
		}
		return fail(msg, failure, fmt.Sprintf("Marginalia is not reachable at %s: %v", p.url, err))
	}
	defer resp.Body.Close()
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		p.mu.Lock()
		p.session = session
		p.mu.Unlock()
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxMessageBytes+1))
	switch {
	case err != nil:
		return fail(msg, Failed, fmt.Sprintf("reading the reply from Marginalia failed: %v", err))
	case len(body) > MaxMessageBytes:
		return fail(msg, Failed, "the reply from Marginalia is too large")
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if success && isEventStream(resp.Header.Get("Content-Type")) {
		reply, ok := sseMessages(body)
		return reply, ok, Delivered
	}
	if reply, ok := compactJSON(body); ok && isJSONRPC(reply) {
		return reply, true, Delivered // includes error replies such as the 400 parse error
	}
	if success && len(trimJSONSpace(body)) == 0 {
		return nil, false, Delivered // 202 Accepted (or 204): nothing to say
	}
	if success {
		return fail(msg, Failed, fmt.Sprintf("unexpected reply from %s (HTTP %d, %s)", p.url, resp.StatusCode, resp.Header.Get("Content-Type")))
	}
	failure := Failed
	if resp.StatusCode == http.StatusUnauthorized {
		failure = Unauthorized
	}
	return fail(msg, failure, upstreamError(resp.StatusCode, body))
}

// compactJSON returns body as single-line JSON, if it is JSON.
func compactJSON(body []byte) ([]byte, bool) {
	body = trimJSONSpace(body)
	if len(body) == 0 {
		return nil, false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, body); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// isJSONRPC reports whether a JSON body is a JSON-RPC reply (an object
// with "jsonrpc", or a batch). Such bodies are relayed whatever the HTTP
// status (e.g. the 400 parse error); other JSON is not.
func isJSONRPC(body []byte) bool {
	if len(body) > 0 && body[0] == '[' {
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	_, ok := fields["jsonrpc"]
	return ok
}

// isEventStream reports whether contentType is text/event-stream.
func isEventStream(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "text/event-stream"
}

// sseMessages extracts the data of every event of an SSE body, one compact
// JSON message per line.
func sseMessages(body []byte) ([]byte, bool) {
	var out [][]byte
	var data []string
	flush := func() {
		if len(data) > 0 {
			if msg, ok := compactJSON([]byte(strings.Join(data, "\n"))); ok {
				out = append(out, msg)
			}
			data = nil
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64<<10), MaxMessageBytes+1)
	for scanner.Scan() {
		text := strings.TrimSuffix(scanner.Text(), "\r")
		switch {
		case text == "":
			flush()
		case strings.HasPrefix(text, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(text, "data:"), " "))
		}
	}
	flush()
	if len(out) == 0 {
		return nil, false
	}
	return bytes.Join(out, []byte("\n")), true
}

// upstreamError describes a failed HTTP exchange, using the body's "detail"
// or "error" text when the app sent one.
func upstreamError(status int, body []byte) string {
	var payload struct {
		Detail any `json:"detail"`
		Error  any `json:"error"`
	}
	reason := ""
	if json.Unmarshal(body, &payload) == nil {
		for _, v := range []any{payload.Detail, payload.Error} {
			if s, ok := v.(string); ok && s != "" {
				reason = s
				break
			}
		}
	}
	text := fmt.Sprintf("Marginalia answered HTTP %d %s", status, http.StatusText(status))
	if status == http.StatusUnauthorized {
		text += " (the MCP token does not match the running app)"
	}
	if reason != "" {
		text += ": " + reason
	}
	return text
}

// failureReplies answers every request in msg (a message or a batch) with
// an upstream error; notifications and invalid elements get nothing.
func failureReplies(msg []byte, reason string) ([]byte, bool) {
	data := trimJSONSpace(msg)
	if !json.Valid(data) {
		return parseErrorReply(), true
	}
	if data[0] != '[' {
		if id := requestID(data); id != nil {
			return errorReply(id, codeUpstreamError, reason), true
		}
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, false
	}
	var replies []json.RawMessage
	for _, item := range items {
		if id := requestID(item); id != nil {
			replies = append(replies, errorReply(id, codeUpstreamError, reason))
		}
	}
	if len(replies) == 0 {
		return nil, false
	}
	return encode(replies), true
}

// requestID returns the id of a JSON-RPC request object, or nil for
// notifications and anything that is not an object.
func requestID(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return compactID(fields["id"])
}
