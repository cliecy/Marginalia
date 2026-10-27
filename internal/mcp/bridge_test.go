package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "s3cret-token"

// appServer imitates the app's API listener: /healthz and a Bearer-guarded
// /mcp serving srv.
func appServer(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	})
	mcpHandler := HTTPHandler(srv)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+testToken)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid or missing bearer token"}`)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// bridgeLines runs Bridge over the given input lines and returns the
// output lines.
func bridgeLines(t *testing.T, url, token string, lines ...string) []string {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	if err := Bridge(context.Background(), in, &out, url, token); err != nil {
		t.Fatalf("Bridge: %v", err)
	}
	text := strings.TrimSuffix(out.String(), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func TestBridgeRelaysToApp(t *testing.T) {
	v, _ := pythonVault(t)
	ts := appServer(t, New(v, nil, Options{Version: "2.0.0"}))
	got := bridgeLines(t, ts.URL+"/mcp", testToken,
		`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1.0,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"write_note","arguments":{"path":"Inbox/Bridge.md","content":"via bridge\n"}}}`,
		`[{"jsonrpc":"2.0","id":8,"method":"ping"},{"jsonrpc":"2.0","method":"n"}]`,
		`[{"jsonrpc":"2.0","method":"n"}]`,
		`{"jsonrpc":"2.0","id":9,`,
	)
	if len(got) != 5 {
		t.Fatalf("got %d lines, want 5:\n%s", len(got), strings.Join(got, "\n"))
	}
	var init struct {
		ID     string           `json:"id"`
		Result initializeResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(got[0]), &init); err != nil || init.ID != "init-1" ||
		init.Result.ProtocolVersion != "2024-11-05" || init.Result.ServerInfo.Version != "2.0.0" {
		t.Errorf("initialize = %s", got[0])
	}
	want := []string{
		`{"jsonrpc":"2.0","id":1.0,"result":{}}`,
		`{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"已保存 Inbox/Bridge.md（etag e00f037988ac56c71184）"}],"isError":false}}`,
		`[{"jsonrpc":"2.0","id":8,"result":{}}]`,
		`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`,
	}
	for i, w := range want {
		if got[i+1] != w {
			t.Errorf("line %d:\n got %s\nwant %s", i+1, got[i+1], w)
		}
	}
	if note, err := v.ReadNote("Inbox/Bridge.md"); err != nil || note.Content != "via bridge\n" {
		t.Errorf("the app did not write the note: %+v %v", note, err)
	}
}

func TestBridgeErrors(t *testing.T) {
	v, _ := pythonVault(t)
	app := appServer(t, New(v, nil, Options{}))
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conflict":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"detail":"未选择笔记库","code":"no_vault"}`)
		case "/html":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<!doctype html><title>Marginalia</title>")
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer broken.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL + "/mcp"
	gone.Close()

	messages := []string{
		`{"jsonrpc":"2.0","id":"a","method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"n"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`,
	}
	cases := []struct {
		name, url, token, reason string
	}{
		{"wrong token", app.URL + "/mcp", "nope", "Marginalia answered HTTP 401 Unauthorized (the MCP token does not match the running app): invalid or missing bearer token"},
		{"no vault", broken.URL + "/conflict", testToken, "Marginalia answered HTTP 409 Conflict: 未选择笔记库"},
		{"server error", broken.URL + "/boom", testToken, "Marginalia answered HTTP 500 Internal Server Error"},
		{"not the MCP endpoint", broken.URL + "/html", testToken, "unexpected reply from " + broken.URL + "/html (HTTP 200, text/html)"},
		{"not running", goneURL, testToken, "Marginalia is not reachable at " + goneURL},
	}
	for _, c := range cases {
		got := bridgeLines(t, c.url, c.token, messages...)
		if len(got) != 2 {
			t.Errorf("%s: got %d lines, want 2 (no reply to the notification):\n%s", c.name, len(got), strings.Join(got, "\n"))
			continue
		}
		var single reply
		if err := json.Unmarshal([]byte(got[0]), &single); err != nil || string(single.ID) != `"a"` || single.Error == nil ||
			single.Error.Code != -32000 || !strings.HasPrefix(single.Error.Message, c.reason) {
			t.Errorf("%s: single = %s, want id \"a\" and error %q", c.name, got[0], c.reason)
		}
		var batch []reply
		if err := json.Unmarshal([]byte(got[1]), &batch); err != nil || len(batch) != 2 ||
			string(batch[0].ID) != "1" || string(batch[1].ID) != "2" || batch[1].Error == nil {
			t.Errorf("%s: batch = %s", c.name, got[1])
		}
	}
}

func TestBridgeEventStreamAndSession(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n > 1 && r.Header.Get("Mcp-Session-Id") != "sess-1" {
			http.Error(w, `{"detail":"missing session"}`, http.StatusBadRequest)
			return
		}
		if r.Header.Get("Accept") != "application/json, text/event-stream" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID json.RawMessage `json:"id"`
		}
		json.Unmarshal(body, &msg)
		w.Header().Set("Mcp-Session-Id", "sess-1")
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprintf(w, ": comment\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":%s,\"result\":{}}\r\n\r\n", msg.ID)
	}))
	defer ts.Close()
	got := bridgeLines(t, ts.URL, "",
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	want := []string{
		`{"jsonrpc":"2.0","method":"notifications/progress"}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress"}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestProbe(t *testing.T) {
	v, _ := pythonVault(t)
	app := appServer(t, New(v, nil, Options{}))
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			io.WriteString(w, `{"status":"ok"}`) // some other service
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer other.Close()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	ctx := context.Background()
	if !Probe(ctx, app.URL+"/healthz") {
		t.Error("Probe(app) = false")
	}
	for _, url := range []string{other.URL + "/healthz", other.URL + "/missing", gone.URL + "/healthz", "::bad url"} {
		if Probe(ctx, url) {
			t.Errorf("Probe(%s) = true", url)
		}
	}
	start := time.Now()
	if Probe(ctx, other.URL+"/slow") {
		t.Error("Probe(slow) = true")
	}
	if elapsed := time.Since(start); elapsed > ProbeTimeout+400*time.Millisecond {
		t.Errorf("Probe took %v, want about %v", elapsed, ProbeTimeout)
	}
}

func TestAppBaseURL(t *testing.T) {
	cases := map[[2]string]string{
		{"127.0.0.1", "8190"}: "http://127.0.0.1:8190",
		{"0.0.0.0", "8190"}:   "http://127.0.0.1:8190",
		{"", "9000"}:          "http://127.0.0.1:9000",
		{"::", "8190"}:        "http://[::1]:8190",
		{"[::1]", "8190"}:     "http://[::1]:8190",
		{"localhost", "1"}:    "http://localhost:1",
	}
	for in, want := range cases {
		var port int
		fmt.Sscan(in[1], &port)
		if got := AppBaseURL(in[0], port); got != want {
			t.Errorf("AppBaseURL(%q, %d) = %q, want %q", in[0], port, got, want)
		}
	}
}

// proofServer answers /healthz?nonce= like the app, with the proof for
// token, as if it listened on port.
func proofServer(t *testing.T, token string, port func(*http.Request) int) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		fmt.Fprintf(w, `{"ok":true,"proof":%q}`, HealthProof(token, nonce, port(r)))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestProbeApp(t *testing.T) {
	ctx := context.Background()
	ownPort := func(r *http.Request) int {
		var port int
		fmt.Sscanf(r.Host[strings.LastIndexByte(r.Host, ':')+1:], "%d", &port)
		return port
	}
	app := proofServer(t, testToken, ownPort)
	if got := ProbeApp(ctx, app.URL, testToken); got != AppUp {
		t.Errorf("our app = %v, want AppUp", got)
	}
	if got := ProbeApp(ctx, app.URL, "another-token"); got != AppForeign {
		t.Errorf("app with another token = %v, want AppForeign", got)
	}
	if got := ProbeApp(ctx, app.URL, ""); got != AppForeign {
		t.Errorf("no token = %v, want AppForeign", got)
	}
	// A server that relays the challenge to the real app (listening on
	// another port) gets a proof for the wrong port.
	relay := proofServer(t, testToken, func(*http.Request) int { return 1 })
	if got := ProbeApp(ctx, relay.URL, testToken); got != AppForeign {
		t.Errorf("relayed proof = %v, want AppForeign", got)
	}
	// Any other Marginalia (an old build) has no proof.
	plain := appServer(t, New(nil, nil, Options{}))
	if got := ProbeApp(ctx, plain.URL, testToken); got != AppForeign {
		t.Errorf("server without proofs = %v, want AppForeign", got)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	for _, url := range []string{gone.URL, "::bad", ""} {
		if got := ProbeApp(ctx, url, testToken); got != AppDown {
			t.Errorf("ProbeApp(%q) = %v, want AppDown", url, got)
		}
	}
}

func TestForwarderReportsFailures(t *testing.T) {
	v, _ := pythonVault(t)
	ts := appServer(t, New(v, nil, Options{}))
	ctx := context.Background()
	ping := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)

	f := NewForwarder(ts.URL+"/mcp", testToken)
	defer f.Close()
	if reply, ok, failure := f.Forward(ctx, ping); failure != Delivered || !ok || string(reply) != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Errorf("delivered: %s, %v, %v", reply, ok, failure)
	}
	if _, ok, failure := f.Forward(ctx, []byte(`{"jsonrpc":"2.0","method":"n"}`)); failure != Delivered || ok {
		t.Errorf("notification: %v, %v", ok, failure)
	}

	wrong := NewForwarder(ts.URL+"/mcp", "wrong")
	defer wrong.Close()
	if reply, ok, failure := wrong.Forward(ctx, ping); failure != Unauthorized || !ok || !strings.Contains(string(reply), "-32000") {
		t.Errorf("unauthorized: %s, %v, %v", reply, ok, failure)
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	down := NewForwarder(gone.URL+"/mcp", testToken)
	defer down.Close()
	if reply, ok, failure := down.Forward(ctx, ping); failure != Unreachable || !ok || !strings.Contains(string(reply), "not reachable") {
		t.Errorf("unreachable: %s, %v, %v", reply, ok, failure)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	failing := NewForwarder(broken.URL, testToken)
	defer failing.Close()
	if _, _, failure := failing.Forward(ctx, ping); failure != Failed {
		t.Errorf("500: %v, want Failed", failure)
	}
	if reply, ok := ErrorReplies(ping, "nope"); !ok || string(reply) != `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"nope"}}` {
		t.Errorf("ErrorReplies = %s, %v", reply, ok)
	}
}
