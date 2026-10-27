package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marginalia/internal/config"
	"marginalia/internal/mcp"
)

// appRoutes are the app-only endpoints with a harmless body each.
var appRoutes = []struct {
	method, path string
	body         any
}{
	{"GET", "/api/app/settings", nil},
	{"PUT", "/api/app/settings", jsonMap{}},
	{"POST", "/api/app/vault/init", jsonMap{"path": "/nonexistent/should-not-be-created"}},
	{"GET", "/api/app/integrations", nil},
	{"POST", "/api/app/token/rotate", nil},
	{"POST", "/api/app/magpie/use", jsonMap{}},
	{"POST", "/api/app/magpie/register-mcp", jsonMap{}},
	{"POST", "/api/app/magpie/unregister-mcp", nil},
}

func TestAppEndpointsRejectTheToken(t *testing.T) {
	f := newFixture(t)
	for _, rt := range appRoutes {
		res := f.bearer().do(rt.method, rt.path, rt.body)
		expect(t, res, 403)
		if body := decode[jsonMap](t, res); body["code"] != "app_only" {
			t.Fatalf("%s %s: %v", rt.method, rt.path, body)
		}
		expect(t, f.web().do(rt.method, rt.path, rt.body), 401)
	}
	if _, err := os.Stat("/nonexistent/should-not-be-created"); err == nil {
		t.Fatal("a rejected vault init created its folder")
	}
	// A browser session (password login) and the app may use them.
	expect(t, f.loggedIn().do("GET", "/api/app/settings", nil), 200)
	expect(t, f.app().do("GET", "/api/app/settings", nil), 200)
	expect(t, f.client(f.srv.AppHandler(), "mygo:1").do("PUT", "/api/app/settings", jsonMap{}), 403) // no Origin
}

func TestSettingsGetShape(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	res := f.app().do("GET", "/api/app/settings", nil)
	expect(t, res, 200)
	var got struct {
		NotesDir string `json:"notes_dir"`
		DataDir  string `json:"data_dir"`
		AI       struct {
			BaseURL      *string `json:"base_url"`
			HasKey       *bool   `json:"has_key"`
			DefaultModel *string `json:"default_model"`
			Provider     string  `json:"provider"`
		} `json:"ai"`
		API struct {
			Enabled     *bool     `json:"enabled"`
			Host        string    `json:"host"`
			Port        int       `json:"port"`
			CORSOrigins *[]string `json:"cors_origins"`
			Running     *bool     `json:"running"`
			URL         string    `json:"url"`
		} `json:"api"`
		Git struct {
			Autocommit         *bool    `json:"autocommit"`
			CommitDelaySeconds *float64 `json:"commit_delay_seconds"`
			AutosyncMinutes    *float64 `json:"autosync_minutes"`
			AuthorName         string   `json:"author_name"`
			AuthorEmail        string   `json:"author_email"`
		} `json:"git"`
		Appearance struct {
			Theme string `json:"theme"`
		} `json:"appearance"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.NotesDir != f.notes || got.DataDir != f.data || got.AI.BaseURL == nil || got.AI.HasKey == nil || *got.AI.HasKey ||
		got.AI.Provider != "custom" || got.API.Enabled == nil || got.API.Host != "127.0.0.1" || got.API.CORSOrigins == nil ||
		got.API.Running == nil || *got.API.Running || got.API.URL == "" || got.Git.Autocommit == nil ||
		got.Git.CommitDelaySeconds == nil || got.Git.AutosyncMinutes == nil || got.Git.AuthorName == "" || got.Appearance.Theme != "system" {
		t.Fatalf("settings = %s", res.Body)
	}
	if strings.Contains(res.Body.String(), f.token) || strings.Contains(res.Body.String(), f.password) {
		t.Fatal("settings leak a secret")
	}
}

func TestSettingsPutReloadsAndRestartsTheListener(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) { s.API.Enabled = true }
	})
	if err := f.srv.StartAPI(); err != nil {
		t.Fatal(err)
	}
	first := f.srv.APIStatus()
	if !first.Running || !probe(first.URL) {
		t.Fatalf("listener = %+v", first)
	}
	app := f.app()
	other := writeVault(t, filepath.Join(t.TempDir(), "Second Vault"))
	newPort := freePort(t)
	res := app.do("PUT", "/api/app/settings", jsonMap{
		"notes_dir":  other,
		"ai":         jsonMap{"base_url": "https://llm.example/v1/", "api_key": " sk-test ", "default_model": "gpt-x"},
		"api":        jsonMap{"port": newPort, "cors_origins": []string{"https://a.example/", "https://a.example"}},
		"git":        jsonMap{"commit_delay_seconds": 5, "author_name": "Me", "author_email": "me@example.com"},
		"appearance": jsonMap{"theme": "dark"},
		"data_dir":   "/ignored",
	})
	expect(t, res, 200)
	got := decode[jsonMap](t, res)
	ai, api, git := got["ai"].(jsonMap), got["api"].(jsonMap), got["git"].(jsonMap)
	wantURL := fmt.Sprintf("http://127.0.0.1:%d", newPort)
	if got["notes_dir"] != other || got["data_dir"] != f.data || ai["base_url"] != "https://llm.example/v1" || ai["has_key"] != true ||
		ai["default_model"] != "gpt-x" || api["port"] != float64(newPort) || api["running"] != true || api["url"] != wantURL ||
		fmt.Sprint(api["cors_origins"]) != "[https://a.example]" || git["commit_delay_seconds"] != float64(5) ||
		git["author_name"] != "Me" || got["appearance"].(jsonMap)["theme"] != "dark" {
		t.Fatalf("settings = %s", res.Body)
	}
	// The listener moved.
	if probe(first.URL) || !probe(wantURL) {
		t.Fatalf("old %s answers %v, new %s answers %v", first.URL, probe(first.URL), wantURL, probe(wantURL))
	}
	// The core was reloaded.
	if f.core.Vault().Root() != other || f.core.AI().BaseURL() != "https://llm.example/v1" || f.core.Settings().AIAPIKey() != "sk-test" {
		t.Fatalf("core: vault %s, ai %s", f.core.Vault().Root(), f.core.AI().BaseURL())
	}
	me := decode[jsonMap](t, app.do("GET", "/api/auth/me", nil))
	if me["ai_configured"] != true || me["vault"].(jsonMap)["path"] != other || me["default_model"] != "gpt-x" {
		t.Fatalf("me = %v", me)
	}
	// settings.json was written (and never holds the key).
	saved, err := config.LoadEnv(config.ModeDesktop, func(name string) (string, bool) {
		if name == "NOTES_DATA_DIR" {
			return f.data, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.NotesDir != other || saved.API.Port != newPort || saved.Appearance.Theme != "dark" || saved.AI.DefaultModel != "gpt-x" {
		t.Fatalf("settings.json = %+v", saved)
	}
	if data, _ := os.ReadFile(saved.SettingsPath()); strings.Contains(string(data), "sk-test") {
		t.Fatal("settings.json holds the API key")
	}

	// "" clears the key; switching the API off stops the listener.
	res = app.do("PUT", "/api/app/settings", jsonMap{"ai": jsonMap{"api_key": ""}, "api": jsonMap{"enabled": false}})
	expect(t, res, 200)
	got = decode[jsonMap](t, res)
	if got["ai"].(jsonMap)["has_key"] != false || got["api"].(jsonMap)["running"] != false || probe(wantURL) {
		t.Fatalf("settings = %s", res.Body)
	}

	// Validation: nothing changes on a bad value.
	for _, bad := range []jsonMap{
		{"notes_dir": "relative/path"},
		{"notes_dir": filepath.Join(other, "missing")},
		{"notes_dir": filepath.Join(other, "A.md")},
		{"api": jsonMap{"port": 70000}},
		{"api": jsonMap{"host": "example.com"}},
		{"api": jsonMap{"cors_origins": []string{"https://a.example/path"}}},
		{"ai": jsonMap{"base_url": "ftp://x"}},
		{"git": jsonMap{"commit_delay_seconds": -1}},
		{"appearance": jsonMap{"theme": "pink"}},
	} {
		res := app.do("PUT", "/api/app/settings", bad)
		expect(t, res, 400)
		if decode[jsonMap](t, res)["detail"] == "" {
			t.Fatalf("%v: no detail", bad)
		}
	}
	expect(t, app.do("PUT", "/api/app/settings", jsonMap{"api": jsonMap{"port": "8190"}}), 422)
	if f.core.Settings().NotesDir != other {
		t.Fatal("a rejected update changed the settings")
	}
}

// probe reports whether a Marginalia API answers /healthz at base.
func probe(base string) bool {
	return mcp.Probe(context.Background(), base+"/healthz")
}

func TestListenerPortFallback(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) { s.API.Enabled, s.API.Port = true, port }
	})
	if err := f.srv.StartAPI(); err != nil {
		t.Fatal(err)
	}
	st := f.srv.APIStatus()
	var got int
	if _, err := fmt.Sscanf(st.URL, "http://127.0.0.1:%d", &got); err != nil || got <= port || got > port+PortFallbacks || !st.Running {
		t.Fatalf("status = %+v", st)
	}
	if !probe(st.URL) {
		t.Fatal("the fallback listener does not answer")
	}
	// `marginalia mcp` finds the real address.
	if u := LocalAPIURL(f.core.Settings()); u != st.URL {
		t.Fatalf("LocalAPIURL = %q, want %q", u, st.URL)
	}
	integrations := decode[jsonMap](t, f.app().do("GET", "/api/app/integrations", nil))["api"].(jsonMap)
	if integrations["url"] != st.URL || integrations["mcp_url"] != st.URL+"/mcp" || integrations["running"] != true {
		t.Fatalf("integrations.api = %v", integrations)
	}
	if err := f.srv.StopAPI(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.srv.APIStatus().Running || probe(st.URL) {
		t.Fatal("the listener still runs")
	}
	if u := LocalAPIURL(f.core.Settings()); u != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("LocalAPIURL after stop = %q", u)
	}
}

func TestListenerNoFreePort(t *testing.T) {
	var held []net.Listener
	defer func() {
		for _, ln := range held {
			ln.Close()
		}
	}()
	first := 0
	for first == 0 {
		port := freePort(t)
		if port+PortFallbacks > 65535 {
			continue
		}
		ok := true
		for p := port; p <= port+PortFallbacks; p++ {
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				ok = false
				break
			}
			held = append(held, ln)
		}
		if ok {
			first = port
			break
		}
		for _, ln := range held {
			ln.Close()
		}
		held = nil
	}
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) { s.API.Enabled, s.API.Port = true, first }
	})
	if err := f.srv.StartAPI(); err == nil {
		t.Fatal("StartAPI succeeded with every port taken")
	}
	st := f.srv.APIStatus()
	if st.Running || st.Error == "" || !st.Enabled {
		t.Fatalf("status = %+v", st)
	}
	if api := decode[jsonMap](t, f.app().do("GET", "/api/app/settings", nil))["api"].(jsonMap); api["error"] == nil {
		t.Fatalf("settings.api = %v", api)
	}
}

func TestServeShutsDownGracefullyAndFlushesGit(t *testing.T) {
	needGit(t)
	f := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) { s.Git.Autocommit, s.Git.CommitDelaySeconds = true, 3600 }
	})
	// The fixture's .git is an empty folder: make it a real repository and
	// reopen the core on it.
	if err := os.RemoveAll(filepath.Join(f.notes, ".git")); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, f.notes, "init", "-q")
	if err := f.core.Apply(f.core.Settings()); err != nil {
		t.Fatal(err)
	}
	if !f.core.Git().Enabled() {
		t.Fatal("the repository was not reopened")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.srv.Serve(ctx, "127.0.0.1:0") }()
	var base string
	for deadline := time.Now().Add(5 * time.Second); ; {
		if st := f.srv.APIStatus(); st.Running {
			base = st.URL
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Serve did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	req, _ := http.NewRequest("PUT", base+"/api/note", strings.NewReader(`{"path":"A.md","content":"edited\n"}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}
	if n := strings.TrimSpace(gitCmd(t, f.notes, "rev-list", "--all", "--count")); n != "0" {
		t.Fatalf("committed before the debounce delay (%s commits)", n)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return")
	}
	if probe(base) {
		t.Fatal("still listening after shutdown")
	}
	if out := gitCmd(t, f.notes, "log", "--format=%s"); !strings.Contains(out, "web: edit A.md") {
		t.Fatalf("the pending edit was not committed on shutdown: %q", out)
	}
}

// gitCmd runs git in dir and returns its output.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestTokenRotation(t *testing.T) {
	f := newFixture(t)
	res := f.app().do("POST", "/api/app/token/rotate", nil)
	expect(t, res, 200)
	token := decode[jsonMap](t, res)["token"].(string)
	if token == "" || token == f.token {
		t.Fatalf("token = %q", token)
	}
	expect(t, f.bearer().do("GET", "/api/tree", nil), 401) // the old token
	expect(t, f.web().do("GET", "/api/tree", nil, "Authorization", "Bearer "+token), 200)
	if readSecret(t, filepath.Join(f.data, "mcp_token")) != token {
		t.Fatal("the new token was not saved")
	}
	if got := decode[jsonMap](t, f.loggedIn().do("GET", "/api/mcp-info", nil))["token"]; got != token {
		t.Fatalf("mcp-info = %v", got)
	}
}

func TestVaultInit(t *testing.T) {
	needGit(t)
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.noVault = true
		o.settings = func(s *settingsT) { s.Git.Autocommit = true }
	})
	dir := filepath.Join(t.TempDir(), "我的笔记")
	res := f.app().do("POST", "/api/app/vault/init", jsonMap{"path": dir})
	expect(t, res, 200)
	got := decode[jsonMap](t, res)
	real, _ := filepath.EvalSymlinks(dir)
	if got["notes_dir"] != real || got["git"] != true || fmt.Sprint(got["created"]) != "[AGENTS.md index.md .gitignore]" {
		t.Fatalf("init = %s", res.Body)
	}
	for _, folder := range []string{"Inbox", "Knowledge", "Chats", "_assets"} {
		if info, err := os.Stat(filepath.Join(dir, folder)); err != nil || !info.IsDir() {
			t.Fatalf("folder %s: %v", folder, err)
		}
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	for _, want := range []string{"Inbox/", "Knowledge/", "Chats/", "_assets/", "index.md", "title:", "summary:", "tags:", "type:", "status:", "updated:", "[[笔记名]]"} {
		if !strings.Contains(string(agents), want) {
			t.Fatalf("AGENTS.md lacks %q:\n%s", want, agents)
		}
	}
	if log := gitCmd(t, dir, "log", "--format=%s"); strings.TrimSpace(log) != initCommitMessage {
		t.Fatalf("git log = %q", log)
	}
	if files := gitCmd(t, dir, "ls-files"); files != ".gitignore\nAGENTS.md\nindex.md\n" {
		t.Fatalf("committed files = %q", files)
	}
	// The vault is open, with history.
	app := f.app()
	tree := app.do("GET", "/api/tree", nil)
	expect(t, tree, 200)
	if !strings.Contains(tree.Body.String(), `"AGENTS.md"`) || strings.Contains(tree.Body.String(), ".gitignore") {
		t.Fatalf("tree = %s", tree.Body)
	}
	note := decode[jsonMap](t, app.do("GET", "/api/note?path=AGENTS.md", nil))
	if !strings.Contains(note["content"].(string), "知识库约定") {
		t.Fatalf("AGENTS.md = %v", note)
	}
	if h := decode[jsonMap](t, app.do("GET", "/api/history?path=AGENTS.md", nil))["history"].([]any); len(h) != 1 {
		t.Fatalf("history = %v", h)
	}
	index := decode[jsonMap](t, app.do("GET", "/api/index", nil))["notes"].([]any)
	if len(index) != 2 {
		t.Fatalf("index = %v", index)
	}

	// Adopting it again changes nothing and never overwrites.
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res = f.app().do("POST", "/api/app/vault/init", jsonMap{"path": dir})
	expect(t, res, 200)
	if fmt.Sprint(decode[jsonMap](t, res)["created"]) != "[]" {
		t.Fatalf("re-init = %s", res.Body)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); string(data) != "mine\n" {
		t.Fatal("AGENTS.md was overwritten")
	}

	expect(t, f.app().do("POST", "/api/app/vault/init", jsonMap{"path": "relative"}), 400)
	expect(t, f.app().do("POST", "/api/app/vault/init", jsonMap{"path": filepath.Join(dir, "AGENTS.md")}), 400)
	expect(t, f.app().do("POST", "/api/app/vault/init", jsonMap{}), 422)
}

func TestVaultInitOfTheOpenVaultEnablesGit(t *testing.T) {
	needGit(t)
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) { s.Git.Autocommit = true }
	})
	if err := os.RemoveAll(filepath.Join(f.notes, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := f.core.Apply(f.core.Settings()); err != nil {
		t.Fatal(err)
	}
	if f.core.Git().Enabled() {
		t.Fatal("git enabled without a repository")
	}
	expect(t, f.app().do("POST", "/api/app/vault/init", jsonMap{"path": f.notes}), 200)
	if !f.core.Git().Enabled() {
		t.Fatal("initialising the open vault did not enable git")
	}
	status := decode[jsonMap](t, f.app().do("GET", "/api/git/status", nil))
	if status["enabled"] != true {
		t.Fatalf("git status = %v", status)
	}
}

func TestMagpieIntegration(t *testing.T) {
	m := newFakeMagpie(t)
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.magpie = m.client
		o.settings = func(s *settingsT) { s.API.Enabled = true }
	})
	if err := f.srv.StartAPI(); err != nil {
		t.Fatal(err)
	}
	app := f.app()

	view := decode[jsonMap](t, app.do("GET", "/api/app/integrations", nil))
	api, mag := view["api"].(jsonMap), view["magpie"].(jsonMap)
	gw := mag["gateway"].(jsonMap)
	if api["token"] != f.token || api["running"] != true || api["command"] != testExecutable || api["openapi_url"] != api["url"].(string)+"/api/openapi.json" ||
		gw["running"] != true || gw["version"] != "0.1.1564" || gw["models"] != float64(2) || mag["cli"] != m.client.CLIPath ||
		mag["mcp_registered"] != false {
		t.Fatalf("integrations = %v", view)
	}

	res := app.do("POST", "/api/app/magpie/register-mcp", jsonMap{"transport": "stdio"})
	expect(t, res, 200)
	out := decode[jsonMap](t, res)
	wantCmd := m.client.CLIPath + " library mcp add marginalia " + testExecutable + " mcp"
	if out["ok"] != true || out["command"] != wantCmd || !strings.Contains(out["output"].(string), "added") {
		t.Fatalf("register = %v", out)
	}
	if decode[jsonMap](t, app.do("GET", "/api/app/integrations", nil))["magpie"].(jsonMap)["mcp_registered"] != true {
		t.Fatal("registration not reflected")
	}

	res = app.do("POST", "/api/app/magpie/register-mcp", jsonMap{"transport": "http"})
	expect(t, res, 200)
	out = decode[jsonMap](t, res)
	if !strings.Contains(out["command"].(string), "/mcp?token="+f.token) || !strings.Contains(out["command"].(string), f.srv.APIStatus().URL) {
		t.Fatalf("http register = %v", out)
	}
	expect(t, app.do("POST", "/api/app/magpie/register-mcp", jsonMap{"transport": "ws"}), 422)

	res = app.do("POST", "/api/app/magpie/unregister-mcp", nil)
	expect(t, res, 200)
	if out := decode[jsonMap](t, res); out["ok"] != true || out["command"] != m.client.CLIPath+" library mcp rm marginalia" {
		t.Fatalf("unregister = %v", out)
	}
	if decode[jsonMap](t, app.do("GET", "/api/app/integrations", nil))["magpie"].(jsonMap)["mcp_registered"] != false {
		t.Fatal("unregistration not reflected")
	}
	args := m.args(t)
	for _, want := range []string{"library mcp add marginalia " + testExecutable + " mcp", "library mcp rm marginalia"} {
		if !strings.Contains(args, want) {
			t.Fatalf("CLI calls = %q", args)
		}
	}

	// Use Magpie as the AI provider: its default routing group is picked.
	res = app.do("POST", "/api/app/magpie/use", nil)
	expect(t, res, 200)
	ai := decode[jsonMap](t, res)["ai"].(jsonMap)
	if ai["base_url"] != "http://127.0.0.1:3425/v1" || ai["provider"] != "magpie" || ai["default_model"] != "group/default" || ai["has_key"] != true {
		t.Fatalf("use = %v", ai)
	}
	if f.core.Settings().AIAPIKey() != "magpie" {
		t.Fatal("the Magpie key was not stored")
	}
	res = app.do("POST", "/api/app/magpie/use", jsonMap{"model": "codex/gpt-6.1-sol"})
	expect(t, res, 200)
	if decode[jsonMap](t, res)["ai"].(jsonMap)["default_model"] != "codex/gpt-6.1-sol" {
		t.Fatalf("use with model = %s", res.Body)
	}
	if me := decode[jsonMap](t, app.do("GET", "/api/auth/me", nil)); me["ai_provider"] != "magpie" || me["ai_configured"] != true {
		t.Fatalf("me = %v", me)
	}
}

func TestMagpieMissing(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) {
			s.AI.BaseURL = "http://127.0.0.1:3425/v1"
			s.AI.DefaultModel = "group/gpt-sol-main"
		}
	})
	app := f.app()
	view := decode[jsonMap](t, app.do("GET", "/api/app/integrations", nil))
	mag := view["magpie"].(jsonMap)
	if mag["cli"] != "" || mag["mcp_registered"] != nil || mag["gateway"].(jsonMap)["running"] != false || mag["install_url"] == "" {
		t.Fatalf("integrations = %v", view)
	}
	res := app.do("POST", "/api/app/magpie/register-mcp", nil)
	expect(t, res, 409)
	if body := decode[jsonMap](t, res); body["code"] != "magpie_cli_missing" || !strings.Contains(body["command"].(string), "library mcp add marginalia") {
		t.Fatalf("register without CLI = %v", body)
	}
	// Switching without a reachable gateway keeps a Magpie model already chosen.
	f.srv.magpie.GatewayURL = closedURL(t)
	res = app.do("POST", "/api/app/magpie/use", nil)
	expect(t, res, 200)
	if decode[jsonMap](t, res)["ai"].(jsonMap)["default_model"] != "group/gpt-sol-main" {
		t.Fatalf("use = %s", res.Body)
	}
}

func TestMagpieHTTPRegistrationNeedsTheAPI(t *testing.T) {
	m := newFakeMagpie(t)
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop; o.magpie = m.client })
	res := f.app().do("POST", "/api/app/magpie/register-mcp", jsonMap{"transport": "http"})
	expect(t, res, 409)
	if decode[jsonMap](t, res)["code"] != "api_disabled" {
		t.Fatalf("http register with the API off = %s", res.Body)
	}
	if m.args(t) != "" {
		t.Fatal("the CLI ran")
	}
}

func TestIntegrationsTokenOnlyForAppAndSession(t *testing.T) {
	f := newFixture(t)
	view := decode[jsonMap](t, f.loggedIn().do("GET", "/api/app/integrations", nil))
	if view["api"].(jsonMap)["token"] != f.token {
		t.Fatalf("session integrations = %v", view)
	}
}
