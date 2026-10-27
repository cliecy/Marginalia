package vault_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

// pythonFixture is the standard test vault.
func pythonFixture(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	root := vaulttest.Write(t, map[string][]byte{
		"A.md":          []byte("---\ntags: [ml]\nsummary: about A\n---\n# A\nsee [[B]] and ![[_assets/x.png]]\n"),
		"sub/B.md":      []byte("# B\nback to [[A|alias]]\n"),
		"_assets/x.png": []byte("\x89PNG"),
		".git/HEAD":     []byte("ref: refs/heads/master\n"),
		".secret.md":    []byte("hidden"),
	})
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return v, v.Root()
}

func ptr(s string) *string { return &s }

// test_path_safety
func TestPathSafety(t *testing.T) {
	v, root := pythonFixture(t)
	for _, bad := range []string{"../x.md", ".git/config", "a/../../x", "sub/.hidden.md", "", "a\x00b", strings.Repeat("a", 1025)} {
		if _, err := v.Resolve(bad); !errors.Is(err, vault.ErrInvalid) {
			t.Errorf("Resolve(%q) error = %v, want invalid", bad, err)
		}
	}
	got, err := v.Resolve("/A.md")
	if err != nil || got != filepath.Join(root, "A.md") {
		t.Errorf("Resolve(/A.md) = %q, %v", got, err)
	}
	if err := os.Symlink("/etc", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Resolve("link/passwd"); err == nil || err.Error() != "path escapes the vault" {
		t.Errorf("symlink escape: %v", err)
	}
}

func TestSymlinkEdgeCases(t *testing.T) {
	v, root := pythonFixture(t)
	outside := t.TempDir()
	// A dangling link whose target lies outside must not be followed lexically.
	if err := os.Symlink(filepath.Join(outside, "later"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Resolve("dangling/x.md"); err == nil {
		t.Error("dangling outside link accepted")
	}
	if _, err := v.WriteNote("dangling/x.md", "x", nil); err == nil {
		t.Error("write through dangling outside link accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "later", "x.md")); err == nil {
		t.Error("file created outside the vault")
	}
	// A link inside the vault is followed and reported under its target.
	if err := os.Symlink("sub/B.md", filepath.Join(root, "alias.md")); err != nil {
		t.Fatal(err)
	}
	note, err := v.ReadNote("alias.md")
	if err != nil || note.Path != "sub/B.md" {
		t.Errorf("ReadNote(alias.md) = %+v, %v", note, err)
	}
	// Link loops do not hang.
	if err := os.Symlink("loop2", filepath.Join(root, "loop1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop1", filepath.Join(root, "loop2")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ReadNote("loop1/x.md"); err == nil {
		t.Error("loop resolved")
	}
	// A link to the root resolves to the root, which cannot be deleted.
	if err := os.Symlink(".", filepath.Join(root, "self")); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete("self"); err == nil || err.Error() != "cannot delete the vault root" {
		t.Errorf("Delete(self) = %v", err)
	}
	// Symlinks never appear in the tree; listing keeps links to vault files
	// only.
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "evil.md")); err != nil {
		t.Fatal(err)
	}
	files := v.Files(true)
	if !slices.Contains(files, "alias.md") || slices.Contains(files, "evil.md") {
		t.Errorf("Files(true) = %q", files)
	}
	for _, n := range v.Tree() {
		if n.Name == "alias.md" || n.Name == "evil.md" || n.Name == "self" {
			t.Errorf("tree shows symlink %s", n.Name)
		}
	}
	if hits := v.Search("TOPSECRET", 50); len(hits) != 0 {
		t.Errorf("search leaked outside content: %+v", hits)
	}
}

// Symlinks must not lead into hidden entries: the dot-component rule applies
// to the resolved target, not only to the path text the client sends.
func TestSymlinksIntoHiddenEntries(t *testing.T) {
	v, root := pythonFixture(t)
	for name, data := range map[string]string{
		".git/config":             "[remote \"origin\"]\n\turl = https://user:SECRETTOKEN@example.test/repo.git\n",
		".git/hooks/README.md":    "hooks",
		".obsidian/app.json":      "{\"SECRETTOKEN\": true}",
		".obsidian/notes/meta.md": "SECRETTOKEN",
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"Inbox/g":  "../.git",        // a folder link to .git
		"sub/c.md": "../.git/config", // a note link to a hidden file
		"o":        ".obsidian",      // a folder link to .obsidian
		"meta.md":  ".obsidian/notes/meta.md",
	} {
		if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	invalid := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, vault.ErrInvalid) {
			t.Errorf("%s: error = %v, want invalid", what, err)
		}
	}
	for _, p := range []string{"Inbox/g", "Inbox/g/config", "Inbox/g/hooks/README.md", "Inbox/g/hooks/pre-commit", "sub/c.md", "o/app.json", "o/notes/meta.md", "meta.md"} {
		_, err := v.Resolve(p)
		invalid("Resolve("+p+")", err)
	}
	_, _, err := v.ReadBytes("Inbox/g/config")
	invalid("ReadBytes(Inbox/g/config)", err)
	_, err = v.ReadNote("sub/c.md")
	invalid("ReadNote(sub/c.md)", err)
	_, err = v.ReadText("sub/c.md")
	invalid("ReadText(sub/c.md)", err)
	_, err = v.ReadNote("o/notes/meta.md")
	invalid("ReadNote(o/notes/meta.md)", err)
	_, err = v.WriteNote("Inbox/g/hooks/x.md", "#!/bin/sh\ntouch MARKER\n", nil)
	invalid("WriteNote(Inbox/g/hooks/x.md)", err)
	_, err = v.WriteNote("sub/c.md", "[core]\n\tfsmonitor = touch MARKER\n", nil)
	invalid("WriteNote(sub/c.md)", err)
	_, err = v.Create("Inbox/g/hooks/y.md", "x", false)
	invalid("Create(Inbox/g/hooks/y.md)", err)
	_, err = v.Create("Inbox/g/newdir", "", true)
	invalid("Create(Inbox/g/newdir, folder)", err)
	_, err = v.Move("Inbox/g/config", "config.md", false)
	invalid("Move(Inbox/g/config, config.md)", err)
	_, err = v.Move("A.md", "Inbox/g/hooks/pre-commit", false)
	invalid("Move(A.md, Inbox/g/hooks/pre-commit)", err)
	invalid("Delete(Inbox/g/HEAD)", v.Delete("Inbox/g/HEAD"))
	invalid("Delete(o/app.json)", v.Delete("o/app.json"))
	_, err = v.Upload("Inbox/g/hooks", "pre-commit", []byte("#!/bin/sh\n"))
	invalid("Upload(Inbox/g/hooks)", err)
	if _, err := v.Backlinks("sub/c.md"); !errors.Is(err, vault.ErrInvalid) {
		t.Errorf("Backlinks(sub/c.md) = %v, want invalid", err)
	}

	// Nothing under .git or .obsidian changed and nothing was created there.
	if data, err := os.ReadFile(filepath.Join(root, ".git", "config")); err != nil || !strings.Contains(string(data), "SECRETTOKEN") {
		t.Errorf(".git/config = %q, %v", data, err)
	}
	for _, p := range []string{".git/hooks/x.md", ".git/hooks/y.md", ".git/hooks/pre-commit", ".git/newdir", "config.md"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			t.Errorf("%s was created", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "HEAD")); err != nil {
		t.Errorf(".git/HEAD was removed: %v", err)
	}

	// Links into hidden entries never reach listings, the index or search.
	for _, notesOnly := range []bool{true, false} {
		for _, f := range v.Files(notesOnly) {
			if f == "sub/c.md" || f == "meta.md" || strings.HasPrefix(f, "Inbox/g") || strings.HasPrefix(f, "o/") {
				t.Errorf("Files(%v) lists %q", notesOnly, f)
			}
		}
	}
	if ix := v.Index(); ix.ResolveLink("c", "A.md") != "" || ix.ResolveLink("meta", "A.md") != "" {
		t.Errorf("index resolves links to hidden targets: c=%q meta=%q", ix.ResolveLink("c", "A.md"), ix.ResolveLink("meta", "A.md"))
	}
	if hits := v.Search("SECRETTOKEN", 50); len(hits) != 0 {
		t.Errorf("search leaked hidden content: %+v", hits)
	}

	// Ordinary links inside the vault keep working.
	if err := os.Symlink("../sub", filepath.Join(root, "Inbox", "s")); err != nil {
		t.Fatal(err)
	}
	if note, err := v.ReadNote("Inbox/s/B.md"); err != nil || note.Path != "sub/B.md" {
		t.Errorf("ReadNote(Inbox/s/B.md) = %+v, %v", note, err)
	}
}

// test_tree_hides_dotfiles
func TestTreeHidesDotfiles(t *testing.T) {
	v, _ := pythonFixture(t)
	var names []string
	for _, n := range v.Tree() {
		names = append(names, n.Name)
	}
	if slices.Contains(names, ".git") || slices.Contains(names, ".secret.md") {
		t.Errorf("hidden entries listed: %q", names)
	}
	if len(names) < 2 || names[0] != "_assets" || names[1] != "sub" {
		t.Errorf("tree order = %q, want [_assets sub …]", names)
	}
}

// test_save_conflict
func TestSaveConflict(t *testing.T) {
	v, _ := pythonFixture(t)
	note, err := v.ReadNote("A.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("A.md", "new", &note.ETag); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err = v.WriteNote("A.md", "x", &note.ETag)
	if vault.StatusOf(err) != 409 || err.Error() != "the note changed on disk since it was opened" {
		t.Errorf("stale save = %v", err)
	}
}

func TestWriteNoteETagSemantics(t *testing.T) {
	v, root := pythonFixture(t)
	if _, err := v.WriteNote("A.md", "overwrite", nil); err != nil {
		t.Errorf("nil etag must overwrite: %v", err)
	}
	if _, err := v.WriteNote("A.md", "x", ptr("")); !errors.Is(err, vault.ErrConflict) {
		t.Errorf("empty etag on existing file = %v, want conflict", err)
	}
	if _, err := v.WriteNote("new.md", "x", ptr("")); err != nil {
		t.Errorf("empty etag on new file = %v", err)
	}
	_, err := v.WriteNote("gone.md", "x", ptr("abc"))
	if err == nil || err.Error() != "the note was deleted or moved since it was opened" {
		t.Errorf("etag on missing file = %v", err)
	}
	if _, err := v.WriteNote("pic.png", "x", nil); err == nil || err.Error() != "only text files can be edited" {
		t.Errorf("binary write = %v", err)
	}
	if _, err := v.WriteNote("sub", "x", nil); err == nil {
		t.Error("writing a folder accepted")
	}
	res, err := v.WriteNote("deep/new/dir/n.md", "hello", nil)
	if err != nil || res.Path != "deep/new/dir/n.md" || res.ETag != vault.ETag([]byte("hello")) {
		t.Errorf("nested write = %+v, %v", res, err)
	}
	// Permissions survive a rewrite and no temp files are left behind.
	p := filepath.Join(root, "A.md")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteNote("A.md", "again", nil); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}

func TestReadNote(t *testing.T) {
	v, _ := pythonFixture(t)
	if _, err := v.ReadNote("_assets/x.png"); err == nil || err.Error() != "not a text file" {
		t.Errorf("binary read = %v", err)
	}
	if _, err := v.ReadNote("missing.md"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("missing read = %v", err)
	}
	if _, err := v.ReadNote("sub"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("folder read = %v", err)
	}
	n, err := v.ReadNote("sub/B.md")
	if err != nil || n.Content != "# B\nback to [[A|alias]]\n" || len(n.ETag) != 20 || n.Mtime == 0 {
		t.Errorf("ReadNote = %+v, %v", n, err)
	}
}

func TestCreateAndDelete(t *testing.T) {
	v, root := pythonFixture(t)
	if _, err := v.Create("A.md", "", false); err == nil || err.Error() != "a file or folder with that name already exists" {
		t.Errorf("create existing = %v", err)
	}
	if _, err := v.Create("x.png", "", false); err == nil || err.Error() != "new notes must end in .md" {
		t.Errorf("create binary = %v", err)
	}
	res, err := v.Create("folder/inner", "", true)
	if err != nil || res.Path != "folder/inner" || res.ETag != "" {
		t.Errorf("create folder = %+v, %v", res, err)
	}
	if _, err := v.Create("folder/n.md", "# N\n", false); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete("folder"); err == nil || err.Error() != "only empty folders can be deleted" {
		t.Errorf("delete non-empty = %v", err)
	}
	if err := v.Delete("folder/n.md"); err != nil {
		t.Fatal(err)
	}
	// Finder metadata does not keep a folder alive.
	if err := os.WriteFile(filepath.Join(root, "folder", "inner", ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete("folder/inner"); err != nil {
		t.Errorf("delete folder with .DS_Store = %v", err)
	}
	// Other hidden files do.
	if err := os.WriteFile(filepath.Join(root, "folder", ".keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete("folder"); err == nil {
		t.Error("folder with hidden file deleted")
	}
	if err := v.Delete("nothing.md"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}
}

func TestUploadCollisions(t *testing.T) {
	v, _ := pythonFixture(t)
	first, err := v.Upload("_assets", "x.png", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^_assets/x-[0-9a-f]{6}\.png$`).MatchString(first) {
		t.Errorf("collision name = %q", first)
	}
	if _, err := v.Upload("../out", "a.png", nil); !errors.Is(err, vault.ErrInvalid) {
		t.Errorf("upload outside = %v", err)
	}
	if _, err := v.Upload("", "a.png", nil); !errors.Is(err, vault.ErrInvalid) {
		t.Errorf("upload without folder = %v", err)
	}
}

// test_move_updates_links
func TestMoveUpdatesLinks(t *testing.T) {
	v, root := pythonFixture(t)
	res, err := v.Move("sub/B.md", "topics/Bee.md", true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.UpdatedLinksIn, []string{"A.md"}) || res.Path != "topics/Bee.md" {
		t.Errorf("move result = %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(root, "A.md"))
	if !strings.Contains(string(data), "[[Bee]]") {
		t.Errorf("A.md not rewritten: %q", data)
	}
	back, err := v.Backlinks("topics/Bee.md")
	if err != nil || len(back) == 0 || back[0].Path != "A.md" {
		t.Errorf("backlinks = %+v, %v", back, err)
	}
}

// test_move_preserves_line_endings
func TestMovePreservesLineEndings(t *testing.T) {
	v, root := pythonFixture(t)
	if err := os.WriteFile(filepath.Join(root, "C.md"), []byte("line one\r\nsee [[B]]\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Move("sub/B.md", "other/B2.md", true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "C.md"))
	if string(data) != "line one\r\nsee [[B2]]\r\n" {
		t.Errorf("C.md = %q", data)
	}
}

func TestMoveErrors(t *testing.T) {
	v, _ := pythonFixture(t)
	if _, err := v.Move("sub", "sub/inner/sub", true); err == nil || err.Error() != "cannot move a folder into itself" {
		t.Errorf("move into itself = %v", err)
	}
	if _, err := v.Move("missing.md", "x.md", true); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("move missing = %v", err)
	}
	if _, err := v.Move("A.md", "sub/B.md", true); !errors.Is(err, vault.ErrConflict) {
		t.Errorf("move onto existing = %v", err)
	}
	if _, err := v.Move("A.md", ".hidden.md", true); !errors.Is(err, vault.ErrInvalid) {
		t.Errorf("move to hidden = %v", err)
	}
	res, err := v.Move("sub", "renamed", true)
	if err != nil || res.Path != "renamed" || len(res.UpdatedLinksIn) != 0 {
		t.Errorf("folder move = %+v, %v", res, err)
	}
}

// test_search_and_graph (search part)
func TestSearchAlias(t *testing.T) {
	v, _ := pythonFixture(t)
	hits := v.Search("alias", vault.DefaultSearchLimit)
	if len(hits) == 0 || hits[0].Path != "sub/B.md" {
		t.Errorf("search alias = %+v", hits)
	}
	if hits := v.Search("   ", 50); hits == nil || len(hits) != 0 {
		t.Errorf("blank query = %#v, want empty slice", hits)
	}
}

func TestOddFileNamesAreSearchable(t *testing.T) {
	root := vaulttest.Strings(t, map[string]string{
		" spaced .md":   "needle one",
		`back\slash.md`: "needle two",
	})
	v, _ := vault.New(root)
	var paths []string
	for _, hit := range v.Search("needle", 10) {
		paths = append(paths, hit.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{" spaced .md", `back\slash.md`}) {
		t.Errorf("search = %q", paths)
	}
	for _, bad := range []string{"../x.md", ".git/config", "/etc/passwd", "a//b.md", ""} {
		if _, err := v.ReadText(bad); !errors.Is(err, vault.ErrInvalid) {
			t.Errorf("ReadText(%q) = %v", bad, err)
		}
	}
	if _, err := v.ReadText("missing.md"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("ReadText(missing) = %v", err)
	}
}

func TestNewRequiresDirectory(t *testing.T) {
	if _, err := vault.New(""); err == nil {
		t.Error("empty root accepted")
	}
	v, err := vault.New(filepath.Join(t.TempDir(), "not", "yet"))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Tree()) != 0 || len(v.Files(false)) != 0 {
		t.Error("missing root should look empty")
	}
}

func TestConcurrentAccess(t *testing.T) {
	v, _ := pythonFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := filepath.Join("c", string(rune('a'+i))+".md")
			for j := 0; j < 20; j++ {
				if _, err := v.WriteNote(name, strings.Repeat("x", j), nil); err != nil {
					t.Error(err)
					return
				}
				v.Search("x", 10)
				v.Tree()
			}
		}(i)
	}
	wg.Wait()
}
