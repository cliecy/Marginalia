// Package ai implements the AI features of Marginalia on top of any
// OpenAI-compatible chat API (Magpie's local gateway, CLIProxyAPI, OpenAI…):
// streaming chat with vault context, and the "organize the vault" agent that
// proposes a reviewable changeset which ApplyChangeset later writes.
//
// It is a port of the Python backend's ai.py. Prompts, context assembly,
// proposal rules, events and error texts are identical; the deliberate
// differences are listed in the package's contract (api-ai.md) and next to the
// code concerned.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"marginalia/internal/chats"
	"marginalia/internal/config"
	"marginalia/internal/vault/pystr"
)

// Message is one OpenAI chat message ({"role", "content"}). It is the same
// type as chats.Message, so request bodies decode leniently: a non-string
// content is converted the way Python's str() did.
type Message = chats.Message

// Chat roles.
const (
	RoleSystem    = "system"
	RoleUser      = chats.RoleUser
	RoleAssistant = chats.RoleAssistant
)

// Error is an AI failure with a user-facing (Chinese) message, the Python
// backend's AIError. The HTTP layer reports Message to the client as is.
type Error struct {
	Message    string // shown to the user
	StatusCode int    // HTTP status returned by the AI service, 0 if none
	Err        error  // underlying cause, if any
}

func (e *Error) Error() string { return e.Message }

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// ErrNotConfigured is wrapped by the error returned when the client has no
// base URL, or no key for an endpoint that is not on the loopback interface.
var ErrNotConfigured = errors.New("ai: not configured")

// errIdle is the cancellation cause used when the AI service stops sending.
var errIdle = errors.New("ai: read timeout")

// timeouts mirror httpx.Timeout: connect bounds the TCP connect and TLS
// handshake, write sending the request, read every wait for data from the
// service (the response headers, then each chunk of the body).
type timeouts struct {
	connect, write, read time.Duration
}

// The Python client's timeouts: httpx.AsyncClient(timeout=15) for the model
// list, Timeout(connect=10, read=300, write=30, pool=10) for completions.
var (
	modelsTimeouts = timeouts{connect: 15 * time.Second, write: 15 * time.Second, read: 15 * time.Second}
	streamTimeouts = timeouts{connect: 10 * time.Second, write: 30 * time.Second, read: 300 * time.Second}
)

// Options configures a Client.
type Options struct {
	// BaseURL is the API root including the version segment, for example
	// "http://127.0.0.1:3425/v1". A trailing slash is ignored.
	BaseURL string
	// APIKey returns the key sent as a Bearer token. It is called for every
	// request, so the key can be changed without rebuilding the client. A nil
	// func or an empty key sends no Authorization header, which loopback
	// gateways such as Magpie accept.
	APIKey func() string
	// KeyFile names the file the key is read from. It only appears in the
	// error reported when a remote endpoint has no key.
	KeyFile string
	// DefaultModel is used by requests that name no model.
	DefaultModel string
	// UserAgent is sent with every request ("Marginalia" when empty).
	UserAgent string
	// HTTPClient performs the requests. nil selects a shared client whose
	// transport applies the connect timeouts above and the proxy settings of
	// the environment (like httpx; loopback addresses are never proxied).
	HTTPClient *http.Client
}

// Client talks to an OpenAI-compatible API. It is immutable and safe for
// concurrent use; build a new one when the settings change.
type Client struct {
	baseURL      string
	apiKey       func() string
	keyFile      string
	defaultModel string
	userAgent    string
	http         *http.Client
	models       timeouts // overridable in tests
	stream       timeouts
}

// NewClient returns a client for opts.
func NewClient(opts Options) *Client {
	c := &Client{
		baseURL:      strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/"),
		apiKey:       opts.APIKey,
		keyFile:      opts.KeyFile,
		defaultModel: opts.DefaultModel,
		userAgent:    opts.UserAgent,
		http:         opts.HTTPClient,
		models:       modelsTimeouts,
		stream:       streamTimeouts,
	}
	if c.apiKey == nil {
		c.apiKey = func() string { return "" }
	}
	if c.userAgent == "" {
		c.userAgent = "Marginalia"
	}
	if c.http == nil {
		c.http = &http.Client{Transport: sharedTransport()}
	}
	return c
}

// NewFromSettings returns a client for the AI section of s. The key is read
// from s.AIAPIKeyFile() on every request.
func NewFromSettings(s config.Settings) *Client {
	return NewClient(Options{
		BaseURL:      s.AI.BaseURL,
		APIKey:       s.AIAPIKey,
		KeyFile:      s.AIAPIKeyFile(),
		DefaultModel: s.AI.DefaultModel,
	})
}

// BaseURL returns the API root without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// DefaultModel returns the model used when a request names none.
func (c *Client) DefaultModel() string { return c.defaultModel }

// Configured reports whether requests can be attempted: a base URL is set and
// there is a key or the endpoint is on the loopback interface (the same rule
// as config.Settings.AIConfigured).
func (c *Client) Configured() bool { return c.check() == nil }

// check returns the "not configured" error for the current key, if any.
func (c *Client) check() error {
	if c.baseURL == "" {
		return &Error{Message: "AI 未配置：请先设置 AI 服务地址", Err: ErrNotConfigured}
	}
	if c.apiKey() == "" && !config.IsLoopbackURL(c.baseURL) {
		msg := "AI 未配置：请先设置 API Key"
		if c.keyFile != "" {
			msg = "AI 未配置：请把 API Key 写入 " + c.keyFile
		}
		return &Error{Message: msg, Err: ErrNotConfigured}
	}
	return nil
}

// ------------------------------------------------------------------ models

// Model is an entry of the service's model list.
type Model struct {
	ID string `json:"id"`
	// Label is a human-readable name: display_name, else magpie_label; ""
	// when the service sends neither (show the ID then).
	Label string `json:"label,omitempty"`
	// Detail is Magpie's magpie_label ("GPT-6.1-Sol · Codex"), which also
	// names the provider; "" when absent.
	Detail        string `json:"detail,omitempty"`
	OwnedBy       string `json:"owned_by,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
}

// excludedModelWords mark models that cannot chat (image generation,
// embeddings, speech, rerankers, video); ids containing one are left out.
var excludedModelWords = []string{"image", "embedding", "tts", "whisper", "rerank", "video"}

// Models lists the chat models the service offers, in the service's order.
// Ids containing excludedModelWords (case-insensitive) and models whose
// declared output modalities do not include text are left out.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	resp, err := c.send(ctx, http.MethodGet, "/models", nil, c.models)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Message: fmt.Sprintf("获取模型列表失败（HTTP %d）", resp.StatusCode), StatusCode: resp.StatusCode}
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		if stop := interrupted(resp); stop != nil {
			return nil, stop
		}
		return nil, &Error{Message: "AI 服务返回的模型列表无法解析：" + err.Error(), Err: err}
	}
	models := []Model{}
	seen := map[string]bool{}
	for _, raw := range payload.Data {
		var entry struct {
			ID            any `json:"id"`
			DisplayName   any `json:"display_name"`
			MagpieLabel   any `json:"magpie_label"`
			OwnedBy       any `json:"owned_by"`
			ContextLength any `json:"context_length"`
			Modalities    any `json:"modalities"`
		}
		if json.Unmarshal(raw, &entry) != nil {
			continue // not an object
		}
		id := stringOf(entry.ID)
		if id == "" || seen[id] || excludedModel(id) || !outputsText(entry.Modalities) {
			continue
		}
		seen[id] = true
		model := Model{ID: id, Detail: stringOf(entry.MagpieLabel), OwnedBy: stringOf(entry.OwnedBy)}
		model.Label = stringOf(entry.DisplayName)
		if model.Label == "" {
			model.Label = model.Detail
		}
		if n, ok := entry.ContextLength.(float64); ok && n > 0 {
			model.ContextLength = int(n)
		}
		models = append(models, model)
	}
	return models, nil
}

// ModelIDs returns the ids of models, in order.
func ModelIDs(models []Model) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// ModelLabels maps each model id that has a Label to it.
func ModelLabels(models []Model) map[string]string {
	labels := map[string]string{}
	for _, m := range models {
		if m.Label != "" {
			labels[m.ID] = m.Label
		}
	}
	return labels
}

func excludedModel(id string) bool {
	lower := strings.ToLower(id)
	for _, word := range excludedModelWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// outputsText reports whether a model's "modalities" entry allows text
// output. Services that do not declare output modalities are assumed to.
func outputsText(modalities any) bool {
	m, ok := modalities.(map[string]any)
	if !ok {
		return true
	}
	outputs, ok := m["output"].([]any)
	if !ok || len(outputs) == 0 {
		return true
	}
	for _, out := range outputs {
		if out == "text" {
			return true
		}
	}
	return false
}

// stringOf returns v trimmed when it is a string, else "".
func stringOf(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// ------------------------------------------------------------- completions

// chatRequest is the body of POST /chat/completions.
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Stream sends a streaming chat completion and calls onDelta with every
// piece of generated text, in order. model "" selects the default model.
// It returns when the service sends [DONE] or closes the stream, when
// onDelta fails (its error is returned) or when ctx ends (ctx.Err()).
func (c *Client) Stream(ctx context.Context, model string, messages []Message, onDelta func(delta string) error) error {
	if model == "" {
		model = c.defaultModel
	}
	if messages == nil {
		messages = []Message{}
	}
	resp, err := c.send(ctx, http.MethodPost, "/chat/completions", chatRequest{Model: model, Messages: messages, Stream: true}, c.stream)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) // 300 code points are at most 1200 bytes
		return &Error{
			Message:    fmt.Sprintf("AI 请求失败（HTTP %d）：%s", resp.StatusCode, pystr.Head(pystr.Decode(head), 300)),
			StatusCode: resp.StatusCode,
		}
	}
	return readStream(resp.Body, onDelta, func(err error) error {
		if stop := interrupted(resp); stop != nil {
			return stop
		}
		return &Error{Message: "读取 AI 响应失败：" + err.Error(), Err: err}
	})
}

// Complete is Stream collecting the whole answer.
func (c *Client) Complete(ctx context.Context, model string, messages []Message) (string, error) {
	var out strings.Builder
	err := c.Stream(ctx, model, messages, func(delta string) error {
		out.WriteString(delta)
		return nil
	})
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// ---------------------------------------------------------------- transport

// send performs a request against the API with the given timeouts. JSON
// bodies are encoded from body (nil sends none). The response body enforces
// t.read between chunks and releases the request's resources when closed.
func (c *Client) send(ctx context.Context, method, endpoint string, body any, t timeouts) (*http.Response, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}

	reqCtx, cancel := context.WithCancelCause(ctx)
	reqCtx = context.WithValue(reqCtx, dialTimeoutKey{}, t.connect)
	// Until the response headers arrive the request may spend its connect,
	// write and read allowances; afterwards each body read gets t.read.
	timer := time.AfterFunc(t.connect+t.write+t.read, func() { cancel(errIdle) })
	release := func() {
		timer.Stop()
		cancel(nil)
	}

	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+endpoint, reader)
	if err != nil {
		release()
		return nil, &Error{Message: "AI 服务地址无效：" + c.baseURL, Err: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.userAgent)
	if key := c.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		cause := context.Cause(reqCtx)
		release()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(cause, errIdle) {
			return nil, timeoutError(t)
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // drop the "Post <url>:" prefix; the base URL is in the message
		}
		return nil, &Error{Message: fmt.Sprintf("无法连接 AI 服务（%s）：%v", c.baseURL, err), Err: err}
	}
	timer.Reset(t.read)
	resp.Body = &watchedBody{ReadCloser: resp.Body, timer: timer, t: t, ctx: ctx, reqCtx: reqCtx, release: release}
	return resp, nil
}

func timeoutError(t timeouts) error {
	return &Error{
		Message: fmt.Sprintf("AI 服务响应超时（%d 秒内没有收到数据）", int(t.read/time.Second)),
		Err:     context.DeadlineExceeded,
	}
}

// watchedBody is a response body that cancels the request when no data
// arrives for t.read (httpx's read timeout) and frees it on Close.
type watchedBody struct {
	io.ReadCloser
	timer   *time.Timer
	t       timeouts
	ctx     context.Context // the caller's context
	reqCtx  context.Context // the request's context (cancelled on idle)
	release func()
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.t.read)
	}
	return n, err
}

func (b *watchedBody) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}

// interrupted explains why reading resp's body failed when the request was
// stopped rather than broken: the caller's context error, or the read
// timeout. It returns nil otherwise.
func interrupted(resp *http.Response) error {
	b, ok := resp.Body.(*watchedBody)
	switch {
	case !ok:
		return nil
	case b.ctx.Err() != nil:
		return b.ctx.Err()
	case errors.Is(context.Cause(b.reqCtx), errIdle):
		return timeoutError(b.t)
	}
	return nil
}

// dialTimeoutKey carries the connect timeout of a request to the dialer.
type dialTimeoutKey struct{}

// sharedTransport is the transport of clients built without an HTTPClient.
// It is shared so that rebuilding a client on a settings change keeps the
// connection pool.
var sharedTransport = sync.OnceValue(func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		timeout, _ := ctx.Value(dialTimeoutKey{}).(time.Duration)
		if timeout <= 0 {
			timeout = streamTimeouts.connect
		}
		dialer := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		return dialer.DialContext(ctx, network, addr)
	}
	transport.TLSHandshakeTimeout = 10 * time.Second
	return transport
})
