// Package chats stores AI conversations as Markdown files in the vault's
// Chats/ folder. It is a byte-compatible port of the Python backend's
// chats.py.
//
// Chats are raw material that the AI later distills into topic notes. Each
// message is introduced by an HTML comment marker so the file reads well in
// Obsidian and round-trips losslessly:
//
//	---
//	type: chat
//	title: PPO 是什么
//	status: inbox
//	…
//	---
//
//	<!-- chat:user -->
//	### 🧑 我
//
//	什么是 PPO?
//
//	<!-- chat:assistant -->
//	### 🤖 AI
//
//	…
package chats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"marginalia/internal/knowledge"
	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// Roles and the headings written under their markers.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	UserHeading      = "### 🧑 我"
	AssistantHeading = "### 🤖 AI"
)

// Status values of a chat's frontmatter.
const (
	StatusInbox     = "inbox"     // not yet distilled
	StatusDistilled = "distilled" // turned into notes by the organize agent
)

var headings = map[string]string{RoleUser: UserHeading, RoleAssistant: AssistantHeading}

var markerRE = regexp.MustCompile(`(?m)^<!-- chat:(user|assistant) -->[ \t]*\n(?:### .*\n)?`)

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// UnmarshalJSON accepts any JSON content and converts it the way Python's
// str() did (a missing content is "", null is "None", a list prints as a
// Python list), so odd client payloads behave exactly as before.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    any             `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	role, _ := raw.Role.(string)
	m.Role = role
	m.Content = ""
	if len(raw.Content) == 0 {
		return nil // no "content" key
	}
	v, err := pyyaml.FromJSON(raw.Content)
	if err != nil {
		return err
	}
	m.Content = pyyaml.Str(v)
	return nil
}

// Slug turns a title into a file-name-safe fragment: path, wikilink and
// control characters become spaces, whitespace collapses, surrounding spaces
// and dots are removed and the result is cut to 60 characters ("对话" when
// nothing is left).
func Slug(title string) string {
	title = slugUnsafe.ReplaceAllString(title, " ")
	title = slugSpace.ReplaceAllString(title, " ")
	title = strings.Trim(title, " .")
	if title = pystr.Head(title, 60); title == "" {
		return "对话"
	}
	return title
}

var (
	slugUnsafe = regexp.MustCompile(`[\x00-\x1f/\\:*?"<>|#^\[\]]`)
	slugSpace  = regexp.MustCompile(`[` + pystr.SpaceClass + `]+`)
)

// Render writes meta and messages in the chat file format. Messages with a
// role other than user/assistant are skipped and contents are trimmed.
func Render(meta *knowledge.Meta, messages []Message) (string, error) {
	var parts []string
	for _, msg := range messages {
		heading, ok := headings[msg.Role]
		if !ok {
			continue
		}
		content := pystr.Strip(msg.Content)
		parts = append(parts, fmt.Sprintf("<!-- chat:%s -->\n%s\n\n%s\n", msg.Role, heading, content))
	}
	return knowledge.JoinFrontmatter(meta, strings.Join(parts, "\n"))
}

// Parse splits a chat file into its frontmatter and messages. The returned
// slice is never nil.
func Parse(text string) (*knowledge.Meta, []Message) {
	meta, body := knowledge.SplitFrontmatter(text)
	messages := []Message{}
	matches := markerRE.FindAllStringSubmatchIndex(body, -1)
	for i, m := range matches {
		end := len(body)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		messages = append(messages, Message{Role: body[m[2]:m[3]], Content: pystr.Strip(body[m[1]:end])})
	}
	return meta, messages
}

// Summary is a row of GET /api/chats.
type Summary struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Status  string `json:"status"`  // "inbox" when unset
	Updated string `json:"updated"` // updated, else created, else ""
}

// List returns every chat in Chats/, most recently updated first (ties keep
// path order).
func List(v *vault.Vault) []Summary {
	list := []Summary{}
	for _, rel := range v.Files(true) {
		if !strings.HasPrefix(rel, knowledge.ChatsDir+"/") {
			continue
		}
		text, err := v.ReadText(rel)
		if err != nil {
			continue
		}
		meta, _ := knowledge.SplitFrontmatter(text)
		list = append(list, Summary{
			Path:    rel,
			Title:   pyyaml.Str(pyyaml.Or(meta.Value("title"), vault.Stem(rel))),
			Status:  pyyaml.Str(pyyaml.Or(meta.Value("status"), StatusInbox)),
			Updated: pyyaml.Str(pyyaml.Or(pyyaml.Or(meta.Value("updated"), meta.Value("created")), "")),
		})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Updated > list[j].Updated })
	return list
}

// Loaded is a chat as returned by GET /api/chats/load.
type Loaded struct {
	Path     string          `json:"path"`
	ETag     string          `json:"etag"`
	Meta     *knowledge.Meta `json:"meta"`
	Messages []Message       `json:"messages"`
}

// Load reads and parses the chat at rel.
func Load(v *vault.Vault, rel string) (*Loaded, error) {
	note, err := v.ReadNote(rel)
	if err != nil {
		return nil, err
	}
	meta, messages := Parse(note.Content)
	return &Loaded{Path: note.Path, ETag: note.ETag, Meta: meta, Messages: messages}, nil
}

// Saved is the result of Save: the vault.WriteResult fields plus the
// frontmatter that was written.
type Saved struct {
	Path  string          `json:"path"`
	ETag  string          `json:"etag"`
	Mtime float64         `json:"mtime"`
	Meta  *knowledge.Meta `json:"meta"`
}

// clock is the time source; tests replace it.
var clock = time.Now

// metaOrder lists the keys Save writes first, in this order.
var metaOrder = []string{"type", "title", "status", "created", "updated", "model"}

// Save writes a conversation. With rel == "" a new file
// Chats/YYYY-MM-DD HHMM <slug>.md is created; otherwise the existing chat's
// frontmatter is kept (status, created and any extra keys) and baseETag
// guards against concurrent edits as in vault.WriteNote. rel must lie in
// Chats/ (Invalid("chats must live in the Chats folder")).
//
// Unlike the Python backend, a new chat never overwrites an existing file
// that happens to get the same name (same title within the same minute): a
// numeric suffix (" 2", " 3", …) is added instead.
func Save(v *vault.Vault, rel, title, model string, messages []Message, baseETag *string) (*Saved, error) {
	now := clock()
	stamp := now.Format("2006-01-02T15:04:05-07:00")
	old := pyyaml.NewMap()
	if rel != "" {
		note, err := v.ReadNote(rel)
		switch {
		case err == nil:
			old, _ = Parse(note.Content)
		case !errors.Is(err, vault.ErrNotFound):
			return nil, err
		}
	} else {
		var err error
		if rel, err = newChatPath(v, now, title); err != nil {
			return nil, err
		}
		baseETag = nil
	}
	if !strings.HasPrefix(rel, knowledge.ChatsDir+"/") {
		return nil, vault.Invalid("chats must live in the Chats folder")
	}
	meta := pyyaml.NewMap()
	meta.Set("type", "chat")
	meta.Set("title", pyyaml.Or(pyyaml.Or(title, old.Value("title")), "对话"))
	meta.Set("status", valueOr(old, "status", StatusInbox))
	meta.Set("created", valueOr(old, "created", stamp))
	meta.Set("updated", stamp)
	meta.Set("model", model)
	old.Range(func(key, value any) bool {
		if s, ok := key.(string); ok && contains(metaOrder, s) {
			return true
		}
		meta.Set(key, value)
		return true
	})
	text, err := Render(meta, messages)
	if err != nil {
		return nil, err
	}
	result, err := v.WriteNote(rel, text, baseETag)
	if err != nil {
		return nil, err
	}
	return &Saved{Path: result.Path, ETag: result.ETag, Mtime: result.Mtime, Meta: meta}, nil
}

// newChatPath picks Chats/<date> <time> <slug>.md, adding " 2", " 3", … when
// the name is taken.
func newChatPath(v *vault.Vault, now time.Time, title string) (string, error) {
	base := fmt.Sprintf("%s/%s %s", knowledge.ChatsDir, now.Format("2006-01-02 1504"), Slug(title))
	for n := 1; n < 1000; n++ {
		rel := base + ".md"
		if n > 1 {
			rel = fmt.Sprintf("%s %d.md", base, n)
		}
		abs, err := v.Resolve(rel)
		if err != nil {
			return "", err
		}
		if !pathExists(abs) {
			return rel, nil
		}
	}
	return "", vault.Conflict("too many chats with the same name")
}

func pathExists(abs string) bool {
	_, err := os.Lstat(abs)
	return err == nil
}

// valueOr is dict.get(key, fallback).
func valueOr(m *knowledge.Meta, key string, fallback any) any {
	if v, ok := m.Get(key); ok {
		return v
	}
	return fallback
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// SetStatus sets the chat's status and, when distilledInto names notes,
// appends "[[note]]" links (without .md) to its distilled_into list, skipping
// ones already present. The write is guarded by the chat's current ETag. It
// returns rel unchanged.
func SetStatus(v *vault.Vault, rel, status string, distilledInto []string) (string, error) {
	note, err := v.ReadNote(rel)
	if err != nil {
		return "", err
	}
	meta, messages := Parse(note.Content)
	meta.Set("status", status)
	if len(distilledInto) > 0 {
		existing, err := iterStrings(pyyaml.Or(meta.Value("distilled_into"), []any{}))
		if err != nil {
			return "", err
		}
		list := make([]any, 0, len(existing)+len(distilledInto))
		for _, s := range existing {
			list = append(list, s)
		}
		for _, p := range distilledInto {
			// The duplicate check strips three characters unconditionally,
			// exactly like the Python code did.
			if contains(existing, "[["+dropLast(p, 3)+"]]") {
				continue
			}
			name := p
			if strings.HasSuffix(p, ".md") {
				name = p[:len(p)-3]
			}
			list = append(list, "[["+name+"]]")
		}
		meta.Set("distilled_into", list)
	}
	text, err := Render(meta, messages)
	if err != nil {
		return "", err
	}
	etag := note.ETag
	if _, err := v.WriteNote(rel, text, &etag); err != nil {
		return "", err
	}
	return rel, nil
}

// iterStrings is [str(x) for x in value] for the iterables Python accepted.
func iterStrings(value any) ([]string, error) {
	switch v := value.(type) {
	case []any:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = pyyaml.Str(item)
		}
		return out, nil
	case string:
		var out []string
		for _, r := range v {
			out = append(out, string(r))
		}
		return out, nil
	case *pyyaml.Map:
		var out []string
		v.Range(func(key, _ any) bool {
			out = append(out, pyyaml.Str(key))
			return true
		})
		return out, nil
	}
	return nil, vault.Invalid(fmt.Sprintf("distilled_into is not a list: %s", pyyaml.Repr(value)))
}

// dropLast is s[:-n] counted in code points.
func dropLast(s string, n int) string {
	return pystr.Head(s, max(0, pystr.Len(s)-n))
}
