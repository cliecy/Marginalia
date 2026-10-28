package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"marginalia/internal/config"
)

// --------------------------------------------- server tests

func TestLoginFlow(t *testing.T) {
	f := newFixture(t)
	c := f.web()
	res := c.do("GET", "/api/tree", nil)
	expect(t, res, 401)
	if got := decode[jsonMap](t, res)["detail"]; got != "login required" {
		t.Fatalf("detail = %v", got)
	}
	res = c.do("POST", "/api/auth/login", map[string]string{"password": "nope"})
	expect(t, res, 401)
	if got := decode[jsonMap](t, res)["detail"]; got != "密码错误" {
		t.Fatalf("detail = %v", got)
	}
	res = c.do("POST", "/api/auth/login", map[string]string{"password": f.password})
	expect(t, res, 200)
	if got := decode[jsonMap](t, res)["user"]; got != "cliecy" {
		t.Fatalf("user = %v", got)
	}
	ck := c.cookies[SessionCookie]
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" || ck.MaxAge != 720*3600 || ck.Secure {
		t.Fatalf("session cookie = %+v", ck)
	}
	expect(t, c.do("GET", "/api/tree", nil), 200)

	expect(t, c.do("POST", "/api/auth/logout", nil), 200)
	expect(t, c.do("GET", "/api/tree", nil), 401)
}

func TestLoginValidation(t *testing.T) {
	f := newFixture(t)
	c := f.web()
	expect(t, c.do("POST", "/api/auth/login", nil), 422)
	expect(t, c.do("POST", "/api/auth/login", map[string]string{}), 422)
	expect(t, c.do("POST", "/api/auth/login", map[string]string{"password": strings.Repeat("x", 513)}), 422)
	expect(t, c.do("POST", "/api/auth/login", "{not json"), 422)
}

func TestLoginRateLimit(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	f.srv.now = func() time.Time { return now }
	c := f.web()
	for range loginAttempts {
		expect(t, c.do("POST", "/api/auth/login", map[string]string{"password": "wrong"}), 401)
	}
	res := c.do("POST", "/api/auth/login", map[string]string{"password": f.password})
	expect(t, res, 429)
	if got := decode[jsonMap](t, res)["detail"]; got != "尝试次数过多，请 5 分钟后再试" {
		t.Fatalf("detail = %v", got)
	}
	// Another client is not affected.
	other := f.client(f.srv.NetHandler(), "192.0.2.2:999")
	expect(t, other.do("POST", "/api/auth/login", map[string]string{"password": f.password}), 200)
	// The window slides.
	now = now.Add(loginWindow + time.Second)
	expect(t, c.do("POST", "/api/auth/login", map[string]string{"password": f.password}), 200)
}

func TestSessionCookieTamperingAndExpiry(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	value := c.cookies[SessionCookie].Value

	forged := f.web()
	forged.cookies[SessionCookie] = &http.Cookie{Name: SessionCookie, Value: signSession([]byte("other secret"), session{User: "cliecy", Exp: time.Now().Add(time.Hour).Unix()})}
	expect(t, forged.do("GET", "/api/tree", nil), 401)

	tampered := f.web()
	tampered.cookies[SessionCookie] = &http.Cookie{Name: SessionCookie, Value: "x" + value}
	expect(t, tampered.do("GET", "/api/tree", nil), 401)

	f.srv.now = func() time.Time { return time.Now().Add(721 * time.Hour) }
	expect(t, c.do("GET", "/api/tree", nil), 401)
}

func TestLoginWithoutLoginUser(t *testing.T) {
	// Serve mode may leave LoginUser unset (the greeting then has no name):
	// the password must still log in and the session must authenticate.
	f := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *config.Settings) { s.LoginUser = "" }
	})
	c := f.client(f.srv.NetHandler(), "192.0.2.10:999")
	expect(t, c.do("POST", "/api/auth/login", map[string]string{"password": f.password}), 200)
	expect(t, c.do("GET", "/api/tree", nil), 200)
}

func TestTreeHidesDotfiles(t *testing.T) {
	f := newFixture(t)
	res := f.loggedIn().do("GET", "/api/tree", nil)
	expect(t, res, 200)
	var names []string
	for _, item := range decode[jsonMap](t, res)["tree"].([]any) {
		names = append(names, item.(jsonMap)["name"].(string))
	}
	if slices.Contains(names, ".git") || slices.Contains(names, ".secret.md") {
		t.Fatalf("hidden entries listed: %v", names)
	}
	if len(names) < 2 || names[0] != "_assets" || names[1] != "sub" {
		t.Fatalf("names = %v", names)
	}
}

func TestSaveConflict(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	note := decode[jsonMap](t, c.do("GET", "/api/note?path=A.md", nil))
	saved := c.do("PUT", "/api/note", jsonMap{"path": "A.md", "content": "new", "base_etag": note["etag"]})
	expect(t, saved, 200)
	stale := c.do("PUT", "/api/note", jsonMap{"path": "A.md", "content": "x", "base_etag": note["etag"]})
	expect(t, stale, 409)
	if got := decode[jsonMap](t, stale)["detail"]; got != "the note changed on disk since it was opened" {
		t.Fatalf("detail = %v", got)
	}
}

func TestNoteRoutes(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	expect(t, c.do("GET", "/api/note", nil), 422)
	expect(t, c.do("GET", "/api/note?path=../x.md", nil), 400)
	expect(t, c.do("GET", "/api/note?path=missing.md", nil), 404)

	res := c.do("POST", "/api/note", jsonMap{"path": "Inbox/new.md", "content": "# new\n"})
	expect(t, res, 200)
	if decode[jsonMap](t, res)["path"] != "Inbox/new.md" {
		t.Fatalf("create = %s", res.Body)
	}
	expect(t, c.do("POST", "/api/note", jsonMap{"path": "Inbox/new.md"}), 409)
	expect(t, c.do("POST", "/api/note", jsonMap{"path": "Inbox/sub", "folder": true}), 200)

	res = c.do("POST", "/api/move", jsonMap{"path": "sub/B.md", "to": "topics/Bee.md"})
	expect(t, res, 200)
	moved := decode[jsonMap](t, res)
	if moved["path"] != "topics/Bee.md" || fmt.Sprint(moved["updated_links_in"]) != "[A.md]" {
		t.Fatalf("move = %v", moved)
	}
	res = c.do("GET", "/api/backlinks?path=topics/Bee.md", nil)
	expect(t, res, 200)
	if !strings.Contains(res.Body.String(), `"path":"A.md"`) {
		t.Fatalf("backlinks = %s", res.Body)
	}
	res = c.do("GET", "/api/search?q=alias", nil)
	expect(t, res, 200)
	if !strings.Contains(res.Body.String(), `"path":"topics/Bee.md"`) {
		t.Fatalf("search = %s", res.Body)
	}
	res = c.do("GET", "/api/graph", nil)
	expect(t, res, 200)
	if !strings.Contains(res.Body.String(), `{"source":"A.md","target":"topics/Bee.md"}`) {
		t.Fatalf("graph = %s", res.Body)
	}
	expect(t, c.do("GET", "/api/graph?chats=maybe", nil), 422)
	res = c.do("GET", "/api/index", nil)
	expect(t, res, 200)
	if !strings.Contains(res.Body.String(), `"summary":"about A"`) {
		t.Fatalf("index = %s", res.Body)
	}

	expect(t, c.do("DELETE", "/api/note?path=Inbox/new.md", nil), 200)
	expect(t, c.do("DELETE", "/api/note?path=Inbox/new.md", nil), 404)
}

func TestRawFileHeaders(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	res := c.do("GET", "/api/file?path=_assets/x.png", nil)
	expect(t, res, 200)
	h := res.Header()
	if h.Get("Content-Type") != "image/png" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("headers = %v", h)
	}
	if h.Get("Content-Disposition") != "inline; filename*=UTF-8''x.png" || h.Get("Cache-Control") != "private, max-age=300" {
		t.Fatalf("headers = %v", h)
	}
	if res.Body.String() != "\x89PNG" {
		t.Fatalf("body = %q", res.Body)
	}
	if got := c.do("GET", "/api/file?path=_assets/x.png&download=1", nil).Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("download disposition = %q", got)
	}

	c2 := f.bearer()
	expect(t, c2.do("POST", "/api/note", jsonMap{"path": "web/page.md", "content": "x"}), 200)
	writeFile(t, f.notes, "web/page.html", "<script>alert(1)</script>")
	writeFile(t, f.notes, "web/pic.svg", "<svg xmlns='http://www.w3.org/2000/svg'/>")
	writeFile(t, f.notes, "web/文档 1.pdf", "%PDF-1.4")

	res = c.do("GET", "/api/file?path=web/page.html", nil)
	if res.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("html headers = %v", res.Header())
	}
	res = c.do("GET", "/api/file?path=web/pic.svg", nil)
	if res.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "inline;") ||
		!strings.Contains(res.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("svg headers = %v", res.Header())
	}
	res = c.do("GET", "/api/file?path="+urlQuery("web/文档 1.pdf"), nil)
	if res.Header().Get("Content-Type") != "application/pdf" || res.Header().Get("Content-Security-Policy") != "" ||
		res.Header().Get("Content-Disposition") != "inline; filename*=UTF-8''%E6%96%87%E6%A1%A3%201.pdf" {
		t.Fatalf("pdf headers = %v", res.Header())
	}
	expect(t, c.do("GET", "/api/file?path=missing.png", nil), 404)
	expect(t, c.do("GET", "/api/file?path=sub", nil), 404)
	expect(t, c.do("GET", "/api/file?path=.git/config", nil), 400)
}

func TestAIUnconfigured(t *testing.T) {
	f := newFixture(t)
	me := decode[jsonMap](t, f.loggedIn().do("GET", "/api/auth/me", nil))
	if me["ai_configured"] != false {
		t.Fatalf("me = %v", me)
	}
}

func TestMCPOverHTTP(t *testing.T) {
	f := newFixture(t)
	c := f.web()
	rpc := func(method string, params any, auth string) *jsonMap {
		t.Helper()
		res := c.do("POST", "/mcp", jsonMap{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, "Authorization", "Bearer "+auth)
		if res.Code != 200 {
			t.Fatalf("%s: %d %s", method, res.Code, res.Body)
		}
		m := decode[jsonMap](t, res)
		return &m
	}
	res := c.do("POST", "/mcp", jsonMap{"jsonrpc": "2.0", "id": 1, "method": "ping"}, "Authorization", "Bearer wrong")
	expect(t, res, 401)
	if res.Header().Get("WWW-Authenticate") != "Bearer" || decode[jsonMap](t, res)["error"] != "invalid or missing bearer token" {
		t.Fatalf("401 = %v %s", res.Header(), res.Body)
	}
	init := *rpc("initialize", jsonMap{"protocolVersion": "2025-06-18", "capabilities": jsonMap{}, "clientInfo": jsonMap{"name": "t", "version": "1"}}, f.token)
	result := init["result"].(jsonMap)
	if result["protocolVersion"] != "2025-06-18" || result["serverInfo"].(jsonMap)["version"] != "9.9.9-test" {
		t.Fatalf("initialize = %v", init)
	}
	note := c.do("POST", "/mcp", jsonMap{"jsonrpc": "2.0", "method": "notifications/initialized"}, "Authorization", "Bearer "+f.token)
	expect(t, note, 202)

	tools := (*rpc("tools/list", jsonMap{}, f.token))["result"].(jsonMap)["tools"].([]any)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.(jsonMap)["name"].(string))
	}
	for _, want := range []string{"read_note", "write_note", "search_notes", "save_conversation"} {
		if !slices.Contains(names, want) {
			t.Fatalf("tools = %v", names)
		}
	}
	call := func(name string, args jsonMap) jsonMap {
		t.Helper()
		return (*rpc("tools/call", jsonMap{"name": name, "arguments": args}, f.token))["result"].(jsonMap)
	}
	text := func(r jsonMap) string { return r["content"].([]any)[0].(jsonMap)["text"].(string) }
	if !strings.Contains(text(call("search_notes", jsonMap{"query": "alias"})), "sub/B.md") {
		t.Fatal("search_notes did not find sub/B.md")
	}
	if call("read_note", jsonMap{"path": ".git/config"})["isError"] != true {
		t.Fatal("read_note .git/config must fail")
	}
	if call("write_note", jsonMap{"path": "Knowledge/X.md", "content": "# X\n"})["isError"] == true {
		t.Fatal("write_note failed")
	}
	got := decode[jsonMap](t, f.loggedIn().do("GET", "/api/note?path=Knowledge/X.md", nil))
	if got["content"] != "# X\n" {
		t.Fatalf("note = %v", got)
	}
	saved := call("save_conversation", jsonMap{"title": "t", "messages": []jsonMap{{"role": "user", "content": "q"}}})
	if !strings.Contains(text(saved), "Chats/") || !strings.Contains(text(call("list_inbox", jsonMap{})), "Chats/") {
		t.Fatalf("save_conversation = %v", saved)
	}

	// ?token= for clients that cannot set headers.
	res = c.do("POST", "/mcp?token="+f.token, jsonMap{"jsonrpc": "2.0", "id": 2, "method": "ping"})
	expect(t, res, 200)
	expect(t, c.do("POST", "/mcp?token=wrong", jsonMap{"jsonrpc": "2.0", "id": 2, "method": "ping"}), 401)
	// The login session is not an MCP credential (as in Python).
	expect(t, f.loggedIn().do("POST", "/mcp", jsonMap{"jsonrpc": "2.0", "id": 3, "method": "ping"}), 401)
	// The app's own pages need no token.
	expect(t, f.app().do("POST", "/mcp", jsonMap{"jsonrpc": "2.0", "id": 4, "method": "ping"}), 200)

	for _, method := range []string{"GET", "DELETE"} {
		res := c.do(method, "/mcp", nil)
		expect(t, res, 405)
		if res.Header().Get("Allow") != "POST" {
			t.Fatalf("%s /mcp Allow = %q", method, res.Header().Get("Allow"))
		}
	}
	res = c.do("POST", "/mcp", "{oops", "Authorization", "Bearer "+f.token)
	expect(t, res, 400)
	if !strings.Contains(res.Body.String(), "-32700") {
		t.Fatalf("parse error = %s", res.Body)
	}
}

// ------------------------------------------------------- authentication

func TestBearerAccessToREST(t *testing.T) {
	f := newFixture(t)
	c := f.bearer()
	expect(t, c.do("GET", "/api/tree", nil), 200)
	// Token clients may call from any origin: the guard is for cookies.
	expect(t, c.do("PUT", "/api/note", jsonMap{"path": "A.md", "content": "x"}, "Origin", "https://tool.example"), 200)
	expect(t, f.web().do("GET", "/api/tree", nil, "Authorization", "Bearer wrong"), 401)
	expect(t, f.web().do("GET", "/api/tree", nil, "Authorization", "Basic "+f.token), 401)
	res := f.web().do("GET", "/api/tree", nil)
	if res.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("401 without WWW-Authenticate")
	}
	me := decode[jsonMap](t, c.do("GET", "/api/auth/me", nil))
	if me["auth"] != "token" || me["desktop"] != false || me["vault"].(jsonMap)["path"] != nil {
		t.Fatalf("me with token = %v", me)
	}
}

func TestCookieOriginGuard(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) { s.AllowedOrigins = []string{"https://notes.example"} }
	})
	c := f.loggedIn()
	body := jsonMap{"path": "A.md", "content": "x"}
	res := c.do("PUT", "/api/note", body, "Origin", "https://evil.example")
	expect(t, res, 403)
	if decode[jsonMap](t, res)["detail"] != "origin is not allowed" {
		t.Fatalf("403 = %s", res.Body)
	}
	expect(t, c.do("PUT", "/api/note", body, "Origin", "http://example.com"), 200) // the request's own origin
	expect(t, c.do("PUT", "/api/note", body, "Origin", "https://notes.example"), 200)
	expect(t, c.do("PUT", "/api/note", body), 200) // no Origin: not a browser
	expect(t, c.do("GET", "/api/tree", nil, "Origin", "https://evil.example"), 200)
	expect(t, f.web().do("POST", "/api/auth/login", jsonMap{"password": f.password}, "Origin", "https://evil.example"), 403)
	// Behind a TLS-terminating proxy on the loopback interface.
	proxied := f.client(f.srv.NetHandler(), "127.0.0.1:5000")
	proxied.cookies = c.cookies
	expect(t, proxied.do("PUT", "/api/note", body, "Origin", "https://example.com", "X-Forwarded-Proto", "https"), 200)
}

func TestCORS(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) { s.API.CORSOrigins = []string{"https://app.example"} }
	})
	c := f.web()
	res := c.do("OPTIONS", "/api/tree", nil, "Origin", "https://app.example", "Access-Control-Request-Method", "GET",
		"Access-Control-Request-Headers", "authorization")
	expect(t, res, 204)
	h := res.Header()
	if h.Get("Access-Control-Allow-Origin") != "https://app.example" || !strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
		h.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("preflight headers = %v", h)
	}
	res = f.bearer().do("GET", "/api/tree", nil, "Origin", "https://app.example")
	expect(t, res, 200)
	if res.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("headers = %v", res.Header())
	}
	res = f.bearer().do("GET", "/api/tree", nil, "Origin", "https://other.example")
	if res.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origin got CORS headers: %v", res.Header())
	}
}

func TestAppHandlerOriginRules(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	app := f.client(f.srv.AppHandler(), "mygo:1")
	body := jsonMap{"path": "A.md", "content": "x"}

	expect(t, app.do("GET", "/api/tree", nil), 200)                                  // navigation, <img>…
	expect(t, app.do("HEAD", "/api/tree", nil), 200)                                 //
	expect(t, app.do("PUT", "/api/note", body), 403)                                 // no Origin: not the app's fetch
	expect(t, app.do("PUT", "/api/note", body, "Referer", "mygo://localhost/"), 200) // WKWebView same-scheme fetch
	expect(t, app.do("PUT", "/api/note", body, "Referer", "mygo://localhost/sandbox.html"), 403)
	expect(t, app.do("PUT", "/api/note", body, "Referer", "mygo://evil/"), 403)
	expect(t, app.do("PUT", "/api/note", body, "Origin", "https://evil.example", "Referer", "mygo://localhost/"), 403)
	expect(t, app.do("PUT", "/api/note", body, "Origin", "mygo://localhost"), 200) // the app
	expect(t, app.do("PUT", "/api/note", body, "Origin", "https://evil.example"), 403)
	expect(t, app.do("GET", "/api/tree", nil, "Origin", "https://evil.example"), 403)
	expect(t, app.do("GET", "/api/tree", nil, "Origin", "http://localhost:5173"), 403) // not in dev

	dev := New(f.core, Options{AppOrigins: AppOrigins("http://localhost:5173/"), Static: testUI, Logger: quietLogger()})
	devApp := f.client(dev.AppHandler(), "mygo:1")
	res := devApp.do("PUT", "/api/note", body, "Origin", "http://localhost:5173")
	expect(t, res, 200)
	if res.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || res.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("dev CORS headers = %v", res.Header())
	}
	res = devApp.do("OPTIONS", "/api/note", nil, "Origin", "http://localhost:5173", "Access-Control-Request-Method", "PUT")
	expect(t, res, 204)
	if res.Header().Get("Access-Control-Allow-Methods") != "PUT" {
		t.Fatalf("preflight = %v", res.Header())
	}
}

func TestAppOrigins(t *testing.T) {
	got := AppOrigins("http://LocalHost:5173/src/main.jsx")
	if !slices.Contains(got, "mygo://localhost") || !slices.Contains(got, "http://localhost:5173") {
		t.Fatalf("AppOrigins = %v", got)
	}
	if len(AppOrigins("")) != len(AppOrigins("not a url")) {
		t.Fatal("an invalid dev URL must add nothing")
	}
}

func TestMeExtended(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) { s.AI.BaseURL = "http://127.0.0.1:3425/v1" }
	})
	res := f.app().do("GET", "/api/auth/me", nil)
	expect(t, res, 200)
	me := decode[jsonMap](t, res)
	vault := me["vault"].(jsonMap)
	features := me["features"].(jsonMap)
	if me["mode"] != "desktop" || me["version"] != "9.9.9-test" || me["desktop"] != true || me["auth"] != "app" ||
		me["user"] != "cliecy" || me["ai_provider"] != "magpie" || me["ai_base_url"] != "http://127.0.0.1:3425/v1" ||
		me["ai_configured"] != true || vault["configured"] != true || vault["name"] != "notes" || vault["path"] != f.notes ||
		features["settings"] != true || features["integrations"] != true || features["openapi"] != true {
		t.Fatalf("me = %v", me)
	}
	// Server mode, browser session: the Python fields plus the extensions.
	g := newFixture(t)
	me = decode[jsonMap](t, g.loggedIn().do("GET", "/api/auth/me", nil))
	for _, key := range []string{"user", "ai_configured", "default_model", "git", "assets_dir"} {
		if _, ok := me[key]; !ok {
			t.Fatalf("me lacks %s: %v", key, me)
		}
	}
	if me["mode"] != "server" || me["desktop"] != false || me["ai_provider"] != "custom" || me["assets_dir"] != "_assets" {
		t.Fatalf("me = %v", me)
	}
}

func TestNoVault(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop; o.noVault = true })
	app := f.app()
	for _, target := range []string{"/api/tree", "/api/note?path=A.md", "/api/search?q=x", "/api/index", "/api/graph",
		"/api/git/status", "/api/history?path=A.md", "/api/chats", "/api/ai/prompts", "/api/file?path=x.png"} {
		res := app.do("GET", target, nil)
		expect(t, res, 409)
		if body := decode[jsonMap](t, res); body["detail"] != "未选择笔记库" || body["code"] != "no_vault" {
			t.Fatalf("%s: %v", target, body)
		}
	}
	expect(t, app.do("PUT", "/api/note", jsonMap{"path": "A.md", "content": ""}), 409)
	expect(t, app.do("POST", "/api/ai/chat", jsonMap{"messages": []any{}}), 409)

	me := decode[jsonMap](t, app.do("GET", "/api/auth/me", nil))
	if me["vault"].(jsonMap)["configured"] != false {
		t.Fatalf("me = %v", me)
	}
	expect(t, app.do("GET", "/api/ai/models", nil), 200)
	expect(t, app.do("GET", "/api/mcp-info", nil), 200)
	res := app.do("POST", "/mcp", jsonMap{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": jsonMap{"name": "list_notes", "arguments": jsonMap{}}})
	expect(t, res, 200)
	if !strings.Contains(res.Body.String(), "错误：未选择笔记库") {
		t.Fatalf("mcp without vault = %s", res.Body)
	}

	// Choosing a vault makes the routes work at once.
	dir := writeVault(t, t.TempDir()+"/vault")
	expect(t, app.do("PUT", "/api/app/settings", jsonMap{"notes_dir": dir}), 200)
	expect(t, app.do("GET", "/api/tree", nil), 200)
	if !strings.Contains(app.do("GET", "/api/auth/me", nil).Body.String(), `"configured":true`) {
		t.Fatal("me still reports no vault")
	}
}

func TestSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	res := f.loggedIn().do("GET", "/api/tree", nil)
	h := res.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" || h.Get("Content-Security-Policy") != appCSP {
		t.Fatalf("headers = %v", h)
	}
	res = f.app().do("GET", "/sandbox.html", nil)
	expect(t, res, 200)
	if res.Header().Get("Content-Security-Policy") != sandboxCSP || res.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("sandbox headers = %v", res.Header())
	}
	if !strings.Contains(sandboxCSP, "connect-src 'none'") || !strings.HasPrefix(sandboxCSP, "sandbox allow-scripts;") {
		t.Fatal("sandbox CSP changed")
	}
}

func TestClientErrorAndValidation(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	expect(t, c.do("POST", "/api/client-error", jsonMap{"message": "boom", "stack": "at x"}), 200)
	expect(t, c.do("POST", "/api/client-error", jsonMap{"message": strings.Repeat("é", 4001)}), 422)
	expect(t, f.web().do("POST", "/api/client-error", jsonMap{"message": "boom"}), 401)
	expect(t, c.do("PUT", "/api/note", jsonMap{"path": "A.md"}), 422)
	expect(t, c.do("PUT", "/api/note", jsonMap{"path": 3, "content": "x"}), 422)
	expect(t, c.do("POST", "/api/ai/agent", jsonMap{"task": strings.Repeat("x", maxTaskChars+1)}), 422)
}

func TestUploadLimit(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) { s.MaxUploadBytes = 16 }
	})
	c := f.loggedIn()
	upload := func(name, content, folder string) *httptestRecorder {
		body, ctype := multipartBody(t, name, content, folder)
		return c.do("POST", "/api/upload", body, "Content-Type", ctype)
	}
	res := upload("photo 1.png", "0123456789abcdef", "")
	expect(t, res, 200)
	if got := fmt.Sprint(decode[jsonMap](t, res)["path"]); !strings.HasPrefix(got, "_assets/") || !strings.HasSuffix(got, ".png") {
		t.Fatalf("path = %v", got)
	}
	res = upload("big.bin", "0123456789abcdefX", "")
	expect(t, res, 413)
	if decode[jsonMap](t, res)["detail"] != "文件太大" {
		t.Fatalf("413 = %s", res.Body)
	}
	res = upload("n.txt", "hi", "Inbox/files")
	expect(t, res, 200)
	if got := fmt.Sprint(decode[jsonMap](t, res)["path"]); !strings.HasPrefix(got, "Inbox/files/") {
		t.Fatalf("path = %v", got)
	}
	expect(t, c.do("POST", "/api/upload", "plain", "Content-Type", "text/plain"), 422)
}

func TestChatsRoutes(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	res := c.do("PUT", "/api/chats", jsonMap{"title": "PPO 是什么", "model": "m", "messages": []jsonMap{{"role": "user", "content": "什么是 PPO?"}, {"role": "assistant", "content": "答"}}})
	expect(t, res, 200)
	saved := decode[jsonMap](t, res)
	path := saved["path"].(string)
	if !strings.HasPrefix(path, "Chats/") || saved["meta"].(jsonMap)["status"] != "inbox" {
		t.Fatalf("saved = %v", saved)
	}
	loaded := decode[jsonMap](t, c.do("GET", "/api/chats/load?path="+urlQuery(path), nil))
	if len(loaded["messages"].([]any)) != 2 {
		t.Fatalf("loaded = %v", loaded)
	}
	list := decode[jsonMap](t, c.do("GET", "/api/chats", nil))
	if len(list["chats"].([]any)) != 1 {
		t.Fatalf("list = %v", list)
	}
	expect(t, c.do("PUT", "/api/chats", jsonMap{"path": "A.md", "messages": []any{}}), 400)
	expect(t, c.do("PUT", "/api/chats", jsonMap{"title": "x"}), 422)
}

func TestAIApply(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	note := decode[jsonMap](t, c.do("GET", "/api/note?path=A.md", nil))
	res := c.do("POST", "/api/ai/apply", jsonMap{"summary": "x", "operations": []jsonMap{{"op": "update", "path": "A.md", "after": "y\n", "base_etag": "stale"}}})
	expect(t, res, 400)
	if decode[jsonMap](t, res)["detail"] != "A.md 在生成后被修改过，请重新生成" {
		t.Fatalf("stale = %s", res.Body)
	}
	res = c.do("POST", "/api/ai/apply", jsonMap{"summary": "x", "operations": []jsonMap{
		{"op": "update", "path": "A.md", "after": "updated\n", "base_etag": note["etag"]},
		{"op": "create", "path": "new/C.md", "after": "# C\n"},
	}})
	expect(t, res, 200)
	if got := fmt.Sprint(decode[jsonMap](t, res)["touched"]); got != "[A.md new/C.md]" {
		t.Fatalf("touched = %v", got)
	}
}

func TestUnknownRoutes(t *testing.T) {
	f := newFixture(t)
	c := f.loggedIn()
	res := c.do("GET", "/api/nope", nil)
	expect(t, res, 404)
	if decode[jsonMap](t, res)["detail"] != "Not Found" {
		t.Fatalf("404 = %s", res.Body)
	}
	res = c.do("PATCH", "/api/tree", nil)
	expect(t, res, 405)
	if !strings.Contains(res.Header().Get("Allow"), "GET") {
		t.Fatalf("Allow = %q", res.Header().Get("Allow"))
	}
	res = c.do("GET", "/healthz", nil)
	expect(t, res, 200)
	if res.Body.String() != `{"ok":true}` {
		t.Fatalf("healthz = %s", res.Body)
	}
}
