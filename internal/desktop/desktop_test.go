package desktop

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/egoist/mygo"

	"marginalia/internal/config"
	"marginalia/internal/server"
	"marginalia/internal/vault"
)

func strPtr(s string) *string { return &s }

func TestParseDeepLink(t *testing.T) {
	tests := []struct {
		raw  string
		want OpenRequest
	}{
		{"marginalia://open?path=Knowledge/RL/PPO.md", OpenRequest{Path: "Knowledge/RL/PPO.md"}},
		{"marginalia://open?path=%E7%AC%94%E8%AE%B0%2FA.md", OpenRequest{Path: "笔记/A.md"}},
		{"Marginalia://OPEN?path=/A.md/", OpenRequest{Path: "/A.md"}}, // absolute: mapped into the vault later
		{"marginalia:///open?path=A.md", OpenRequest{Path: "A.md"}},
		{"marginalia:open?path=A.md", OpenRequest{Path: "A.md"}},
		{"marginalia://search?q=transformer%20attention", OpenRequest{Q: strPtr("transformer attention")}},
		{"marginalia://search?q=", OpenRequest{Q: strPtr("")}},
		{"marginalia://search", OpenRequest{Q: strPtr("")}},
		{" marginalia://search/?q=x ", OpenRequest{Q: strPtr("x")}},
	}
	for _, tt := range tests {
		got, err := ParseDeepLink(tt.raw)
		if err != nil {
			t.Errorf("ParseDeepLink(%q): %v", tt.raw, err)
			continue
		}
		if got.Path != tt.want.Path || (got.Q == nil) != (tt.want.Q == nil) || (got.Q != nil && *got.Q != *tt.want.Q) {
			t.Errorf("ParseDeepLink(%q) = %+v, want %+v", tt.raw, got, tt.want)
		}
	}
	for _, raw := range []string{
		"https://example.com/open?path=A.md",
		"marginalia://open",
		"marginalia://open?path=",
		"marginalia://open?path=%2F%2F",
		"marginalia://delete?path=A.md",
		"marginalia://open?path=a%00b",
		"::",
	} {
		if req, err := ParseDeepLink(raw); !errors.Is(err, errNotADeepLink) {
			t.Errorf("ParseDeepLink(%q) = %+v, %v; want errNotADeepLink", raw, req, err)
		}
	}
}

// TestOpenRequestJSON checks the payload the page gets, encoded as MyGo
// encodes events (encoding/json/v2): {path} or {q}, an empty query kept.
func TestOpenRequestJSON(t *testing.T) {
	for req, want := range map[*OpenRequest]string{
		{Path: "A.md"}:    `{"path":"A.md"}`,
		{Q: strPtr("")}:   `{"q":""}`,
		{Q: strPtr("rl")}: `{"q":"rl"}`,
	} {
		got, err := json.Marshal(*req)
		if err != nil || string(got) != want {
			t.Errorf("json(%+v) = %s, %v; want %s", *req, got, err, want)
		}
	}
}

func TestVaultRelative(t *testing.T) {
	root := t.TempDir()
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"A.md":                                 "A.md",
		`sub\B.md`:                             "sub/B.md",
		filepath.Join(v.Root(), "sub", "B.md"): "sub/B.md",
	} {
		if got, err := vaultRelative(v, in); err != nil || got != want {
			t.Errorf("vaultRelative(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"../etc/passwd", ".git/config", "sub/../../x.md", v.Root(), filepath.Join(filepath.Dir(v.Root()), "other.md")} {
		if got, err := vaultRelative(v, in); err == nil {
			t.Errorf("vaultRelative(%q) = %q; want an error", in, got)
		}
	}
	// Without a vault, relative paths pass, absolute ones cannot be mapped.
	if got, err := vaultRelative(nil, "A.md"); err != nil || got != "A.md" {
		t.Errorf("vaultRelative(nil, A.md) = %q, %v", got, err)
	}
	if _, err := vaultRelative(nil, filepath.Join(root, "A.md")); err == nil {
		t.Error("vaultRelative(nil, absolute) must fail")
	}
}

func TestDeepLinkArgs(t *testing.T) {
	args := []string{"-psn_0_123", "marginalia://open?path=A.md", "notes.md", "MARGINALIA://search?q=x", "https://example.com"}
	got := deepLinkArgs(args)
	want := []string{"marginalia://open?path=A.md", "MARGINALIA://search?q=x"}
	if !slices.Equal(got, want) {
		t.Fatalf("deepLinkArgs = %q, want %q", got, want)
	}
}

func TestExternalURL(t *testing.T) {
	for _, ok := range []string{"https://example.com/a?b=c#d", "http://127.0.0.1:8190/api/openapi.json", "mailto:me@example.com", " HTTPS://Example.com "} {
		if _, err := externalURL(ok); err != nil {
			t.Errorf("externalURL(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "marginalia://open?path=A.md", "https://", "mailto:", "/relative", "", "vscode://file/x"} {
		if got, err := externalURL(bad); err == nil {
			t.Errorf("externalURL(%q) = %q; want an error", bad, got)
		}
	}
}

func TestThemeSource(t *testing.T) {
	for theme, want := range map[string]mygo.ThemeSource{
		config.ThemeSystem: mygo.ThemeSystem,
		config.ThemeLight:  mygo.ThemeLight,
		config.ThemeDark:   mygo.ThemeDark,
		"":                 mygo.ThemeSystem,
		"sepia":            mygo.ThemeSystem,
	} {
		if got := themeSource(theme); got != want {
			t.Errorf("themeSource(%q) = %q, want %q", theme, got, want)
		}
	}
}

func TestIsAppURL(t *testing.T) {
	a := &app{origins: server.AppOrigins("http://localhost:5173")}
	for _, ok := range []string{"mygo://localhost/", "mygo://localhost/#!/notes/A.md", "MYGO://LOCALHOST/x", "http://localhost:5173/", "about:blank"} {
		if !a.isAppURL(ok) {
			t.Errorf("isAppURL(%q) = false", ok)
		}
	}
	for _, bad := range []string{"https://example.com/", "http://localhost:5174/", "mygo://evil/", "file:///etc/passwd", "http://127.0.0.1:8190/"} {
		if a.isAppURL(bad) {
			t.Errorf("isAppURL(%q) = true", bad)
		}
	}
}

// allItems flattens a menu template.
func allItems(items []*mygo.MenuItem) []*mygo.MenuItem {
	var out []*mygo.MenuItem
	for _, it := range items {
		out = append(out, it)
		out = append(out, allItems(it.Submenu)...)
	}
	return out
}

func TestMenuTemplate(t *testing.T) {
	a := &app{name: "Marginalia"}
	template := a.menuTemplate()
	var tops []string
	for _, it := range template {
		tops = append(tops, it.Label)
	}
	want := []string{"文件", "编辑", "视图", "窗口", "帮助"}
	if runtime.GOOS == "darwin" {
		want = append([]string{"Marginalia"}, want...)
	}
	if !slices.Equal(tops, want) {
		t.Fatalf("menus = %q, want %q", tops, want)
	}

	// Every command is in the menu exactly once, with its shortcut.
	accelerators := map[MenuCommand]string{
		CommandNewNote:       "CmdOrCtrl+N",
		CommandOpenVault:     "CmdOrCtrl+O",
		CommandSettings:      "CmdOrCtrl+,",
		CommandToggleSidebar: `CmdOrCtrl+\`,
		CommandSearch:        "CmdOrCtrl+K",
		CommandRevealVault:   "",
	}
	if runtime.GOOS == "darwin" {
		accelerators[CommandSettings] = "Cmd+,"
	}
	seen := map[MenuCommand]int{}
	keys := map[string]string{}
	for _, it := range allItems(template) {
		if it.Accelerator != "" {
			if other, dup := keys[it.Accelerator]; dup {
				t.Errorf("%q and %q share %s", other, it.Label, it.Accelerator)
			}
			keys[it.Accelerator] = it.Label
		}
		if it.ID == "" {
			continue
		}
		c := MenuCommand(it.ID)
		seen[c]++
		if acc, known := accelerators[c]; !known {
			t.Errorf("unknown command %q", c)
		} else if it.Accelerator != acc {
			t.Errorf("%s: accelerator %q, want %q", c, it.Accelerator, acc)
		}
		if it.Click == nil || it.Label == "" || !strings.ContainsFunc(it.Label, func(r rune) bool { return r > 0x2e80 }) {
			t.Errorf("%s: label %q / click missing or not Chinese", c, it.Label)
		}
	}
	for c := range accelerators {
		if seen[c] != 1 {
			t.Errorf("command %s appears %d times", c, seen[c])
		}
	}
	// Standard items keep their roles, relabelled in Chinese.
	for _, it := range allItems(template) {
		if it.Role != "" && it.Label == "" {
			t.Errorf("role %s has no Chinese label", it.Role)
		}
	}
}

// ------------------------------------------------------------ Native

type fakeShell struct {
	external, opened, shown []string
}

func (s *fakeShell) OpenExternal(url string) error { s.external = append(s.external, url); return nil }
func (s *fakeShell) OpenPath(path string) error    { s.opened = append(s.opened, path); return nil }
func (s *fakeShell) ShowItemInFolder(path string)  { s.shown = append(s.shown, path) }

// newTestNative returns a Native over a server whose vault holds A.md and
// sub/B.md (noVault: no vault), with a fake shell.
func newTestNative(t *testing.T, noVault bool) (*Native, *fakeShell, *server.Server) {
	t.Helper()
	root := t.TempDir()
	s := config.Defaults(config.ModeDesktop)
	s.DataDir = filepath.Join(root, "data")
	s.Git.Autocommit = false
	s.API.Host, s.API.Port = "127.0.0.1", 0
	if !noVault {
		s.NotesDir = filepath.Join(root, "notes")
		for _, rel := range []string{"A.md", "sub/B.md"} {
			p := filepath.Join(s.NotesDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("# "+rel), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	core, err := server.NewCore(s, server.CoreOptions{Mode: config.ModeDesktop, Version: "1.2.3", Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(core, server.Options{Static: fstest.MapFS{"index.html": {Data: []byte("ok")}}, Logger: quiet})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	sh := &fakeShell{}
	n := newNative(srv, "1.2.3")
	n.shell = sh
	return n, sh, srv
}

func TestNativeReveal(t *testing.T) {
	n, sh, srv := newTestNative(t, false)
	root := srv.Core().Vault().Root()
	if err := n.Reveal(""); err != nil || !slices.Equal(sh.opened, []string{root}) {
		t.Fatalf("Reveal(\"\") = %v, opened %q", err, sh.opened)
	}
	if err := n.Reveal("sub/B.md"); err != nil || !slices.Equal(sh.shown, []string{filepath.Join(root, "sub", "B.md")}) {
		t.Fatalf("Reveal(sub/B.md) = %v, shown %q", err, sh.shown)
	}
	for _, bad := range []string{"../outside", ".git", "missing.md", "/etc/passwd"} {
		if err := n.Reveal(bad); err == nil {
			t.Errorf("Reveal(%q) must fail", bad)
		}
	}
	if len(sh.shown) != 1 {
		t.Fatalf("shown %q", sh.shown)
	}

	n, _, _ = newTestNative(t, true)
	if err := n.Reveal(""); err == nil {
		t.Fatal("Reveal without a vault must fail")
	}
}

func TestNativeRevealDataDirAndOpenExternal(t *testing.T) {
	n, sh, srv := newTestNative(t, false)
	if err := n.RevealDataDir(); err != nil || !slices.Equal(sh.opened, []string{srv.Core().Settings().DataDir}) {
		t.Fatalf("RevealDataDir = %v, opened %q", err, sh.opened)
	}
	if err := n.OpenExternal("https://github.com/egoist/mygo"); err != nil {
		t.Fatal(err)
	}
	if err := n.OpenExternal("file:///etc/passwd"); err == nil {
		t.Fatal("OpenExternal(file:) must fail")
	}
	if !slices.Equal(sh.external, []string{"https://github.com/egoist/mygo"}) {
		t.Fatalf("external %q", sh.external)
	}
}

func TestNativeInfo(t *testing.T) {
	n, _, srv := newTestNative(t, false)
	info := n.Info()
	if info.Version != "1.2.3" || info.OS != runtime.GOOS || info.Arch != runtime.GOARCH ||
		info.DataDir != srv.Core().Settings().DataDir || info.APIURL != "" {
		t.Fatalf("Info = %+v", info)
	}
	if err := srv.StartAPI(); err != nil {
		t.Fatal(err)
	}
	if info := n.Info(); info.APIURL == "" || info.APIURL != srv.APIStatus().URL {
		t.Fatalf("Info with the API running = %+v, status %+v", info, srv.APIStatus())
	}
	got, err := json.Marshal(n.Info())
	if err != nil || !strings.Contains(string(got), `"apiURL":"http://127.0.0.1:`) || !strings.Contains(string(got), `"dataDir":`) {
		t.Fatalf("json = %s, %v", got, err)
	}
}

func TestNativePickFolder(t *testing.T) {
	n, _, srv := newTestNative(t, false)
	var asked mygo.OpenDialogOptions
	answer := []string{"/Users/me/Notes"}
	n.pickFolders = func(o mygo.OpenDialogOptions) ([]string, error) { asked = o; return answer, nil }
	got, err := n.PickFolder(context.Background(), "选择笔记库文件夹")
	if err != nil || got != "/Users/me/Notes" {
		t.Fatalf("PickFolder = %q, %v", got, err)
	}
	if !asked.Directory || asked.Multiple || asked.Title != "选择笔记库文件夹" ||
		asked.DefaultPath != filepath.Dir(srv.Core().Settings().NotesDir) {
		t.Fatalf("dialog options = %+v", asked)
	}
	answer = nil // cancelled
	if got, err := n.PickFolder(context.Background(), "x"); err != nil || got != "" {
		t.Fatalf("cancelled PickFolder = %q, %v", got, err)
	}
}

// The window waits for the page's Flushed answer to its FlushEvent, and an
// answer left over from an earlier request does not count.
func TestNativeAwaitFlush(t *testing.T) {
	n, _, _ := newTestNative(t, false)
	n.Flushed() // stale: no request was waiting
	n.Flushed() // a second stale answer must not block
	tick := func() error { time.Sleep(time.Millisecond); return nil }
	asked := 0
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := n.awaitFlush(ctx, func() error { asked++; return nil }, tick); !errors.Is(err, context.DeadlineExceeded) || asked != 1 {
		t.Fatalf("without an answer: %v (asked %d)", err, asked)
	}
	err := n.awaitFlush(context.Background(), func() error {
		go n.Flushed() // the page answers
		return nil
	}, tick)
	if err != nil {
		t.Fatalf("with an answer: %v", err)
	}
	failed := errors.New("no window")
	if err := n.awaitFlush(context.Background(), func() error { return failed }, tick); !errors.Is(err, failed) {
		t.Fatalf("emit failure: %v", err)
	}
	gone := errors.New("page gone")
	if err := n.awaitFlush(context.Background(), func() error { return nil }, func() error { return gone }); !errors.Is(err, gone) {
		t.Fatalf("tick failure: %v", err)
	}
}

// The main window draws Magpie's 46 px top bar: hidden title bar without the
// inset style's toolbar, controls placed to make the title bar 46 px tall.
func TestMainWindowOptions(t *testing.T) {
	o := mainWindowOptions("Marginalia")
	if o.TitleBarStyle != mygo.TitleBarHidden || o.TitleBarHeight != 46 ||
		o.TrafficLightPosition == nil || *o.TrafficLightPosition != (mygo.Point{X: 16, Y: 16}) {
		t.Fatalf("title bar options = %+v", o)
	}
	if o.Title != "Marginalia" || o.URL != "/" || o.Width != windowWidth || o.MinWidth != windowMinWidth ||
		o.StateKey != windowStateKey || o.BackgroundColor != backgroundColor {
		t.Fatalf("window options = %+v", o)
	}
}
