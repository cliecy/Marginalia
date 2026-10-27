package magpie

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGatewayServiceDocument(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apis":["/v1/chat/completions"],"models":50,"name":"magpie","version":"0.1.1606"}`))
	}))
	defer gw.Close()

	got := (&Client{GatewayURL: gw.URL}).Gateway(context.Background())
	if !got.Running || got.Version != "0.1.1606" || got.Name != "magpie" || got.Models == nil || *got.Models != 50 || got.URL != gw.URL {
		t.Fatalf("gateway = %+v", got)
	}
}

func TestGatewayFallsBackToModels(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.NotFound(w, r)
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer magpie" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"group/default"},{"id":"codex/gpt"}]}`))
		}
	}))
	defer gw.Close()

	got := (&Client{GatewayURL: gw.URL + "/"}).Gateway(context.Background())
	if !got.Running || got.Version != "" || got.Models == nil || *got.Models != 2 {
		t.Fatalf("gateway = %+v", got)
	}
}

func TestGatewayOtherServiceIsNotMagpie(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"something-else","version":"1","data":[]}`))
	}))
	defer gw.Close()
	if got := (&Client{GatewayURL: gw.URL}).Gateway(context.Background()); got.Running {
		t.Fatalf("gateway = %+v", got)
	}
}

func TestGatewayDownOrSlow(t *testing.T) {
	ln := httptest.NewServer(http.NotFoundHandler())
	url := ln.URL
	ln.Close() // nothing listens there any more
	if got := (&Client{GatewayURL: url}).Gateway(context.Background()); got.Running || got.Models != nil {
		t.Fatalf("closed gateway = %+v", got)
	}

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	start := time.Now()
	got := (&Client{GatewayURL: slow.URL, DetectTimeout: 150 * time.Millisecond}).Gateway(context.Background())
	if got.Running {
		t.Fatalf("slow gateway = %+v", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("detection took %v", elapsed)
	}
}

func TestIsGatewayURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1:3425/v1":  true,
		"http://localhost:3425":     true,
		"http://[::1]:3425/v1/":     true,
		"http://127.0.0.1:8317/v1":  false,
		"https://127.0.0.1:3425/v1": false,
		"http://example.com:3425":   false,
		"":                          false,
	} {
		if got := IsGatewayURL(raw); got != want {
			t.Errorf("IsGatewayURL(%q) = %v", raw, got)
		}
	}
}

// fakeCLI writes a shell script named magpie into a new folder and returns
// the folder. It records its arguments in args.txt and answers like Magpie.
func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a shell script")
	}
	dir := t.TempDir()
	body := "#!/bin/sh\necho \"$@\" >> \"" + filepath.Join(dir, "args.txt") + "\"\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "magpie"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readArgs(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFindCLIOnPath(t *testing.T) {
	dir := fakeCLI(t, "exit 0")
	t.Setenv("PATH", dir)
	if got := FindCLI(); got != filepath.Join(dir, "magpie") {
		t.Fatalf("FindCLI() = %q", got)
	}
	if got := (&Client{}).CLI(); got != filepath.Join(dir, "magpie") {
		t.Fatalf("CLI() = %q", got)
	}
}

func TestRegisterAndUnregister(t *testing.T) {
	dir := fakeCLI(t, `case "$1 $2 $3" in
"library mcp add") echo "added $4 to every agent";;
"library mcp rm") echo "removed $4" >&2;;
*) echo "unknown"; exit 2;;
esac`)
	t.Setenv("PATH", dir)
	c := &Client{}

	res, err := c.RegisterMCP(context.Background(), "marginalia", "/Applications/Mark Down.app/bin", "mcp")
	if err != nil || !res.OK || res.Output != "added marginalia to every agent" {
		t.Fatalf("register = %+v, %v", res, err)
	}
	wantCmd := filepath.Join(dir, "magpie") + " library mcp add marginalia '/Applications/Mark Down.app/bin' mcp"
	if res.Command != wantCmd {
		t.Fatalf("command = %q, want %q", res.Command, wantCmd)
	}
	res, err = c.UnregisterMCP(context.Background(), "marginalia")
	if err != nil || !res.OK || res.Output != "removed marginalia" {
		t.Fatalf("unregister = %+v, %v", res, err)
	}
	if got := readArgs(t, dir); got != "library mcp add marginalia /Applications/Mark Down.app/bin mcp\nlibrary mcp rm marginalia\n" {
		t.Fatalf("args = %q", got)
	}
}

func TestCommandFailureAndTimeout(t *testing.T) {
	dir := fakeCLI(t, `if [ "$1" = library ] && [ -z "$2" ]; then exec sleep 5; fi
echo "magpie isn't running" >&2; exit 1`)
	c := &Client{CLIPath: filepath.Join(dir, "magpie"), StatusTimeout: 200 * time.Millisecond}

	res, err := c.RegisterMCP(context.Background(), "marginalia", "http://127.0.0.1:8190/mcp?token=x")
	if err != nil || res.OK || !strings.Contains(res.Output, "isn't running") {
		t.Fatalf("failed register = %+v, %v", res, err)
	}

	start := time.Now()
	res, err = c.Library(context.Background())
	if err != nil || res.OK || !strings.Contains(res.Output, "超时") {
		t.Fatalf("slow library = %+v, %v", res, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
	if got := c.MCPRegistered(context.Background(), "marginalia"); got != nil {
		t.Fatalf("MCPRegistered = %v, want nil", *got)
	}
}

func TestMCPRegistered(t *testing.T) {
	for output, want := range map[string]bool{
		"Claude Code\n  mcp: marginalia, web-search\nCodex\n  mcp: marginalia": true,
		"Claude Code\n  mcp: web-search, marginalia-old":                       false,
		"the library has no MCP servers":                                       false,
	} {
		dir := fakeCLI(t, "cat <<'EOF'\n"+output+"\nEOF")
		c := &Client{CLIPath: filepath.Join(dir, "magpie")}
		got := c.MCPRegistered(context.Background(), "marginalia")
		if got == nil || *got != want {
			t.Errorf("MCPRegistered with %q = %v, want %v", output, got, want)
		}
	}
}

func TestNoCLI(t *testing.T) {
	c := &Client{CLIPath: filepath.Join(t.TempDir(), "missing")}
	if c.CLI() != "" {
		t.Fatal("a missing CLIPath must not fall back to FindCLI")
	}
	res, err := c.RegisterMCP(context.Background(), "marginalia", "x")
	if !errors.Is(err, ErrNoCLI) || res.OK || !strings.HasPrefix(res.Command, "magpie library mcp add") {
		t.Fatalf("register without CLI = %+v, %v", res, err)
	}
	if got := c.MCPRegistered(context.Background(), "marginalia"); got != nil {
		t.Fatal("MCPRegistered without CLI must be nil")
	}
}

func TestQuoteCommand(t *testing.T) {
	got := QuoteCommand([]string{"magpie", "library", "mcp", "add", "n", "http://127.0.0.1:8190/mcp?token=a-b_c", "it's"})
	want := `magpie library mcp add n 'http://127.0.0.1:8190/mcp?token=a-b_c' 'it'\''s'`
	if got != want {
		t.Fatalf("QuoteCommand = %q, want %q", got, want)
	}
}
