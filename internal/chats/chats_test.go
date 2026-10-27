package chats

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marginalia/internal/knowledge"
	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/knowledge/pyyaml/pyyamltest"
	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

type outcome struct {
	OK     json.RawMessage `json:"ok"`
	Error  string          `json:"error"`
	Status int             `json:"status"`
}

type savedGolden struct {
	Path    string          `json:"path"`
	ETag    string          `json:"etag"`
	Meta    json.RawMessage `json:"meta"`
	Content string          `json:"content"`
}

type golden struct {
	Slug   map[string]string `json:"slug"`
	Render []struct {
		Meta     json.RawMessage `json:"meta"`
		Messages []Message       `json:"messages"`
		Out      string          `json:"out"`
	} `json:"render"`
	Parse []struct {
		Text     string          `json:"text"`
		Meta     json.RawMessage `json:"meta"`
		Messages []Message       `json:"messages"`
	} `json:"parse"`
	Save struct {
		First    savedGolden `json:"first"`
		Second   savedGolden `json:"second"`
		Existing savedGolden `json:"existing"`
		Missing  savedGolden `json:"missing"`
		Bad      outcome     `json:"bad"`
		Stale    outcome     `json:"stale"`
		Status1  string      `json:"status_1"`
		Status2  string      `json:"status_2"`
		Status3  string      `json:"status_3"`
		List     []Summary   `json:"list"`
		Loaded   struct {
			Path     string          `json:"path"`
			ETag     string          `json:"etag"`
			Meta     json.RawMessage `json:"meta"`
			Messages []Message       `json:"messages"`
		} `json:"loaded"`
	} `json:"save"`
	List []Summary `json:"list"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	var g golden
	vaulttest.Golden(t, "testdata/golden_chats.json", &g)
	return g
}

// freezeClock makes Save use the instant the Python golden run used.
func freezeClock(t *testing.T) {
	t.Helper()
	old := clock
	clock = func() time.Time { return time.Date(2025, 3, 4, 5, 6, 7, 0, time.FixedZone("", 9*3600)) }
	t.Cleanup(func() { clock = old })
}

func fixture(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	root := vaulttest.Fixture(t)
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return v, v.Root()
}

func untagMap(t *testing.T, raw json.RawMessage) *pyyaml.Map {
	t.Helper()
	m, ok := pyyamltest.MustUntag(raw).(*pyyaml.Map)
	if !ok {
		t.Fatalf("golden meta is not a map: %s", raw)
	}
	return m
}

func sameMeta(t *testing.T, label string, got *pyyaml.Map, want json.RawMessage) {
	t.Helper()
	w := untagMap(t, want)
	if !pyyamltest.Same(got, w) {
		t.Errorf("%s meta:\n got %s\nwant %s", label, pyyamltest.Describe(got), pyyamltest.Describe(w))
	}
}

func fileIs(t *testing.T, label, path, wantB64 string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := base64.StdEncoding.DecodeString(wantB64)
	if string(got) != string(want) {
		t.Errorf("%s:\n got %q\nwant %q", label, got, want)
	}
}

func TestGoldenSlug(t *testing.T) {
	for in, want := range loadGolden(t).Slug {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoldenRender(t *testing.T) {
	for _, c := range loadGolden(t).Render {
		got, err := Render(untagMap(t, c.Meta), c.Messages)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.Out {
			t.Errorf("Render:\n got %q\nwant %q", got, c.Out)
		}
	}
}

func TestGoldenParse(t *testing.T) {
	for _, c := range loadGolden(t).Parse {
		meta, messages := Parse(c.Text)
		sameMeta(t, "Parse", meta, c.Meta)
		if !reflect.DeepEqual(messages, c.Messages) && !(len(messages) == 0 && len(c.Messages) == 0) {
			t.Errorf("Parse(%q) messages:\n got %q\nwant %q", c.Text, messages, c.Messages)
		}
	}
}

func TestGoldenList(t *testing.T) {
	g := loadGolden(t)
	v, _ := fixture(t)
	if got := List(v); !reflect.DeepEqual(got, g.List) {
		t.Errorf("List:\n got %+v\nwant %+v", got, g.List)
	}
}

func TestGoldenSaveSequence(t *testing.T) {
	g := loadGolden(t)
	freezeClock(t)
	v, root := fixture(t)
	msgs := g.Render[0].Messages

	first, err := Save(v, "", "PPO 是什么", "m", msgs[:2], nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != g.Save.First.Path || first.ETag != g.Save.First.ETag {
		t.Errorf("first save = %s %s, want %s %s", first.Path, first.ETag, g.Save.First.Path, g.Save.First.ETag)
	}
	sameMeta(t, "first", first.Meta, g.Save.First.Meta)
	fileIs(t, "first file", filepath.Join(root, first.Path), g.Save.First.Content)

	etag := first.ETag
	second, err := Save(v, first.Path, "", "m2", msgs[:4], &etag)
	if err != nil {
		t.Fatal(err)
	}
	sameMeta(t, "second", second.Meta, g.Save.Second.Meta)
	fileIs(t, "second file", filepath.Join(root, first.Path), g.Save.Second.Content)

	existing, err := Save(v, "Chats/2025-02-01 0900 second.md", "Renamed", "m3", msgs[:1], nil)
	if err != nil {
		t.Fatal(err)
	}
	sameMeta(t, "existing", existing.Meta, g.Save.Existing.Meta)
	fileIs(t, "existing file", filepath.Join(root, existing.Path), g.Save.Existing.Content)

	missing, err := Save(v, "Chats/new-file.md", "", "m4", msgs[:1], nil)
	if err != nil || missing.Path != g.Save.Missing.Path {
		t.Fatalf("missing save = %+v, %v", missing, err)
	}
	sameMeta(t, "missing", missing.Meta, g.Save.Missing.Meta)

	_, err = Save(v, "Notes/x.md", "t", "m", msgs[:1], nil)
	if err == nil || err.Error() != g.Save.Bad.Error || vault.StatusOf(err) != g.Save.Bad.Status {
		t.Errorf("bad folder = %v, want %q", err, g.Save.Bad.Error)
	}
	_, err = Save(v, first.Path, "t", "m", msgs[:1], ptr("stale"))
	if err == nil || err.Error() != g.Save.Stale.Error || vault.StatusOf(err) != g.Save.Stale.Status {
		t.Errorf("stale = %v, want %q", err, g.Save.Stale.Error)
	}

	if _, err := SetStatus(v, first.Path, StatusDistilled, []string{"RL/PPO.md", "A.md"}); err != nil {
		t.Fatal(err)
	}
	fileIs(t, "status 1", filepath.Join(root, first.Path), g.Save.Status1)
	if _, err := SetStatus(v, first.Path, StatusDistilled, []string{"RL/PPO.md", "B.markdown", "ab", "ab"}); err != nil {
		t.Fatal(err)
	}
	fileIs(t, "status 2", filepath.Join(root, first.Path), g.Save.Status2)
	if _, err := SetStatus(v, "Chats/2025-01-01 1000 hello.md", StatusInbox, nil); err != nil {
		t.Fatal(err)
	}
	fileIs(t, "status 3", filepath.Join(root, "Chats/2025-01-01 1000 hello.md"), g.Save.Status3)

	if got := List(v); !reflect.DeepEqual(got, g.Save.List) {
		t.Errorf("List after saves:\n got %+v\nwant %+v", got, g.Save.List)
	}
	loaded, err := Load(v, first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Path != g.Save.Loaded.Path || loaded.ETag != g.Save.Loaded.ETag ||
		!reflect.DeepEqual(loaded.Messages, g.Save.Loaded.Messages) {
		t.Errorf("Load = %+v", loaded)
	}
	sameMeta(t, "loaded", loaded.Meta, g.Save.Loaded.Meta)
}

func ptr(s string) *string { return &s }

// Saving and loading a chat.
func TestChatRoundtrip(t *testing.T) {
	root := vaulttest.Strings(t, map[string]string{"A.md": "# A\n"})
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	messages := []Message{{Role: "user", Content: "什么是 PPO?"}, {Role: "assistant", Content: "## 答\n<!-- x -->\n内容"}}
	saved, err := Save(v, "", "PPO 是什么", "m", messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(v, saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Messages, messages) {
		t.Errorf("messages = %q", loaded.Messages)
	}
	if loaded.Meta.Value("status") != "inbox" {
		t.Errorf("status = %v", loaded.Meta.Value("status"))
	}
	if _, err := SetStatus(v, saved.Path, StatusDistilled, []string{"RL/PPO.md"}); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(filepath.Join(v.Root(), saved.Path))
	meta, _ := knowledge.SplitFrontmatter(string(text))
	if meta.Value("status") != "distilled" || !pyyaml.Equal(meta.Value("distilled_into"), []any{"[[RL/PPO]]"}) {
		t.Errorf("meta = %s", pyyaml.Repr(meta))
	}
}

func TestNewChatNeverOverwrites(t *testing.T) {
	freezeClock(t)
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := Save(v, "", "Same title", "m", []Message{{Role: "user", Content: "one"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Save(v, "", "Same title", "m", []Message{{Role: "user", Content: "two"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path != "Chats/2025-03-04 0506 Same title.md" || b.Path != "Chats/2025-03-04 0506 Same title 2.md" {
		t.Errorf("paths = %q, %q", a.Path, b.Path)
	}
	first, _ := Load(v, a.Path)
	if first.Messages[0].Content != "one" {
		t.Error("first chat was overwritten")
	}
}

func TestMessageJSON(t *testing.T) {
	var msgs []Message
	in := `[{"role":"user","content":"hi"},{"role":"assistant"},{"role":"user","content":null},` +
		`{"role":"user","content":[{"type":"text","text":"x"}]},{"role":"user","content":1.0},{"role":3,"content":true}]`
	if err := json.Unmarshal([]byte(in), &msgs); err != nil {
		t.Fatal(err)
	}
	want := []Message{
		{"user", "hi"}, {"assistant", ""}, {"user", "None"},
		{"user", "[{'type': 'text', 'text': 'x'}]"}, {"user", "1.0"}, {"", "True"},
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Errorf("decoded = %q", msgs)
	}
	out, _ := json.Marshal(Message{Role: "user", Content: "x"})
	if string(out) != `{"role":"user","content":"x"}` {
		t.Errorf("encoded = %s", out)
	}
}

func TestSetStatusErrors(t *testing.T) {
	root := vaulttest.Strings(t, map[string]string{
		"Chats/odd.md": "---\ndistilled_into: 5\n---\n<!-- chat:user -->\nx\n",
	})
	v, _ := vault.New(root)
	if _, err := SetStatus(v, "Chats/missing.md", StatusDistilled, nil); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("missing chat = %v", err)
	}
	if _, err := SetStatus(v, "Chats/odd.md", StatusDistilled, []string{"A.md"}); err == nil ||
		!strings.Contains(err.Error(), "distilled_into") {
		t.Errorf("non-list distilled_into = %v", err)
	}
}
