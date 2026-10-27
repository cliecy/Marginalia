package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"marginalia/internal/config"
	"marginalia/internal/magpie"
)

// testUI is the web UI the fixtures serve (independent of whether the real
// frontend was built into internal/webui).
var testUI = fstest.MapFS{
	"index.html":       {Data: []byte("<!doctype html><title>Marginalia</title>")},
	"sandbox.html":     {Data: []byte("<!doctype html><title>sandbox</title>")},
	"assets/app-1.js":  {Data: []byte("console.log(1)")},
	"assets/app-1.css": {Data: []byte("body{}")},
	"favicon.svg":      {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
	".keep":            {Data: nil},
}

// testExecutable is the command registered for the stdio MCP server.
const testExecutable = "/Applications/Marginalia.app/Contents/MacOS/Marginalia"

// fixture is a Core and Server over the standard test vault,
// with secrets in a temporary data folder.
type fixture struct {
	t        *testing.T
	notes    string // vault folder ("" = none)
	data     string
	core     *Core
	srv      *Server
	password string
	token    string
}

// fixtureOptions adjust a fixture before the core is built.
type fixtureOptions struct {
	mode     config.Mode
	noVault  bool
	settings func(*config.Settings)
	magpie   *magpie.Client
	static   fstest.MapFS
}

// newFixture builds the default fixture: server mode like the Python
// tests, autocommit off, no AI key, the API listener disabled, a Magpie
// client that finds neither a gateway nor the CLI.
func newFixture(t *testing.T, opts ...func(*fixtureOptions)) *fixture {
	t.Helper()
	o := fixtureOptions{mode: config.ModeServer, static: testUI}
	for _, fn := range opts {
		fn(&o)
	}
	root := t.TempDir()
	f := &fixture{t: t, data: filepath.Join(root, "data")}
	if !o.noVault {
		f.notes = writeVault(t, filepath.Join(root, "notes"))
	}
	s := config.Defaults(o.mode)
	s.DataDir = f.data
	s.NotesDir = f.notes
	s.StaticDir = ""
	s.Git.Autocommit = false
	s.AI.APIKeyFile = filepath.Join(f.data, "key")
	s.API.Enabled = false
	s.API.Port = freePort(t)
	if o.settings != nil {
		o.settings(&s)
	}
	core, err := NewCore(s, CoreOptions{Mode: o.mode, Version: "9.9.9-test", Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if o.magpie == nil {
		o.magpie = &magpie.Client{GatewayURL: closedURL(t), CLIPath: filepath.Join(root, "no-magpie")}
	}
	f.core = core
	f.srv = New(core, Options{Static: o.static, Magpie: o.magpie, Executable: testExecutable, Logger: quietLogger()})
	t.Cleanup(func() {
		_ = f.srv.StopAPI(t.Context())
		core.Close()
	})
	f.password = readSecret(t, filepath.Join(f.data, "password"))
	f.token = readSecret(t, filepath.Join(f.data, "mcp_token"))
	return f
}

// writeVault creates the vault of the Python tests in dir.
func writeVault(t *testing.T, dir string) string {
	t.Helper()
	files := map[string]string{
		"A.md":          "---\ntags: [ml]\nsummary: about A\n---\n# A\nsee [[B]] and ![[_assets/x.png]]\n",
		"sub/B.md":      "# B\nback to [[A|alias]]\n",
		"_assets/x.png": "\x89PNG",
		".secret.md":    "hidden",
	}
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func readSecret(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// freePort returns a TCP port that was free a moment ago.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// closedURL is an http URL nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("http://127.0.0.1:%d", freePort(t))
}

// needGit skips a test when git is not installed.
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// ------------------------------------------------------------------ client

// client sends requests to a handler and keeps the cookies it sets, like a
// browser (or FastAPI's TestClient).
type client struct {
	t       *testing.T
	h       http.Handler
	remote  string // RemoteAddr of the requests
	header  http.Header
	cookies map[string]*http.Cookie
}

// app returns a client of the desktop app's own pages (AppHandler, origin
// mygo://localhost).
func (f *fixture) app() *client {
	c := f.client(f.srv.AppHandler(), "mygo:1")
	c.header.Set("Origin", "mygo://localhost")
	return c
}

// web returns a browser-like client of NetHandler (no credentials).
func (f *fixture) web() *client { return f.client(f.srv.NetHandler(), "192.0.2.1:1234") }

// bearer returns a NetHandler client using the API token.
func (f *fixture) bearer() *client {
	c := f.web()
	c.header.Set("Authorization", "Bearer "+f.token)
	return c
}

// loggedIn returns a NetHandler client that logged in with the password.
func (f *fixture) loggedIn() *client {
	c := f.web()
	if res := c.do("POST", "/api/auth/login", map[string]string{"password": f.password}); res.Code != 200 {
		f.t.Fatalf("login: %d %s", res.Code, res.Body)
	}
	return c
}

func (f *fixture) client(h http.Handler, remote string) *client {
	return &client{t: f.t, h: h, remote: remote, header: http.Header{}, cookies: map[string]*http.Cookie{}}
}

// do sends a request. body is nil, a string or []byte (sent as is) or a
// value sent as JSON; extra are header name/value pairs.
func (c *client) do(method, target string, body any, extra ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	var reader io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	case []byte:
		reader = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			c.t.Fatal(err)
		}
		reader, contentType = bytes.NewReader(data), "application/json"
	}
	req := httptest.NewRequest(method, target, reader)
	req.RemoteAddr = c.remote
	for k, v := range c.header {
		req.Header[k] = v
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "" {
			req.Header.Del(extra[i])
		} else {
			req.Header.Set(extra[i], extra[i+1])
		}
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	return rec
}

// decode unmarshals a JSON response body.
func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

// jsonMap is a decoded JSON object.
type jsonMap = map[string]any

// expect fails unless rec has status.
func expect(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
}

// --------------------------------------------------------------- fakes

// fakeMagpie is a Magpie installation: a gateway (httptest) and a CLI
// (a shell script) whose library remembers registrations in a file.
type fakeMagpie struct {
	client  *magpie.Client
	dir     string
	gateway *httptest.Server
}

func newFakeMagpie(t *testing.T) *fakeMagpie {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake magpie CLI is a shell script")
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "registered")
	script := `#!/bin/sh
echo "$@" >> '` + filepath.Join(dir, "args.txt") + `'
case "$1 $2 $3" in
"library mcp add") shift 3; echo "$@" > '` + state + `'; echo "added $1 to 3 agents";;
"library mcp rm") rm -f '` + state + `'; echo "removed $4";;
*)
  if [ "$1" = library ] && [ -z "$2" ]; then
    if [ -f '` + state + `' ]; then echo "Claude Code"; echo "  mcp: $(cut -d' ' -f1 '` + state + `')"; else echo "Claude Code"; echo "  mcp: (none)"; fi
  else
    echo "unknown command" >&2; exit 2
  fi;;
esac
`
	cli := filepath.Join(dir, "magpie")
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/":
			_, _ = io.WriteString(w, `{"name":"magpie","version":"0.1.1564","apis":["/v1/chat/completions"],"models":2}`)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"codex/gpt-6.1-sol","magpie_label":"GPT-6.1-Sol · Codex"},{"id":"group/default","display_name":"Default"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gw.Close)
	return &fakeMagpie{client: &magpie.Client{GatewayURL: gw.URL, CLIPath: cli}, dir: dir, gateway: gw}
}

// args returns the CLI invocations so far, one per line.
func (m *fakeMagpie) args(t *testing.T) string {
	data, err := os.ReadFile(filepath.Join(m.dir, "args.txt"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// sseLines splits an event stream into its data payloads.
func sseLines(body string) []string {
	var out []string
	for _, block := range strings.Split(body, "\n\n") {
		if data, ok := strings.CutPrefix(strings.TrimSpace(block), "data: "); ok {
			out = append(out, data)
		}
	}
	return out
}

// Shorthands for the tests.
type (
	settingsT        = config.Settings
	httptestRecorder = httptest.ResponseRecorder
)

const modeDesktop = config.ModeDesktop

// writeFile writes a vault file directly (not through the API).
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// multipartBody is an /api/upload body with a file and an optional folder.
func multipartBody(t *testing.T, name, content, folder string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if folder != "" {
		if err := w.WriteField("folder", folder); err != nil {
			t.Fatal(err)
		}
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, content)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// urlQuery escapes a query parameter value.
func urlQuery(s string) string { return url.QueryEscape(s) }
