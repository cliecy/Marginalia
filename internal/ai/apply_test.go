package ai

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

func applyVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(vaulttest.Strings(t, map[string]string{
		"a.md":        "# A\n",
		"b.md":        "# B\n",
		"dir/c.md":    "see [[a]]\n",
		"folder/x.md": "x\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func etagOf(t *testing.T, v *vault.Vault, rel string) *string {
	t.Helper()
	_, data, err := v.ReadBytes(rel)
	if err != nil {
		t.Fatal(err)
	}
	etag := vault.ETag(data)
	return &etag
}

func readFile(t *testing.T, v *vault.Vault, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(v.Root(), filepath.FromSlash(rel)))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(data)
}

func TestApplyChangesetRefusesStaleProposals(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(v *vault.Vault)
		failOp int // index of the operation that is stale
		want   string
	}{
		{"modified", func(v *vault.Vault) { os.WriteFile(filepath.Join(v.Root(), "b.md"), []byte("# B edited\n"), 0o644) }, 1,
			"b.md 在生成后被修改过，请重新生成"},
		{"deleted", func(v *vault.Vault) { os.Remove(filepath.Join(v.Root(), "b.md")) }, 1,
			"b.md 已不存在，请重新生成"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := applyVault(t)
			ops := []Proposal{
				{Op: OpCreate, Path: "new.md", After: "new\n"},
				{Op: OpUpdate, Path: "b.md", After: "# B2\n", BaseETag: etagOf(t, v, "b.md")},
				{Op: OpDelete, Path: "a.md", BaseETag: etagOf(t, v, "a.md")},
			}
			c.change(v)
			touched, err := ApplyChangeset(v, ops)
			if err == nil || err.Error() != c.want || vault.StatusOf(err) != http.StatusBadRequest {
				t.Fatalf("err = %v (status %d), want %q (400)", err, vault.StatusOf(err), c.want)
			}
			if !errors.Is(err, vault.ErrInvalid) || touched != nil {
				t.Errorf("errors.Is(ErrInvalid) = %v, touched %q", errors.Is(err, vault.ErrInvalid), touched)
			}
			// Nothing was written, not even the operations before the stale one.
			if got := readFile(t, v, "new.md"); !strings.HasPrefix(got, "<") {
				t.Errorf("new.md was created: %q", got)
			}
			if got := readFile(t, v, "a.md"); got != "# A\n" {
				t.Errorf("a.md = %q", got)
			}
		})
	}
}

func TestApplyChangesetChecksMovesAndIgnoresEmptyETags(t *testing.T) {
	v := applyVault(t)
	stale := "0123456789abcdef0123"
	_, err := ApplyChangeset(v, []Proposal{{Op: OpMove, Path: "a.md", To: "z.md", BaseETag: &stale}})
	if err == nil || err.Error() != "a.md 在生成后被修改过，请重新生成" {
		t.Errorf("move with stale etag: %v", err)
	}
	empty := ""
	touched, err := ApplyChangeset(v, []Proposal{
		{Op: OpMove, Path: "folder", To: "Archive/folder"}, // folders carry no etag
		{Op: OpDelete, Path: "b.md", BaseETag: &empty},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"folder", "Archive/folder", "b.md"}; !reflect.DeepEqual(touched, want) {
		t.Errorf("touched = %q, want %q", touched, want)
	}
	if got := readFile(t, v, "Archive/folder/x.md"); got != "x\n" {
		t.Errorf("moved folder content = %q", got)
	}
}

func TestApplyChangesetMoveRewritesLinks(t *testing.T) {
	v := applyVault(t)
	touched, err := ApplyChangeset(v, []Proposal{
		{Op: OpMove, Path: "a.md", To: "notes/renamed.md", BaseETag: etagOf(t, v, "a.md")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.md", "notes/renamed.md", "dir/c.md"}; !reflect.DeepEqual(touched, want) {
		t.Errorf("touched = %q, want %q", touched, want)
	}
	if got := readFile(t, v, "dir/c.md"); got != "see [[renamed]]\n" {
		t.Errorf("dir/c.md = %q", got)
	}
}

func TestApplyChangesetPartialFailureReportsTouched(t *testing.T) {
	v := applyVault(t)
	touched, err := ApplyChangeset(v, []Proposal{
		{Op: OpCreate, Path: "fresh.md", After: "fresh\n"},
		{Op: "frobnicate", Path: "a.md"},
		{Op: OpDelete, Path: "missing.md"}, // no etag: not validated up front
		{Op: OpDelete, Path: "b.md"},
	})
	if !errors.Is(err, vault.ErrNotFound) || err.Error() != "file not found" {
		t.Errorf("err = %v", err)
	}
	if !reflect.DeepEqual(touched, []string{"fresh.md"}) {
		t.Errorf("touched = %q", touched)
	}
	if readFile(t, v, "fresh.md") != "fresh\n" || readFile(t, v, "b.md") != "# B\n" {
		t.Error("operations before the failure must stay applied, later ones must not run")
	}
}

func TestApplyChangesetUpdateUsesETagAndCreateOverwrites(t *testing.T) {
	v := applyVault(t)
	touched, err := ApplyChangeset(v, []Proposal{
		{Op: OpUpdate, Path: "a.md", After: "# A2\n", BaseETag: etagOf(t, v, "a.md")},
		{Op: OpCreate, Path: "b.md", After: "# B overwritten\n"}, // Python semantics
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(touched, []string{"a.md", "b.md"}) || readFile(t, v, "a.md") != "# A2\n" || readFile(t, v, "b.md") != "# B overwritten\n" {
		t.Errorf("touched %q, a %q, b %q", touched, readFile(t, v, "a.md"), readFile(t, v, "b.md"))
	}
	// Writing a non-note path fails like PUT /api/note.
	if _, err := ApplyChangeset(v, []Proposal{{Op: OpCreate, Path: "x.png", After: "x"}}); err == nil || err.Error() != "only text files can be edited" {
		t.Errorf("non-text create: %v", err)
	}
}

func TestWrittenPathsAndCommitMessage(t *testing.T) {
	ops := []Proposal{
		{Op: OpCreate, Path: "a.md"}, {Op: OpUpdate, Path: "b.md"}, {Op: OpMove, Path: "c.md", To: "d/c.md"},
		{Op: OpDelete, Path: "e.md"}, {Op: "other", Path: "f.md"},
	}
	if got := WrittenPaths(ops); !reflect.DeepEqual(got, []string{"a.md", "b.md", "d/c.md", "f.md"}) {
		t.Errorf("WrittenPaths = %q", got)
	}
	if got := WrittenPaths(nil); got == nil || len(got) != 0 {
		t.Errorf("WrittenPaths(nil) = %#v", got)
	}
	if got := CommitMessage(""); got != "ai: organize notes" {
		t.Errorf("CommitMessage(\"\") = %q", got)
	}
	long := strings.Repeat("整理", 100)
	if got := CommitMessage(long); got != "ai: "+strings.Repeat("整理", 58) {
		t.Errorf("CommitMessage(long) = %q (%d runes)", got, len([]rune(got)))
	}
}
