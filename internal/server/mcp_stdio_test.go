package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marginalia/internal/config"
	"marginalia/internal/mcp"
)

// desktopAPIFixture is a desktop fixture whose loopback API runs.
func desktopAPIFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) { s.API.Enabled = true }
	})
	if err := f.srv.StartAPI(); err != nil {
		t.Fatal(err)
	}
	return f
}

// testRouter is the stdio MCP router of `marginalia mcp` for settings
// returned by load, checking for the app on every message.
func testRouter(t *testing.T, load func() (config.Settings, error)) (*mcpRouter, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	r := &mcpRouter{opts: StdioOptions{Load: load, Version: "9.9.9-test", Recheck: time.Nanosecond}}
	r.log = slogTo(&logs)
	r.opts.Logger = r.log
	t.Cleanup(r.close)
	return r, &logs
}

// slogTo returns a logger writing text to w.
func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// call sends one tools/call through the router and returns the reply.
func (r *mcpRouter) call(t *testing.T, id int, tool string, args map[string]any) map[string]any {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":%s}`, id, params)
	reply, ok := r.handle(t.Context(), []byte(msg))
	if !ok {
		t.Fatalf("no reply to %s", msg)
	}
	var out map[string]any
	if err := json.Unmarshal(reply, &out); err != nil {
		t.Fatalf("reply %s: %v", reply, err)
	}
	return out
}

func TestHealthzProvesTheToken(t *testing.T) {
	f := desktopAPIFixture(t)
	base := f.srv.APIStatus().URL
	if got := mcp.ProbeApp(t.Context(), base, f.token); got != mcp.AppUp {
		t.Fatalf("ProbeApp(own token) = %v", got)
	}
	if got := mcp.ProbeApp(t.Context(), base, "not-the-token"); got != mcp.AppForeign {
		t.Fatalf("ProbeApp(other token) = %v", got)
	}
	// The plain health check is unchanged, and the app's own scheme never
	// answers with a proof.
	res, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if len(body) != 1 || body["ok"] != true {
		t.Fatalf("/healthz = %v", body)
	}
	if got := decode[jsonMap](t, f.app().do("GET", "/healthz?nonce=abc", nil)); got["proof"] != nil {
		t.Fatalf("app /healthz = %v", got)
	}
	if got := decode[jsonMap](t, f.web().do("GET", "/healthz?nonce="+strings.Repeat("a", mcp.HealthNonceMax+1), nil)); got["proof"] != nil {
		t.Fatalf("long nonce answered: %v", got)
	}
}

func TestEndpointFile(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	s := f.core.Settings()
	configured := mcp.AppBaseURL(s.API.Host, s.API.Port)
	path := filepath.Join(f.data, EndpointFileName)

	// The app runs with its API off: it says so, and `mcp` must not take
	// over the vault.
	if err := f.srv.StartAPI(); err != nil {
		t.Fatal(err)
	}
	if app := FindLocalApp(s); !app.Running || app.URL != "" {
		t.Fatalf("API off: %+v", app)
	}
	if u := LocalAPIURL(s); u != configured {
		t.Fatalf("LocalAPIURL with the API off = %q", u)
	}
	if err := f.srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the endpoint file outlived the app: %v", err)
	}

	// A file left by a crashed app (its process is gone) is ignored.
	dead := deadPID(t)
	if err := os.WriteFile(path, fmt.Appendf(nil, `{"url":"http://127.0.0.1:1","pid":%d}`, dead), 0o600); err != nil {
		t.Fatal(err)
	}
	if app := FindLocalApp(s); app.Running || app.URL != configured {
		t.Fatalf("stale file: %+v", app)
	}
}

// deadPID returns the ID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.StartProcess(exe, []string{exe, "-test.run=^$"}, &os.ProcAttr{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	return p.Pid
}

// The token is read again when the app refuses it, so regenerating it in
// the app does not break a running `marginalia mcp`.
func TestMCPRouterFollowsTokenRotation(t *testing.T) {
	f := desktopAPIFixture(t)
	r, logs := testRouter(t, func() (config.Settings, error) { return f.core.Settings(), nil })
	write := func(id int, path string) {
		t.Helper()
		reply := r.call(t, id, "write_note", map[string]any{"path": path, "content": "x\n"})
		if reply["error"] != nil || reply["result"].(map[string]any)["isError"] == true {
			t.Fatalf("write_note %s = %v\nlogs:\n%s", path, reply, logs)
		}
	}
	write(1, "Inbox/one.md")
	if r.kind != routeApp {
		t.Fatalf("route = %v, want the app\nlogs:\n%s", r.kind, logs)
	}
	if _, err := f.core.RotateToken(); err != nil {
		t.Fatal(err)
	}
	write(2, "Inbox/two.md")
	if r.kind != routeApp || r.core != nil {
		t.Fatalf("after rotation: route = %v, in-process core = %v", r.kind, r.core != nil)
	}
}

// The router hands over between in-process serving and the app as the app
// starts and stops, and refuses to write while the app runs with its API
// off.
func TestMCPRouterFollowsTheApp(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	r, logs := testRouter(t, func() (config.Settings, error) { return f.core.Settings(), nil })
	read := func(id int) {
		t.Helper()
		reply := r.call(t, id, "read_note", map[string]any{"path": "A.md"})
		if reply["error"] != nil || !strings.Contains(fmt.Sprint(reply["result"]), "about A") {
			t.Fatalf("read_note = %v\nlogs:\n%s", reply, logs)
		}
	}
	read(1)
	if r.kind != routeLocal || r.core == nil {
		t.Fatalf("no app: route = %v", r.kind)
	}

	if _, err := f.core.Update(func(s *config.Settings) error { s.API.Enabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.StartAPI(); err != nil {
		t.Fatal(err)
	}
	read(2)
	if r.kind != routeApp || r.core != nil {
		t.Fatalf("app started: route = %v, in-process core kept = %v", r.kind, r.core != nil)
	}

	if err := f.srv.StopAPI(t.Context()); err != nil {
		t.Fatal(err)
	}
	read(3) // the app is gone: served in-process again
	if r.kind != routeLocal {
		t.Fatalf("app stopped: route = %v", r.kind)
	}

	if _, err := f.core.Update(func(s *config.Settings) error { s.API.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.StartAPI(); err != nil { // the app runs, API off
		t.Fatal(err)
	}
	reply := r.call(t, 4, "read_note", map[string]any{"path": "A.md"})
	if r.kind != routeOff || r.core != nil || !strings.Contains(fmt.Sprint(reply["error"]), "本地 API 已关闭") {
		t.Fatalf("API off: route = %v, reply = %v", r.kind, reply)
	}
}

// Another Marginalia on the configured port (one that does not hold this
// data folder's token) never gets the token: the vault is served
// in-process.
func TestMCPRouterIgnoresAForeignInstance(t *testing.T) {
	foreign := desktopAPIFixture(t)
	mine := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(foreign.srv.APIStatus().URL, "http://"))
	var port int
	fmt.Sscan(portText, &port)
	r, logs := testRouter(t, func() (config.Settings, error) {
		s := mine.core.Settings()
		s.API.Host, s.API.Port = host, port
		return s, nil
	})
	reply := r.call(t, 1, "read_note", map[string]any{"path": "A.md"})
	if reply["error"] != nil || r.kind != routeLocal {
		t.Fatalf("route = %v, reply = %v\nlogs:\n%s", r.kind, reply, logs)
	}
	if !strings.Contains(logs.String(), "another Marginalia") {
		t.Fatalf("logs:\n%s", logs)
	}
}
