package knowledge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"marginalia/internal/knowledge"
	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/knowledge/pyyaml/pyyamltest"
	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

type golden struct {
	InfoCases []struct {
		Rel  string `json:"rel"`
		Text string `json:"text"`
	} `json:"info_cases"`
	Infos      json.RawMessage `json:"infos"`
	Index      json.RawMessage `json:"index"`
	Graph      json.RawMessage `json:"graph"`
	GraphChats json.RawMessage `json:"graph_chats"`
	Split      []struct {
		Text  string          `json:"text"`
		Meta  json.RawMessage `json:"meta"`
		Body  string          `json:"body"`
		Error string          `json:"error"`
	} `json:"split"`
	Join []struct {
		Meta json.RawMessage `json:"meta"`
		Body string          `json:"body"`
		Out  string          `json:"out"`
	} `json:"join"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	var g golden
	vaulttest.Golden(t, "testdata/golden_knowledge.json", &g)
	return g
}

func fixture(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(vaulttest.Fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func jsonEqual(t *testing.T, label string, got any, want json.RawMessage) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(gotJSON, &a)
	_ = json.Unmarshal(want, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("%s:\n got %s\nwant %s", label, gotJSON, want)
	}
}

func TestGoldenNoteInfo(t *testing.T) {
	g := loadGolden(t)
	v := fixture(t)
	ix := v.Index()
	var infos []knowledge.Note
	for _, c := range g.InfoCases {
		infos = append(infos, knowledge.NoteInfo(v, c.Rel, c.Text, ix))
	}
	var want []json.RawMessage
	_ = json.Unmarshal(g.Infos, &want)
	for i := range infos {
		jsonEqual(t, "NoteInfo("+g.InfoCases[i].Text+")", infos[i], want[i])
	}
}

func TestGoldenVaultIndex(t *testing.T) {
	g := loadGolden(t)
	v := fixture(t)
	jsonEqual(t, "VaultIndex", knowledge.VaultIndex(v), g.Index)
}

func TestGoldenGraph(t *testing.T) {
	g := loadGolden(t)
	v := fixture(t)
	jsonEqual(t, "Graph", knowledge.Graph(v, false), g.Graph)
	jsonEqual(t, "Graph(chats)", knowledge.Graph(v, true), g.GraphChats)
}

func TestGoldenSplitFrontmatter(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Split {
		meta, body := knowledge.SplitFrontmatter(c.Text)
		if c.Error != "" {
			// Python crashed (e.g. 2024-02-30 raised ValueError); the port
			// treats the block as invalid instead.
			if meta.Len() != 0 {
				t.Errorf("SplitFrontmatter(%q): want empty meta for a %s case", c.Text, c.Error)
			}
			continue
		}
		want := pyyamltest.MustUntag(c.Meta)
		if !pyyamltest.Same(meta, want) {
			t.Errorf("SplitFrontmatter(%q) meta:\n got %s\nwant %s", c.Text, pyyamltest.Describe(meta), pyyamltest.Describe(want))
		}
		if body != c.Body {
			t.Errorf("SplitFrontmatter(%q) body = %q, want %q", c.Text, body, c.Body)
		}
	}
}

func TestGoldenJoinFrontmatter(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Join {
		meta := pyyamltest.MustUntag(c.Meta).(*pyyaml.Map)
		got, err := knowledge.JoinFrontmatter(meta, c.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.Out {
			t.Errorf("JoinFrontmatter = %q, want %q", got, c.Out)
		}
	}
}

// The link graph of the standard test vault.
func TestGraphFromPythonFixture(t *testing.T) {
	root := vaulttest.Strings(t, map[string]string{
		"A.md":          "---\ntags: [ml]\nsummary: about A\n---\n# A\nsee [[B]] and ![[_assets/x.png]]\n",
		"sub/B.md":      "# B\nback to [[A|alias]]\n",
		"_assets/x.png": "\x89PNG",
	})
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	g := knowledge.Graph(v, false)
	found := false
	for _, e := range g.Edges {
		if e == (knowledge.GraphEdge{Source: "A.md", Target: "sub/B.md"}) {
			found = true
		}
	}
	if !found {
		t.Errorf("edge A.md -> sub/B.md missing: %+v", g.Edges)
	}
	index := knowledge.VaultIndex(v)
	if index[0].Summary != "about A" || !reflect.DeepEqual(index[0].Tags, []string{"ml"}) {
		t.Errorf("index[0] = %+v", index[0])
	}
	if err := os.MkdirAll(filepath.Join(root, "copilot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "copilot", "P.md"), []byte("plugin prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, n := range knowledge.Graph(v, false).Nodes {
		if strings.HasPrefix(n.ID, "copilot/") {
			t.Errorf("plugin data in graph: %s", n.ID)
		}
	}
}

func TestEmptyCollectionsEncodeAsArrays(t *testing.T) {
	v, err := vault.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(knowledge.Graph(v, false))
	if string(data) != `{"nodes":[],"edges":[]}` {
		t.Errorf("empty graph = %s", data)
	}
	data, _ = json.Marshal(knowledge.VaultIndex(v))
	if string(data) != `[]` {
		t.Errorf("empty index = %s", data)
	}
	n := knowledge.NoteInfo(v, "x.md", "", nil)
	data, _ = json.Marshal(n)
	if !strings.Contains(string(data), `"tags":[]`) || !strings.Contains(string(data), `"links":[]`) {
		t.Errorf("note info = %s", data)
	}
}

func TestJoinFrontmatterRoundTrip(t *testing.T) {
	meta := pyyaml.NewMap()
	meta.Set("title", "标题: with colon")
	meta.Set("tags", []any{"a", "b"})
	text, err := knowledge.JoinFrontmatter(meta, "\n\n# Body\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ntitle: '标题: with colon'\ntags:\n- a\n- b\n---\n\n# Body\n"
	if text != want {
		t.Errorf("JoinFrontmatter = %q, want %q", text, want)
	}
	back, body := knowledge.SplitFrontmatter(text)
	if !pyyaml.Equal(back, meta) || body != "\n# Body\n" {
		t.Errorf("round trip = %s, %q", pyyaml.Repr(back), body)
	}
}
