package ai

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"marginalia/internal/knowledge"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
	"marginalia/internal/vault/vaulttest"
)

func TestClip(t *testing.T) {
	for _, c := range []struct {
		text  string
		limit int
		want  string
	}{
		{"", 0, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc\n…（已截断）"},
		{"中文字符", 4, "中文字符"},
		{"中文字符串", 4, "中文字符\n…（已截断）"},
		{"a", 0, "\n…（已截断）"},
	} {
		if got := clip(c.text, c.limit); got != c.want {
			t.Errorf("clip(%q, %d) = %q, want %q", c.text, c.limit, got, c.want)
		}
	}
}

func TestIndexListing(t *testing.T) {
	links := make([]string, 14)
	for i := range links {
		links[i] = fmt.Sprintf("L%02d.md", i)
	}
	links[1] = "img.png"
	notes := []knowledge.Note{
		{Path: "a.md", Title: "A", Summary: strings.Repeat("摘", 120), Tags: []string{"x", "y/z"}, Status: "draft", Links: links},
		{Path: "b.md", Title: "B"},
	}
	want := "- a.md [draft]：A — " + strings.Repeat("摘", 100) + " #x #y/z → L00, img.png, L02, L03, L04, L05, L06, L07, L08, L09, L10, L11\n" +
		"- b.md：B — "
	if got := IndexListing(notes); got != want {
		t.Errorf("IndexListing =\n%q\nwant\n%q", got, want)
	}
	if got := IndexListing(nil); got != "" {
		t.Errorf("IndexListing(nil) = %q", got)
	}
}

// budgetVault has notes of known sizes: tiny (10 chars), small (60), big
// (600), a chat (40) and a non-note file.
func budgetVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(vaulttest.Strings(t, map[string]string{
		"tiny.md":      "tiny note\n",
		"small.md":     strings.Repeat("s", 59) + "\n",
		"big.md":       "needle " + strings.Repeat("b", 592) + "\n",
		"Chats/c.md":   "---\ntype: chat\n---\n" + strings.Repeat("c", 21) + "\n",
		"data.json":    `{"needle": true}`,
		"copilot/p.md": "plugin data needle\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var contextBlockRE = regexp.MustCompile(`(?m)^===== 笔记：(.+) =====$`)

// included lists the notes VaultContext put in full, in order.
func included(ctx string) []string {
	var out []string
	for _, m := range contextBlockRE.FindAllStringSubmatch(ctx, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestVaultContextBudget(t *testing.T) {
	v := budgetVault(t)
	header := VaultContext(v, 0, nil, "", false) // the index alone, always included
	if !strings.HasPrefix(header, "# 笔记库索引（共 4 篇）\n- Chats/c.md") || len(included(header)) != 0 {
		t.Fatalf("header = %q", header)
	}
	base := pystr.Len(header)
	block := func(rel string) int {
		note, err := v.ReadNote(rel)
		if err != nil {
			t.Fatal(err)
		}
		return pystr.Len("\n\n===== 笔记：" + rel + " =====\n" + note.Content)
	}

	for _, c := range []struct {
		name         string
		budget       int
		focus        []string
		query        string
		includeChats bool
		want         string
	}{
		{"everything, shortest first", 1 << 20, nil, "", false, "tiny.md small.md big.md"},
		{"chats on request", 1 << 20, nil, "", true, "tiny.md Chats/c.md small.md big.md"},
		{"exact fit", base + block("tiny.md") + block("small.md"), nil, "", false, "tiny.md small.md"},
		{"one char short skips, keeps trying", base + block("tiny.md") + block("small.md") - 1, nil, "", false, "tiny.md"},
		{"focus first", 1 << 20, []string{"big.md", "tiny.md"}, "", false, "big.md tiny.md small.md"},
		{"focus too big is skipped", base + block("small.md") + block("tiny.md"), []string{"big.md", "small.md"}, "", false, "small.md tiny.md"},
		// Search covers every note, plugin folders included (as in Python).
		{"search hits before the pool", 1 << 20, nil, "needle", false, "big.md copilot/p.md tiny.md small.md"},
		{"focus dedupe and non-notes", 1 << 20, []string{"data.json", "tiny.md", "tiny.md", "../x.md", "missing.md"}, "", false, "tiny.md small.md big.md"},
		{"focused chat is included", 1 << 20, []string{"Chats/c.md"}, "", false, "Chats/c.md tiny.md small.md big.md"},
	} {
		got := strings.Join(included(VaultContext(v, c.budget, c.focus, c.query, c.includeChats)), " ")
		if got != c.want {
			t.Errorf("%s: included %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBuildChatMessagesScopes(t *testing.T) {
	freezeClock(t, time.Date(2026, 1, 2, 23, 59, 0, 0, time.Local))
	v := budgetVault(t)
	history := []Message{{Role: RoleUser, Content: "needle"}, {Role: "tool", Content: "x"}, {Role: RoleAssistant, Content: "yes"}}

	msgs, err := BuildChatMessages(v, 1<<20, history, ScopeNone, "tiny.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[0].Role != RoleSystem || msgs[1] != history[0] || msgs[2] != history[2] {
		t.Fatalf("messages = %+v", msgs)
	}
	if want := BaseSystem + "\n\n今天是 2026-01-02。"; msgs[0].Content != want {
		t.Errorf("system = %q", msgs[0].Content)
	}

	// "" behaves like "note": with a path, the index plus that note.
	msgs, err = BuildChatMessages(v, 1<<20, history, "", "small.md", strings.Repeat("选", selectionLimit+1))
	if err != nil {
		t.Fatal(err)
	}
	system := msgs[0].Content
	for _, part := range []string{
		"# 笔记库索引（共 3 篇，只有标题和摘要；需要全文时请用户切换到「整个知识库」）\n- big.md",
		"用户当前打开的笔记 `small.md`：\n\n" + strings.Repeat("s", 59) + "\n",
		"用户在笔记中选中的文本：\n<<<\n" + strings.Repeat("选", selectionLimit) + "\n…（已截断）\n>>>",
	} {
		if !strings.Contains(system, part) {
			t.Errorf("note scope lacks %q", part[:min(len(part), 80)])
		}
	}
	if strings.Contains(system, "Chats/c.md") {
		t.Error("note scope index lists chats")
	}

	// "vault": the last message is the search query (the assistant's "yes"
	// matches nothing, so the pool order applies).
	msgs, err = BuildChatMessages(v, 1<<20, history, ScopeVault, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(included(msgs[0].Content), " "); got != "tiny.md small.md big.md" {
		t.Errorf("vault scope included %q", got)
	}
	msgs, err = BuildChatMessages(v, 1<<20, history[:1], ScopeVault, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(included(msgs[0].Content), " "); got != "big.md copilot/p.md tiny.md small.md" {
		t.Errorf("vault scope with query included %q", got)
	}

	if _, err := BuildChatMessages(v, 1<<20, nil, ScopeNote, "missing.md", ""); vault.StatusOf(err) != 404 {
		t.Errorf("missing note: %v", err)
	}
}

func TestConventionsInChat(t *testing.T) {
	long := strings.Repeat("约", conventionsChatLimit+5)
	v, err := vault.New(vaulttest.Strings(t, map[string]string{knowledge.ConventionsFile: long}))
	if err != nil {
		t.Fatal(err)
	}
	if Conventions(v) != long {
		t.Error("Conventions did not return the file")
	}
	msgs, err := BuildChatMessages(v, 100, nil, ScopeNone, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "知识库约定（AGENTS.md）：\n" + strings.Repeat("约", conventionsChatLimit) + "\n…（已截断）"
	if !strings.HasSuffix(msgs[0].Content, want) {
		t.Errorf("conventions not clipped to %d characters", conventionsChatLimit)
	}
	empty, _ := vault.New(t.TempDir())
	if Conventions(empty) != "" {
		t.Error("Conventions of a vault without AGENTS.md")
	}
}
