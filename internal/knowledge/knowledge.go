// Package knowledge builds the vault-level views the AI features and the
// graph UI use: YAML frontmatter, the note index (title, summary, tags,
// links per note) and the link graph. It is a behaviour-identical port of the
// Python backend's knowledge.py.
package knowledge

import (
	"regexp"
	"sort"
	"strings"

	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// Well-known vault locations (see the AGENTS.md conventions file).
const (
	ConventionsFile = "AGENTS.md" // vault conventions the AI must follow
	ChatsDir        = "Chats"     // saved AI conversations (the inbox)
	IndexFile       = "index.md"  // the knowledge map
)

// NonKnowledgePrefixes are folders of plugin data that live in the vault but
// are not knowledge; they are left out of the index and the graph.
var NonKnowledgePrefixes = []string{"copilot/", "_templates/"}

// Meta is a note's frontmatter: an ordered YAML mapping with Python
// semantics (see package pyyaml).
type Meta = pyyaml.Map

var (
	frontmatterRE = regexp.MustCompile(`\A---[ \t]*\n((?s:.*?))\n---[ \t]*(?:\n|\z)`)
	headingRE     = regexp.MustCompile(`(?m)^#{1,6}[` + pystr.SpaceClass + `]+(.+?)[` + pystr.SpaceClass + `]*#*[` + pystr.SpaceClass + `]*$`)
	inlineTagRE   = regexp.MustCompile(`(?:^|[` + pystr.SpaceClass + `])#([` + pystr.WordClass + `一-鿿぀-ヿ/-]+)`)
	tagSplitRE    = regexp.MustCompile(`[,，]`)
)

// SplitFrontmatter separates a leading "---" YAML block from the body. When
// there is no block, or it is not a valid YAML mapping, the metadata is empty
// and (for a missing block) the body is the whole text. The returned map is
// never nil.
func SplitFrontmatter(text string) (*Meta, string) {
	loc := frontmatterRE.FindStringSubmatchIndex(text)
	if loc == nil {
		return pyyaml.NewMap(), text
	}
	body := text[loc[1]:]
	data, err := pyyaml.Load(text[loc[2]:loc[3]])
	if err != nil {
		return pyyaml.NewMap(), body
	}
	meta, ok := data.(*pyyaml.Map)
	if !ok || meta == nil {
		return pyyaml.NewMap(), body
	}
	return meta, body
}

// JoinFrontmatter renders meta as a YAML block (PyYAML formatting, keys in
// insertion order) in front of body, separated by a blank line. With empty
// meta it returns body unchanged.
func JoinFrontmatter(meta *Meta, body string) (string, error) {
	if meta.Len() == 0 {
		return body, nil
	}
	header, err := pyyaml.Dump(meta)
	if err != nil {
		return "", err
	}
	return "---\n" + pystr.Strip(header) + "\n---\n\n" + pystr.LStrip(body), nil
}

// Note summarises one note for the AI index, the graph and GET /api/index.
type Note struct {
	Path    string   `json:"path"`
	Title   string   `json:"title"`   // frontmatter title, first heading, or file stem
	Summary string   `json:"summary"` // frontmatter summary/description, or the first prose line (120 chars)
	Tags    []string `json:"tags"`    // frontmatter tags plus inline #tags, sorted
	Type    string   `json:"type"`    // frontmatter type, else "chat" under Chats/, else "note"
	Status  string   `json:"status"`
	Updated string   `json:"updated"`
	Chars   int      `json:"chars"` // length of the text in characters
	Links   []string `json:"links"` // resolved link targets, sorted and unique
}

// summarySkip are line prefixes that never make a summary.
var summarySkip = []string{"#", "---", "!", "|", "```", "$$", ">"}

// NoteInfo extracts a Note from the text of rel. ix resolves the note's
// links; nil builds a fresh index.
func NoteInfo(v *vault.Vault, rel, text string, ix *vault.Index) Note {
	if ix == nil {
		ix = v.Index()
	}
	meta, body := SplitFrontmatter(text)

	title := meta.Value("title")
	if !pyyaml.Truthy(title) {
		if m := headingRE.FindStringSubmatch(body); m != nil {
			title = m[1]
		} else {
			title = vault.Stem(rel)
		}
	}

	tagSet := map[string]bool{}
	for _, tag := range asList(meta.Value("tags")) {
		tagSet[strings.TrimLeft(tag, "#")] = true
	}
	for _, m := range inlineTagRE.FindAllStringSubmatch(body, -1) {
		tagSet[m[1]] = true
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	summary := pystr.Strip(pyyaml.Str(pyyaml.Or(pyyaml.Or(meta.Value("summary"), meta.Value("description")), "")))
	if summary == "" {
		for _, line := range pystr.SplitLines(body) {
			line = pystr.Strip(line)
			if line != "" && !hasAnyPrefix(line, summarySkip) {
				summary = pystr.Head(line, 120)
				break
			}
		}
	}

	defaultType := "note"
	if strings.HasPrefix(rel, ChatsDir+"/") {
		defaultType = "chat"
	}
	links := uniqueSorted(ix.LinksFrom(rel, body))
	return Note{
		Path:    rel,
		Title:   pyyaml.Str(title),
		Summary: summary,
		Tags:    tags,
		Type:    pyyaml.Str(pyyaml.Or(meta.Value("type"), defaultType)),
		Status:  pyyaml.Str(pyyaml.Or(meta.Value("status"), "")),
		Updated: pyyaml.Str(pyyaml.Or(pyyaml.Or(meta.Value("updated"), meta.Value("updated_at")), "")),
		Chars:   pystr.Len(text),
		Links:   links,
	}
}

// asList is the backend's _as_list: a string is split on commas (ASCII or
// full-width), a list yields its non-blank items as strings, None yields
// nothing and anything else its str().
func asList(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		var out []string
		for _, part := range tagSplitRE.Split(v, -1) {
			if part = pystr.Strip(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range v {
			if s := pystr.Strip(pyyaml.Str(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{pyyaml.Str(value)}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func uniqueSorted(items []string) []string {
	set := map[string]bool{}
	out := []string{}
	for _, item := range items {
		if !set[item] {
			set[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

// IsKnowledge reports whether rel is a note that belongs in the index: a
// Markdown note outside NonKnowledgePrefixes.
func IsKnowledge(rel string) bool {
	return vault.IsNote(rel) && !hasAnyPrefix(rel, NonKnowledgePrefixes)
}

// VaultIndex summarises every knowledge note (chats included), in path order.
func VaultIndex(v *vault.Vault) []Note {
	ix := v.Index()
	notes := []Note{}
	for _, rel := range ix.Files() {
		if !IsKnowledge(rel) {
			continue
		}
		text, err := v.ReadText(rel)
		if err != nil {
			continue
		}
		notes = append(notes, NoteInfo(v, rel, text, ix))
	}
	return notes
}

// GraphNode is a note in the link graph.
type GraphNode struct {
	ID    string   `json:"id"` // vault path
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
	Type  string   `json:"type"`
	Size  int      `json:"size"` // characters
}

// GraphEdge is a link between two notes of the graph.
type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// GraphData is the response of GET /api/graph.
type GraphData struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// Graph returns the link graph of the knowledge notes. Chats (type "chat")
// are left out unless includeChats is set; edges only connect notes in the
// graph and never loop.
func Graph(v *vault.Vault, includeChats bool) GraphData {
	return GraphFromIndex(VaultIndex(v), includeChats)
}

// GraphFromIndex builds the graph from an existing VaultIndex result.
func GraphFromIndex(notes []Note, includeChats bool) GraphData {
	g := GraphData{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	known := map[string]bool{}
	var kept []Note
	for _, n := range notes {
		if !includeChats && n.Type == "chat" {
			continue
		}
		kept = append(kept, n)
		known[n.Path] = true
		g.Nodes = append(g.Nodes, GraphNode{ID: n.Path, Title: n.Title, Tags: n.Tags, Type: n.Type, Size: n.Chars})
	}
	for _, n := range kept {
		for _, target := range n.Links {
			if known[target] && target != n.Path {
				g.Edges = append(g.Edges, GraphEdge{Source: n.Path, Target: target})
			}
		}
	}
	return g
}
