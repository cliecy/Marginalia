package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"marginalia/internal/knowledge"
	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

// golden is internal/ai/testdata/golden.json, recorded from the
// reference implementation.
type golden struct {
	Today   string            `json:"today"`
	Fixture map[string]string `json:"fixture"` // path -> base64 content
	Prompts struct {
		BaseSystem string `json:"base_system"`
		Plan       string `json:"plan"`
		Write      string `json:"write"`
	} `json:"prompts"`
	IndexListing string `json:"index_listing"`
	Conventions  string `json:"conventions"`
	VaultContext []struct {
		Budget       int      `json:"budget"`
		Focus        []string `json:"focus"`
		Query        string   `json:"query"`
		IncludeChats bool     `json:"include_chats"`
		Output       string   `json:"output"`
	} `json:"vault_context"`
	Chat []struct {
		Budget    int        `json:"budget"`
		Scope     string     `json:"scope"`
		Path      *string    `json:"path"`
		Selection *string    `json:"selection"`
		History   []Message  `json:"history"`
		Output    []Message  `json:"output"`
		Error     *goldenErr `json:"error"`
	} `json:"chat"`
	ExtractJSON []struct {
		Input  string          `json:"input"`
		Output json.RawMessage `json:"output"`
		Error  *string         `json:"error"`
	} `json:"extract_json"`
	StripFence []struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	} `json:"strip_fence"`
	Agent []struct {
		Name      string   `json:"name"`
		Model     string   `json:"model"`
		Task      string   `json:"task"`
		Chats     []string `json:"chats"`
		Path      *string  `json:"path"`
		Budget    int      `json:"budget"`
		Responses []string `json:"responses"`
		Calls     []struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		} `json:"calls"`
		Events []map[string]any `json:"events"`
		Error  *goldenErr       `json:"error"`
	} `json:"agent"`
	Apply []struct {
		Name       string             `json:"name"`
		Operations []Proposal         `json:"operations"`
		Mutate     map[string]*string `json:"mutate"`
		Touched    []string           `json:"touched"`
		Error      *goldenErr         `json:"error"`
		Files      map[string]string  `json:"files"`
	} `json:"apply"`
}

type goldenErr struct {
	Type    string `json:"type"`
	Status  int    `json:"status"`
	Message string `json:"message"`
}

var loadGolden = sync.OnceValues(func() (*golden, error) {
	data, err := os.ReadFile(filepath.Join("testdata", "golden.json"))
	if err != nil {
		return nil, err
	}
	g := new(golden)
	return g, json.Unmarshal(data, g)
})

func mustGolden(t *testing.T) *golden {
	t.Helper()
	g, err := loadGolden()
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	return g
}

// setupGolden loads the golden data, freezes the clock on its date and writes
// a fresh copy of the fixture vault.
func setupGolden(t *testing.T) (*golden, *vault.Vault) {
	t.Helper()
	g := mustGolden(t)
	day, err := time.ParseInLocation(time.DateOnly, g.Today, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	freezeClock(t, day.Add(15*time.Hour))
	return g, fixtureVault(t, g.Fixture)
}

func freezeClock(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

func fixtureVault(t *testing.T, files map[string]string) *vault.Vault {
	t.Helper()
	decoded := make(map[string][]byte, len(files))
	for rel, b64 := range files {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("fixture %s: %v", rel, err)
		}
		decoded[rel] = data
	}
	v, err := vault.New(vaulttest.Write(t, decoded))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestGoldenPrompts(t *testing.T) {
	g, _ := setupGolden(t)
	for name, pair := range map[string][2]string{
		"BaseSystem":  {BaseSystem, g.Prompts.BaseSystem},
		"PlanPrompt":  {PlanPrompt, g.Prompts.Plan},
		"WritePrompt": {WritePrompt, g.Prompts.Write},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s differs from Python:\n got %q\nwant %q", name, pair[0], pair[1])
		}
	}
}

func TestGoldenIndexAndConventions(t *testing.T) {
	g, v := setupGolden(t)
	if got := IndexListing(knowledge.VaultIndex(v)); got != g.IndexListing {
		t.Errorf("IndexListing:\n got %q\nwant %q", got, g.IndexListing)
	}
	if got := Conventions(v); got != g.Conventions {
		t.Errorf("Conventions = %q, want %q", got, g.Conventions)
	}
}

func TestGoldenVaultContext(t *testing.T) {
	g, v := setupGolden(t)
	for i, c := range g.VaultContext {
		got := VaultContext(v, c.Budget, c.Focus, c.Query, c.IncludeChats)
		if got != c.Output {
			t.Errorf("case %d (budget %d, focus %q, query %q, chats %v):\n got %s\nwant %s",
				i, c.Budget, c.Focus, c.Query, c.IncludeChats, firstDiff(got, c.Output), firstDiff(c.Output, got))
		}
	}
}

func TestGoldenBuildChatMessages(t *testing.T) {
	g, v := setupGolden(t)
	for i, c := range g.Chat {
		got, err := BuildChatMessages(v, c.Budget, c.History, c.Scope, deref(c.Path), deref(c.Selection))
		if c.Error != nil {
			if err == nil || err.Error() != c.Error.Message || vault.StatusOf(err) != c.Error.Status {
				t.Errorf("case %d (%s %q): error %v (status %d), want %q (%d)",
					i, c.Scope, deref(c.Path), err, vault.StatusOf(err), c.Error.Message, c.Error.Status)
			}
			continue
		}
		if err != nil {
			t.Errorf("case %d (%s %q): %v", i, c.Scope, deref(c.Path), err)
			continue
		}
		if len(got) != len(c.Output) {
			t.Errorf("case %d: %d messages, want %d", i, len(got), len(c.Output))
			continue
		}
		for j := range got {
			if got[j] != c.Output[j] {
				t.Errorf("case %d (%s %q) message %d:\n got %s\nwant %s", i, c.Scope, deref(c.Path), j,
					firstDiff(got[j].Role+": "+got[j].Content, c.Output[j].Role+": "+c.Output[j].Content),
					firstDiff(c.Output[j].Role+": "+c.Output[j].Content, got[j].Role+": "+got[j].Content))
			}
		}
	}
}

func TestGoldenExtractJSON(t *testing.T) {
	g, _ := setupGolden(t)
	for _, c := range g.ExtractJSON {
		plan, err := ExtractJSON(c.Input)
		if c.Error != nil {
			want := *c.Error
			if prefix, _, ok := strings.Cut(want, "："); ok && strings.HasPrefix(want, "无法解析") {
				want = prefix + "：" // the decoder's own message differs from Python's
			}
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("ExtractJSON(%q) error = %v, want %q…", c.Input, err, want)
			}
			var aiErr *Error
			if err != nil && !errors.As(err, &aiErr) {
				t.Errorf("ExtractJSON(%q) error %T is not *ai.Error", c.Input, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ExtractJSON(%q): %v", c.Input, err)
			continue
		}
		gotJSON, err := pyyaml.ToJSON(plan)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(t, gotJSON, c.Output) {
			t.Errorf("ExtractJSON(%q) = %s, want %s", c.Input, gotJSON, c.Output)
		}
	}
}

func TestGoldenStripFence(t *testing.T) {
	g, _ := setupGolden(t)
	for _, c := range g.StripFence {
		if got := StripFence(c.Input); got != c.Output {
			t.Errorf("StripFence(%q) = %q, want %q", c.Input, got, c.Output)
		}
	}
}

// scriptedModel is a Completer that returns canned answers and records the
// requests, like the Python generator's FakeClient.
type scriptedModel struct {
	mu        sync.Mutex
	responses []string
	calls     []recordedCall
}

type recordedCall struct {
	Model    string
	Messages []Message
}

func (m *scriptedModel) Complete(_ context.Context, model string, messages []Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, recordedCall{model, append([]Message(nil), messages...)})
	if len(m.responses) == 0 {
		return "", &Error{Message: "fake model: no more responses"}
	}
	answer := m.responses[0]
	m.responses = m.responses[1:]
	return answer, nil
}

func TestGoldenAgent(t *testing.T) {
	g := mustGolden(t)
	for _, c := range g.Agent {
		t.Run(c.Name, func(t *testing.T) {
			_, v := setupGolden(t)
			model := &scriptedModel{responses: append([]string(nil), c.Responses...)}
			var events []Event
			err := RunAgent(context.Background(), model, v, c.Budget,
				AgentRequest{Model: c.Model, Task: c.Task, Chats: c.Chats, Path: deref(c.Path)},
				func(e Event) error { events = append(events, e); return nil })

			switch {
			case c.Error != nil && err == nil:
				t.Errorf("no error, want %q", c.Error.Message)
			case c.Error != nil && !strings.HasPrefix(err.Error(), strings.SplitAfter(c.Error.Message, "：")[0]):
				t.Errorf("error %q, want %q", err, c.Error.Message)
			case c.Error == nil && err != nil:
				t.Errorf("unexpected error: %v", err)
			}

			if len(model.calls) != len(c.Calls) {
				t.Fatalf("%d model calls, want %d", len(model.calls), len(c.Calls))
			}
			for i, want := range c.Calls {
				got := model.calls[i]
				if got.Model != want.Model {
					t.Errorf("call %d model %q, want %q", i, got.Model, want.Model)
				}
				if len(got.Messages) != len(want.Messages) {
					t.Errorf("call %d: %d messages, want %d", i, len(got.Messages), len(want.Messages))
					continue
				}
				for j := range want.Messages {
					if got.Messages[j] != want.Messages[j] {
						t.Errorf("call %d message %d:\n got %s\nwant %s", i, j,
							firstDiff(got.Messages[j].Content, want.Messages[j].Content),
							firstDiff(want.Messages[j].Content, got.Messages[j].Content))
					}
				}
			}

			gotEvents := make([]any, len(events))
			for i, e := range events {
				data, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				gotEvents[i] = decodeJSON(t, data)
			}
			wantEvents := make([]any, len(c.Events))
			for i, e := range c.Events {
				wantEvents[i] = normalizeNulls(e)
			}
			if !reflect.DeepEqual(gotEvents, wantEvents) {
				got, _ := json.MarshalIndent(gotEvents, "", " ")
				want, _ := json.MarshalIndent(wantEvents, "", " ")
				t.Errorf("events differ:\n got %s\nwant %s", got, want)
			}
		})
	}
}

// normalizeNulls applies the one deliberate difference in event shapes:
// Python passed a null summary or reason through, Go sends "".
func normalizeNulls(event map[string]any) map[string]any {
	out := make(map[string]any, len(event))
	for k, v := range event {
		out[k] = v
	}
	if s, ok := out["summary"]; ok && s == nil {
		out["summary"] = ""
	}
	if p, ok := out["proposal"].(map[string]any); ok {
		cp := make(map[string]any, len(p))
		for k, v := range p {
			cp[k] = v
		}
		if cp["reason"] == nil {
			cp["reason"] = ""
		}
		out["proposal"] = cp
	}
	return out
}

func TestGoldenApplyChangeset(t *testing.T) {
	g := mustGolden(t)
	for _, c := range g.Apply {
		t.Run(c.Name, func(t *testing.T) {
			_, v := setupGolden(t)
			for rel, content := range c.Mutate {
				p := filepath.Join(v.Root(), filepath.FromSlash(rel))
				if content == nil {
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(p, []byte(*content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			touched, err := ApplyChangeset(v, c.Operations)
			if c.Error != nil {
				if err == nil || err.Error() != c.Error.Message || vault.StatusOf(err) != c.Error.Status {
					t.Errorf("error %v (status %d), want %q (%d)", err, vault.StatusOf(err), c.Error.Message, c.Error.Status)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !reflect.DeepEqual(touched, c.Touched) {
					t.Errorf("touched = %q, want %q", touched, c.Touched)
				}
			}
			got := snapshot(t, v.Root())
			for rel, want := range c.Files {
				if got[rel] != want {
					gotText, _ := base64.StdEncoding.DecodeString(got[rel])
					wantText, _ := base64.StdEncoding.DecodeString(want)
					t.Errorf("%s = %q, want %q", rel, gotText, wantText)
				}
			}
			for rel := range got {
				if _, ok := c.Files[rel]; !ok {
					t.Errorf("unexpected file %s", rel)
				}
			}
		})
	}
}

// snapshot maps every file under root to its base64 content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files[filepath.ToSlash(rel)] = base64.StdEncoding.EncodeToString(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func decodeJSON(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return v
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	return reflect.DeepEqual(decodeJSON(t, a), decodeJSON(t, b))
}

// firstDiff returns a short window of a around the first byte where it
// differs from b, to keep failure messages readable for long prompts.
func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	start, end := max(0, i-80), min(len(a), i+120)
	return strings.ToValidUTF8(a[start:end], "�") + " (at byte " + strconv.Itoa(i) + " of " + strconv.Itoa(len(a)) + ")"
}
