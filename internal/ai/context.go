package ai

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"marginalia/internal/knowledge"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// Chat context scopes (the "scope" of POST /api/ai/chat).
const (
	ScopeNone  = "none"  // no vault context
	ScopeNote  = "note"  // the open note in full plus the index (default)
	ScopeVault = "vault" // the index plus as many full notes as the budget allows
)

// Limits used when assembling prompts, in characters (code points).
const (
	conventionsChatLimit  = 6000  // AGENTS.md in chat prompts
	conventionsAgentLimit = 8000  // AGENTS.md in agent prompts
	selectionLimit        = 30000 // editor selection
	summaryListingLimit   = 100   // summary per index line
	linksListingLimit     = 12    // links per index line
	searchContextLimit    = 20    // search hits ranked into the vault context
)

// now is the clock behind "今天是 …" (replaced in tests).
var now = time.Now

// today is date.today().isoformat() in local time.
func today() string { return now().Format(time.DateOnly) }

// clip is Python's _clip: text cut to limit characters with a marker.
func clip(text string, limit int) string {
	if pystr.Len(text) <= limit {
		return text
	}
	return pystr.Head(text, limit) + "\n…（已截断）"
}

// Conventions returns the vault's conventions file (AGENTS.md), or "" when
// it is missing or unreadable.
func Conventions(v *vault.Vault) string {
	note, err := v.ReadNote(knowledge.ConventionsFile)
	if err != nil {
		logSkipped(knowledge.ConventionsFile, err)
		return ""
	}
	return note.Content
}

// IndexListing renders the note index for prompts, one line per note:
// "- <path> [<status>]：<title> — <summary> #<tag> #<tag> → <link>, <link>",
// where the status, tags and links parts are left out when empty, the
// summary is cut to 100 characters and at most 12 links are shown, without
// their ".md" suffix.
func IndexListing(notes []knowledge.Note) string {
	lines := make([]string, 0, len(notes))
	for _, n := range notes {
		var line strings.Builder
		line.WriteString("- " + n.Path)
		if n.Status != "" {
			line.WriteString(" [" + n.Status + "]")
		}
		line.WriteString("：" + n.Title + " — " + pystr.Head(n.Summary, summaryListingLimit))
		if len(n.Tags) > 0 {
			line.WriteString(" #" + strings.Join(n.Tags, " #"))
		}
		if len(n.Links) > 0 {
			links := n.Links[:min(len(n.Links), linksListingLimit)]
			shown := make([]string, len(links))
			for i, l := range links {
				shown[i] = strings.TrimSuffix(l, ".md")
			}
			line.WriteString(" → " + strings.Join(shown, ", "))
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

// VaultContext is the vault as prompt context: the index of every note,
// then the full text of as many notes as fit into budget characters, in this
// order of preference: the focus paths, notes matching query (best search
// hits first), then all remaining notes from the shortest to the longest.
// Chats are only included in full when includeChats is set. A note that does
// not fit is skipped and the next one tried; the index itself is always
// included. Unreadable notes are skipped.
func VaultContext(v *vault.Vault, budget int, focus []string, query string, includeChats bool) string {
	notes := knowledge.VaultIndex(v)
	var out strings.Builder
	header := fmt.Sprintf("# 笔记库索引（共 %d 篇）\n%s", len(notes), IndexListing(notes))
	out.WriteString(header)
	used := pystr.Len(header)

	ranked := append([]string{}, focus...)
	if query != "" {
		for _, hit := range v.Search(query, searchContextLimit) {
			ranked = append(ranked, hit.Path)
		}
	}
	pool := make([]knowledge.Note, 0, len(notes))
	for _, n := range notes {
		if includeChats || n.Type != "chat" {
			pool = append(pool, n)
		}
	}
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].Chars < pool[j].Chars })
	for _, n := range pool {
		ranked = append(ranked, n.Path)
	}

	seen := make(map[string]bool, len(ranked))
	for _, rel := range ranked {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if !vault.IsNote(rel) {
			continue
		}
		note, err := v.ReadNote(rel)
		if err != nil {
			logSkipped(rel, err)
			continue
		}
		block := "\n\n===== 笔记：" + rel + " =====\n" + note.Content
		size := pystr.Len(block)
		if used+size > budget {
			continue
		}
		out.WriteString(block)
		used += size
	}
	return out.String()
}

// BuildChatMessages assembles the messages of a chat request: one system
// message (BaseSystem, today's date, the vault conventions and the context
// selected by scope) followed by the user and assistant messages of history
// (other roles are dropped). budget is the context size in characters
// (config AISettings.VaultCharBudget).
//
// Scopes: ScopeNone adds no context. ScopeNote (also used for "") adds the
// index (titles and summaries, chats excluded) and the full text of path; a
// ScopeNote request without a path, and ScopeVault, add VaultContext ranked
// by path and by the last message. Unknown scopes add no context. selection,
// when set, is appended as the text the user selected.
//
// The only errors are those of reading path under ScopeNote (vault errors,
// e.g. 404 "file not found").
func BuildChatMessages(v *vault.Vault, budget int, history []Message, scope, path, selection string) ([]Message, error) {
	if scope == "" {
		scope = ScopeNote
	}
	system := []string{BaseSystem, "今天是 " + today() + "。"}
	if rules := Conventions(v); rules != "" {
		system = append(system, fmt.Sprintf("知识库约定（%s）：\n%s", knowledge.ConventionsFile, clip(rules, conventionsChatLimit)))
	}
	switch {
	case scope == ScopeVault || (scope == ScopeNote && path == ""):
		query := ""
		if len(history) > 0 {
			query = history[len(history)-1].Content
		}
		var focus []string
		if path != "" {
			focus = []string{path}
		}
		system = append(system, VaultContext(v, budget, focus, query, false))
	case scope == ScopeNote:
		note, err := v.ReadNote(path)
		if err != nil {
			return nil, err
		}
		var notes []knowledge.Note
		for _, n := range knowledge.VaultIndex(v) {
			if n.Type != "chat" {
				notes = append(notes, n)
			}
		}
		system = append(system,
			fmt.Sprintf("# 笔记库索引（共 %d 篇，只有标题和摘要；需要全文时请用户切换到「整个知识库」）\n%s", len(notes), IndexListing(notes)),
			fmt.Sprintf("用户当前打开的笔记 `%s`：\n\n%s", path, clip(note.Content, budget)))
	}
	if selection != "" {
		system = append(system, fmt.Sprintf("用户在笔记中选中的文本：\n<<<\n%s\n>>>", clip(selection, selectionLimit)))
	}

	messages := []Message{{Role: RoleSystem, Content: strings.Join(system, "\n\n")}}
	for _, m := range history {
		if m.Role == RoleUser || m.Role == RoleAssistant {
			messages = append(messages, Message{Role: m.Role, Content: m.Content})
		}
	}
	return messages, nil
}

// logSkipped records a note left out of a prompt. Missing and invalid paths
// are expected (focus paths come from clients, plans from the model); other
// failures are worth a warning.
func logSkipped(rel string, err error) {
	if vault.StatusOf(err) < 500 {
		slog.Debug("ai: note skipped", "path", rel, "err", err)
		return
	}
	slog.Warn("ai: cannot read note", "path", rel, "err", err)
}
