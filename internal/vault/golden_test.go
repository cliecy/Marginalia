package vault_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
	"marginalia/internal/vault/vaulttest"
)

// outcome is a golden {"ok": …} / {"error": …, "status": …} result.
type outcome struct {
	OK     json.RawMessage `json:"ok"`
	Error  string          `json:"error"`
	Status int             `json:"status"`
}

type vaultGolden struct {
	Clean        map[string]outcome         `json:"clean"`
	Tree         json.RawMessage            `json:"tree"`
	Files        []string                   `json:"files"`
	Notes        []string                   `json:"notes"`
	Resolve      map[string]*string         `json:"resolve"`
	Links        map[string][]string        `json:"links"`
	MDLinks      map[string][]string        `json:"md_links"`
	Search       map[string]json.RawMessage `json:"search"`
	SearchLimits map[string]json.RawMessage `json:"search_limits"`
	Backlinks    map[string]outcome         `json:"backlinks"`
	Natural      []string                   `json:"natural"`
	Uploads      map[string]string          `json:"uploads"`
	Mime         map[string]string          `json:"mime"`
	Read         map[string]outcome         `json:"read"`
	Move         struct{ Steps []moveStep } `json:"move"`
}

type moveStep struct {
	Src    string            `json:"src"`
	Dst    string            `json:"dst"`
	Update bool              `json:"update"`
	Result outcome           `json:"result"`
	Files  map[string]string `json:"files"`
}

func loadVaultGolden(t *testing.T) vaultGolden {
	t.Helper()
	var g vaultGolden
	vaulttest.Golden(t, "testdata/golden_vault.json", &g)
	return g
}

// fixtureVault opens the golden fixture in a temp folder.
func fixtureVault(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	root := vaulttest.Fixture(t)
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return v, root
}

// jsonEqual compares v's JSON encoding with a golden JSON value.
func jsonEqual(t *testing.T, label string, got any, want json.RawMessage) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(gotJSON, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s:\n got %s\nwant %s", label, gotJSON, want)
	}
}

func checkOutcome(t *testing.T, label string, got any, err error, want outcome) {
	t.Helper()
	if want.Error != "" {
		if err == nil {
			t.Errorf("%s: got success %v, want error %q", label, got, want.Error)
			return
		}
		if err.Error() != want.Error || vault.StatusOf(err) != want.Status {
			t.Errorf("%s: got error %q (%d), want %q (%d)", label, err, vault.StatusOf(err), want.Error, want.Status)
		}
		return
	}
	if err != nil {
		t.Errorf("%s: unexpected error %v", label, err)
		return
	}
	jsonEqual(t, label, got, want.OK)
}

func TestGoldenClean(t *testing.T) {
	g := loadVaultGolden(t)
	v, _ := fixtureVault(t)
	for in, want := range g.Clean {
		got, err := v.Clean(in)
		checkOutcome(t, "Clean("+strings.ReplaceAll(in, "\x00", `\0`)+")", got, err, want)
	}
}

func TestGoldenTreeAndFiles(t *testing.T) {
	g := loadVaultGolden(t)
	v, _ := fixtureVault(t)
	tree := v.Tree()
	// Strip mtimes, which differ between runs.
	var strip func(nodes []*vault.Node)
	strip = func(nodes []*vault.Node) {
		for _, n := range nodes {
			n.Mtime = 0
			strip(n.Children)
		}
	}
	strip(tree)
	var want []map[string]any
	if err := json.Unmarshal(g.Tree, &want); err != nil {
		t.Fatal(err)
	}
	var addMtime func(items []map[string]any)
	addMtime = func(items []map[string]any) {
		for _, item := range items {
			if item["type"] == "folder" {
				var children []map[string]any
				raw, _ := json.Marshal(item["children"])
				_ = json.Unmarshal(raw, &children)
				addMtime(children)
				item["children"] = children
			} else {
				item["mtime"] = 0
			}
		}
	}
	addMtime(want)
	wantJSON, _ := json.Marshal(want)
	jsonEqual(t, "Tree", tree, wantJSON)

	if got := v.Files(false); !reflect.DeepEqual(got, g.Files) {
		t.Errorf("Files(false):\n got %q\nwant %q", got, g.Files)
	}
	if got := v.Files(true); !reflect.DeepEqual(got, g.Notes) {
		t.Errorf("Files(true):\n got %q\nwant %q", got, g.Notes)
	}
}

func TestGoldenResolveLink(t *testing.T) {
	g := loadVaultGolden(t)
	v, _ := fixtureVault(t)
	ix := v.Index()
	if len(g.Resolve) == 0 {
		t.Fatal("empty golden")
	}
	for key, want := range g.Resolve {
		target, source, _ := strings.Cut(key, "\u241f")
		got := ix.ResolveLink(target, source)
		w := ""
		if want != nil {
			w = *want
		}
		if got != w {
			t.Errorf("ResolveLink(%q, %q) = %q, want %q", target, source, got, w)
		}
	}
}

func TestGoldenLinksFrom(t *testing.T) {
	g := loadVaultGolden(t)
	v, _ := fixtureVault(t)
	ix := v.Index()
	for rel, want := range g.Links {
		text, err := v.ReadText(rel)
		if err != nil {
			t.Fatal(err)
		}
		got := ix.LinksFrom(rel, text)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("LinksFrom(%s):\n got %q\nwant %q", rel, got, want)
		}
	}
}

func TestGoldenMarkdownLinkScan(t *testing.T) {
	g := loadVaultGolden(t)
	for text, want := range g.MDLinks {
		var got []string
		for _, l := range vault.FindMDLinks(text) {
			got = append(got, l.Target)
		}
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FindMDLinks(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestGoldenSearch(t *testing.T) {
	g := loadVaultGolden(t)
	v, _ := fixtureVault(t)
	for q, want := range g.Search {
		jsonEqual(t, "Search("+q+")", v.Search(q, vault.DefaultSearchLimit), want)
	}
	for lim, want := range g.SearchLimits {
		var n int
		_ = json.Unmarshal([]byte(lim), &n)
		jsonEqual(t, "Search(a, "+lim+")", v.Search("a", n), want)
	}
}

func TestGoldenBacklinks(t *testing.T) {
	g := loadVaultGolden(t)
	v, _ := fixtureVault(t)
	for rel, want := range g.Backlinks {
		got, err := v.Backlinks(rel)
		checkOutcome(t, "Backlinks("+rel+")", got, err, want)
	}
}

func TestGoldenRead(t *testing.T) {
	g := loadVaultGolden(t)
	v, _ := fixtureVault(t)
	for rel, want := range g.Read {
		note, err := v.ReadNote(rel)
		var got any
		if err == nil {
			got = map[string]any{"path": note.Path, "content": note.Content, "etag": note.ETag}
		}
		checkOutcome(t, "ReadNote("+rel+")", got, err, want)
	}
}

func TestGoldenNaturalOrder(t *testing.T) {
	g := loadVaultGolden(t)
	// Python's sorted() result must be non-decreasing under NaturalCompare.
	for i := 1; i < len(g.Natural); i++ {
		if pystr.NaturalCompare(g.Natural[i-1], g.Natural[i]) > 0 {
			t.Errorf("%q sorts after %q", g.Natural[i-1], g.Natural[i])
		}
	}
}

func TestGoldenUploadNames(t *testing.T) {
	g := loadVaultGolden(t)
	for name, want := range g.Uploads {
		root := t.TempDir()
		v, _ := vault.New(root)
		got, err := v.Upload("_assets", name, []byte("data"))
		if err != nil {
			t.Errorf("Upload(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("Upload(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestGoldenGuessType(t *testing.T) {
	g := loadVaultGolden(t)
	for name, want := range g.Mime {
		// The golden comes from CPython's built-in table only. In production
		// Python also read the system's mime.types (like Go's fallback does),
		// so extensions unknown to the built-in table may legitimately differ.
		if want == "application/octet-stream" {
			continue
		}
		if got := vault.GuessType(name); got != want {
			t.Errorf("GuessType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestGoldenMoveSequence(t *testing.T) {
	g := loadVaultGolden(t)
	v, root := fixtureVault(t)
	if len(g.Move.Steps) == 0 {
		t.Fatal("empty golden")
	}
	for _, step := range g.Move.Steps {
		label := "Move(" + step.Src + " -> " + step.Dst + ")"
		got, err := v.Move(step.Src, step.Dst, step.Update)
		checkOutcome(t, label, got, err, step.Result)
		files := snapshot(t, root)
		for rel, want := range step.Files {
			if files[rel] != want {
				gotText, _ := base64.StdEncoding.DecodeString(files[rel])
				wantText, _ := base64.StdEncoding.DecodeString(want)
				t.Errorf("%s: %s =\n%q\nwant\n%q", label, rel, gotText, wantText)
			}
		}
		for rel := range files {
			if _, ok := step.Files[rel]; !ok {
				t.Errorf("%s: unexpected file %s", label, rel)
			}
		}
	}
}

// snapshot returns every non-.git file of the vault, base64-encoded.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = base64.StdEncoding.EncodeToString(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var errSentinels = []error{vault.ErrInvalid, vault.ErrNotFound, vault.ErrConflict}

func TestErrorSentinels(t *testing.T) {
	for _, s := range errSentinels {
		e := &vault.Error{Status: s.(*vault.Error).Status, Message: "x"}
		if !errors.Is(e, s) {
			t.Errorf("errors.Is(%v, sentinel %d) = false", e, e.Status)
		}
	}
	if errors.Is(vault.NotFound("x"), vault.ErrConflict) {
		t.Error("NotFound must not match ErrConflict")
	}
	if vault.StatusOf(errors.New("io")) != 500 {
		t.Error("plain errors map to 500")
	}
	if !regexp.MustCompile(`^Bad Request$`).MatchString(vault.ErrInvalid.Error()) {
		t.Errorf("sentinel text = %q", vault.ErrInvalid.Error())
	}
}
