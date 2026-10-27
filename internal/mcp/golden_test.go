package mcp

import (
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

// mcpGolden is testdata/golden_mcp.json, a recorded
// JSON-RPC session on the shared fixture vault, with every reply and the
// vault's final files.
type mcpGolden struct {
	Version string `json:"version"`
	Steps   []struct {
		Name    string          `json:"name"`
		Request json.RawMessage `json:"request"`
		Reply   json.RawMessage `json:"reply"`
	} `json:"steps"`
	Files map[string]string `json:"files"`
}

// pythonQualnames are the prefixes CPython puts before the nested tool
// functions' names in TypeError messages; the port names the tool only.
var pythonQualnames = strings.NewReplacer(
	"app.mcp.MCPServer._register.<locals>.", "",
	"MCPServer._register.<locals>.", "",
)

// canonical re-encodes JSON through the ordered Python value model, so that
// two replies compare equal exactly when they have the same members in the
// same order with the same values.
func canonical(t *testing.T, data []byte) string {
	t.Helper()
	v, err := pyyaml.FromJSON(data)
	if err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}
	out, err := pyyaml.ToJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestGoldenSession(t *testing.T) {
	var g mcpGolden
	vaulttest.Golden(t, filepath.Join("testdata", "golden_mcp.json"), &g)
	if len(g.Steps) == 0 {
		t.Fatal("golden file has no steps")
	}
	root := vaulttest.Fixture(t)
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	git := &fakeGit{}
	srv := New(v, git, Options{Version: g.Version})

	for _, step := range g.Steps {
		reply, ok := srv.Handle(step.Request)
		if string(step.Reply) == "null" {
			if ok {
				t.Errorf("%s: got reply %s, want none", step.Name, reply)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: got no reply, want %s", step.Name, step.Reply)
			continue
		}
		want := canonical(t, []byte(pythonQualnames.Replace(string(step.Reply))))
		if got := canonical(t, reply); got != want {
			t.Errorf("%s:\n got %s\nwant %s", step.Name, got, want)
		}
	}

	// The vault ends up byte-for-byte where the recording left it.
	got := map[string]string{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			got[filepath.ToSlash(rel)] = base64.StdEncoding.EncodeToString(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range g.Files {
		if got[rel] != want {
			gotText, _ := base64.StdEncoding.DecodeString(got[rel])
			wantText, _ := base64.StdEncoding.DecodeString(want)
			t.Errorf("file %s:\n got %q\nwant %q", rel, gotText, wantText)
		}
	}
	for rel := range got {
		if _, ok := g.Files[rel]; !ok {
			t.Errorf("unexpected file %s", rel)
		}
	}

	// Every successful change was committed immediately with "mcp: …";
	// move and delete flushed pending commits first, even when they failed.
	wantMessages := []string{
		"mcp: write AGENTS.md",
		"mcp: write A.md",
		"mcp: write Knowledge/New.md",
		"mcp: write Knowledge/New.md",
		"mcp: append Knowledge/New.md",
		"mcp: append Inbox/Idea.md",
		"mcp: append crlf.md",
		"mcp: move sub/B.md -> topics/Bee.md",
		"mcp: move nested -> Archive/nested",
		"mcp: delete Inbox/Idea.md",
		"mcp: delete Inbox",
		"mcp: distilled Chats/2025-01-01 1000 hello.md",
		"mcp: distilled Chats/2025-01-01 1000 hello.md",
		"mcp: distilled Chats/no-meta.md",
	}
	records, flushes := git.snapshot()
	var messages []string
	for _, r := range records {
		messages = append(messages, r.message)
		if !r.immediate {
			t.Errorf("%s: not committed immediately", r.message)
		}
	}
	if !reflect.DeepEqual(messages, wantMessages) {
		t.Errorf("commit messages:\n got %q\nwant %q", messages, wantMessages)
	}
	if flushes != 10 {
		t.Errorf("flushes = %d, want 10 (5 moves + 5 deletes)", flushes)
	}
	for _, r := range records {
		if r.message == "mcp: move sub/B.md -> topics/Bee.md" {
			if len(r.paths) != 5 || r.paths[0] != "sub/B.md" || r.paths[1] != "topics/Bee.md" {
				t.Errorf("move recorded %q, want source, destination and the 3 relinked notes", r.paths)
			}
		}
	}
}
