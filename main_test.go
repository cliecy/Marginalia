package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		args     []string
		name     string
		wantRest []string
	}{
		{nil, "desktop", nil},
		{[]string{"desktop"}, "desktop", []string{}},
		{[]string{"serve", "--addr", "127.0.0.1:1"}, "serve", []string{"--addr", "127.0.0.1:1"}},
		{[]string{"mcp"}, "mcp", []string{}},
		{[]string{"version"}, "version", []string{}},
		{[]string{"--version"}, "version", []string{}},
		{[]string{"-h"}, "help", []string{}},
		// What the system starts the app with: deep links, files, flags.
		{[]string{"marginalia://open?path=A.md"}, "desktop", []string{"marginalia://open?path=A.md"}},
		{[]string{"-psn_0_12345"}, "desktop", []string{"-psn_0_12345"}},
		{[]string{"/Users/me/notes/A.md"}, "desktop", []string{"/Users/me/notes/A.md"}},
		{[]string{"notes.md"}, "desktop", []string{"notes.md"}},
		// A mistyped command is not taken for a launch of the app.
		{[]string{"sevre"}, "", []string{"sevre"}},
	}
	for _, tt := range tests {
		name, rest := parseCommand(tt.args)
		if name != tt.name || !slices.Equal(rest, tt.wantRest) {
			t.Errorf("parseCommand(%q) = %q, %q; want %q, %q", tt.args, name, rest, tt.name, tt.wantRest)
		}
	}
}

func TestAppInfoComesFromMygoJSON(t *testing.T) {
	var file struct{ Name, Version, Identifier string }
	if err := json.Unmarshal(mygoJSON, &file); err != nil {
		t.Fatal(err)
	}
	info := readAppInfo()
	if info.Name != "Marginalia" || info.Version == "" || info.Version != file.Version || file.Identifier != "com.cliecy.marginalia" {
		t.Fatalf("app info = %+v, mygo.json = %+v", info, file)
	}
	old := version
	version = "9.9.9"
	defer func() { version = old }()
	if got := readAppInfo().Version; got != "9.9.9" {
		t.Fatalf("-X main.version: got %q", got)
	}
}

func TestVersionAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("version: exit %d, %s", code, errOut.String())
	}
	want := fmt.Sprintf("Marginalia %s (%s/%s)\n", readAppInfo().Version, runtime.GOOS, runtime.GOARCH)
	if out.String() != want {
		t.Fatalf("version printed %q, want %q", out.String(), want)
	}
	out.Reset()
	if code := run([]string{"help"}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "marginalia serve") {
		t.Fatalf("help: exit %d, %q", code, out.String())
	}
	errOut.Reset()
	if code := run([]string{"sevre"}, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown command "sevre"`) {
		t.Fatalf("unknown command: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"serve", "extra"}, nil, &out, &errOut); code != 2 {
		t.Fatalf("serve extra: exit %d, %q", code, errOut.String())
	}
	if code := run([]string{"serve", "--bogus"}, nil, &out, &errOut); code != 1 {
		t.Fatalf("serve --bogus: exit %d, %q", code, errOut.String())
	}
	if code := run([]string{"mcp", "extra"}, nil, &out, &errOut); code != 2 {
		t.Fatalf("mcp extra: exit %d, %q", code, errOut.String())
	}
}

// closedPort returns a TCP port on loopback that nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// TestMCPInProcess runs `marginalia mcp` without a running app: it serves
// the vault of settings.json in-process, protocol on stdout only.
func TestMCPInProcess(t *testing.T) {
	root := t.TempDir()
	data, notes := filepath.Join(root, "data"), filepath.Join(root, "notes")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "Hello.md"), []byte("# Hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf(`{"notes_dir": %q, "api": {"enabled": true, "host": "127.0.0.1", "port": %d}, "git": {"autocommit": false}}`, notes, closedPort(t))
	if err := os.WriteFile(filepath.Join(data, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOTES_DATA_DIR", data)
	for _, name := range []string{"NOTES_DIR", "NOTES_GIT_AUTOCOMMIT"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_note","arguments":{"path":"Hello.md"}}}`,
	}, "\n") + "\n"
	var out, errOut bytes.Buffer
	if code := run([]string{"mcp"}, strings.NewReader(in), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 replies on stdout, got %d:\n%s\nstderr:\n%s", len(lines), out.String(), errOut.String())
	}
	var replies [3]struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &replies[i]); err != nil || replies[i].ID != i+1 || replies[i].Error != nil {
			t.Fatalf("reply %d = %s (%v)", i+1, line, err)
		}
	}
	var init struct {
		ServerInfo struct{ Name, Version string } `json:"serverInfo"`
	}
	if err := json.Unmarshal(replies[0].Result, &init); err != nil || init.ServerInfo.Name != "marginalia" || init.ServerInfo.Version != readAppInfo().Version {
		t.Fatalf("initialize = %s", replies[0].Result)
	}
	if !strings.Contains(string(replies[1].Result), `"read_note"`) || !strings.Contains(string(replies[2].Result), "world") {
		t.Fatalf("tools/list = %s\nread_note = %s", replies[1].Result, replies[2].Result)
	}
	if !strings.Contains(errOut.String(), "serving MCP in-process") {
		t.Fatalf("stderr = %s", errOut.String())
	}
}
