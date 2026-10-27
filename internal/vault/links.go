package vault

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"marginalia/internal/vault/pystr"
)

// WikiLinkRE matches Obsidian wikilinks and embeds: [[target#heading|alias]]
// and ![[target]]. Groups: 1 "!" (embed), 2 target, 3 "#heading"/"^block",
// 4 "|alias".
var WikiLinkRE = regexp.MustCompile(`(!?)\[\[([^\]\|#\^\n]*)([#\^][^\]\|\n]*)?(\|[^\]\n]*)?\]\]`)

// mdLinkRE matches Markdown links and images [text](target "title"). Python's
// pattern also had a negative lookahead (?!https?:|mailto:|#) before the
// target, which RE2 cannot express; findMDLinks applies it in code.
var mdLinkRE = regexp.MustCompile(`(!?)\[[^\]\n]*\]\(([^)` + pystr.SpaceClass + `]+)(?:[` + pystr.SpaceClass + `]+"[^"]*")?\)`)

// MDLink is one Markdown link found by FindMDLinks.
type MDLink struct {
	Start, End int    // byte offsets of the whole match
	Embed      bool   // the link was an image: ![..](..)
	Target     string // the raw target, e.g. "notes/a.md#part"
}

// FindMDLinks returns the local Markdown links in text, skipping targets that
// start with "http:", "https:", "mailto:" or "#". It scans exactly like
// Python's re.finditer with the lookahead: a rejected candidate does not
// consume the text after its start.
func FindMDLinks(text string) []MDLink {
	var links []MDLink
	for pos := 0; pos <= len(text); {
		loc := mdLinkRE.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		target := text[pos+loc[4] : pos+loc[5]]
		if externalTarget(target) {
			pos = start + 1
			continue
		}
		links = append(links, MDLink{Start: start, End: end, Embed: loc[3] > loc[2], Target: target})
		pos = end
	}
	return links
}

func externalTarget(target string) bool {
	for _, prefix := range []string{"http:", "https:", "mailto:", "#"} {
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return false
}

// Index is a snapshot of the vault's file list used to resolve links. Build
// one with Vault.Index and reuse it for a batch of lookups.
type Index struct {
	files  map[string]bool
	sorted []string
	byName map[string][]string // lower-cased name and note stem -> paths, shallowest first
}

// Index lists every visible file of the vault for link resolution.
func (v *Vault) Index() *Index { return newIndex(v.Files(false)) }

func newIndex(paths []string) *Index {
	ix := &Index{files: make(map[string]bool, len(paths)), byName: map[string][]string{}}
	for _, rel := range paths {
		if ix.files[rel] {
			continue
		}
		ix.files[rel] = true
		ix.sorted = append(ix.sorted, rel)
		name := Name(rel)
		ix.byName[pystr.Lower(name)] = append(ix.byName[pystr.Lower(name)], rel)
		if IsNote(rel) {
			stem := pystr.Lower(Stem(rel))
			ix.byName[stem] = append(ix.byName[stem], rel)
		}
	}
	sort.Strings(ix.sorted)
	for _, list := range ix.byName {
		sort.Slice(list, func(i, j int) bool {
			di, dj := strings.Count(list[i], "/"), strings.Count(list[j], "/")
			if di != dj {
				return di < dj
			}
			return list[i] < list[j]
		})
	}
	return ix
}

// Has reports whether rel is a file in the index.
func (ix *Index) Has(rel string) bool { return ix.files[rel] }

// Files returns all indexed paths in sorted order. The slice must not be
// modified.
func (ix *Index) Files() []string { return ix.sorted }

// Notes returns the indexed Markdown notes in sorted order.
func (ix *Index) Notes() []string {
	var notes []string
	for _, rel := range ix.sorted {
		if IsNote(rel) {
			notes = append(notes, rel)
		}
	}
	return notes
}

// ResolveLink resolves a link target written in the note at source the way
// Obsidian does: the exact vault path, the path plus ".md", the same two
// relative to the source's folder, and finally any file whose name (or note
// stem) matches, preferring the shallowest path. Targets are URL-unquoted
// first. It returns "" when nothing matches.
func (ix *Index) ResolveLink(target, source string) string {
	target = pystr.Strip(target)
	if target == "" {
		return ""
	}
	target = unquote(target)
	sourceDir := path.Dir(source)
	for _, candidate := range []string{target, target + ".md"} {
		if ix.files[candidate] {
			return candidate
		}
		var relative string
		if strings.HasPrefix(candidate, "/") {
			relative = path.Clean(candidate)
		} else {
			relative = path.Clean(sourceDir + "/" + candidate)
		}
		if ix.files[relative] {
			return relative
		}
	}
	if matches := ix.byName[pystr.Lower(Name(target))]; len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// LinksFrom returns the resolved targets of every wikilink in text, followed
// by those of every local Markdown link (anchors stripped), in order of
// appearance and with duplicates kept.
func (ix *Index) LinksFrom(source, text string) []string {
	var found []string
	for _, m := range WikiLinkRE.FindAllStringSubmatch(text, -1) {
		if resolved := ix.ResolveLink(m[2], source); resolved != "" {
			found = append(found, resolved)
		}
	}
	for _, link := range FindMDLinks(text) {
		target, _, _ := strings.Cut(link.Target, "#")
		if resolved := ix.ResolveLink(target, source); resolved != "" {
			found = append(found, resolved)
		}
	}
	return found
}

// Backlink is a note linking to another note, with up to three of the
// linking lines (whitespace-trimmed).
type Backlink struct {
	Path  string   `json:"path"`
	Lines []string `json:"lines"`
}

// Backlinks lists the notes that link to rel, in path order.
func (v *Vault) Backlinks(rel string) ([]Backlink, error) {
	abs, err := v.Resolve(rel)
	if err != nil {
		return nil, err
	}
	target, err := v.Rel(abs)
	if err != nil {
		return nil, err
	}
	ix := v.Index()
	results := []Backlink{}
	for _, source := range ix.Notes() {
		if source == target {
			continue
		}
		text, err := v.ReadText(source)
		if err != nil {
			continue
		}
		var lines []string
		for _, line := range pystr.SplitLines(text) {
			if !strings.Contains(line, "[[") && !strings.Contains(line, "](") {
				continue
			}
			for _, link := range ix.LinksFrom(source, line) {
				if link == target {
					lines = append(lines, pystr.Strip(line))
					break
				}
			}
		}
		if len(lines) > 0 {
			if len(lines) > 3 {
				lines = lines[:3]
			}
			results = append(results, Backlink{Path: source, Lines: lines})
		}
	}
	return results, nil
}

// MoveResult reports a move: the new path and the notes whose wikilinks were
// rewritten to follow it.
type MoveResult struct {
	Path           string   `json:"path"`
	UpdatedLinksIn []string `json:"updated_links_in"`
}

// Move renames a file or folder. Missing sources yield NotFound("file not
// found") and existing destinations Conflict("destination already exists").
//
// When updateLinks is set and a file is moved, every wikilink that resolved
// to the old path is rewritten to the shortest unambiguous form of the new
// one ("[[Bee]]", or the full path without ".md" when the name is not
// unique), keeping embeds, headings and aliases. Notes are rewritten
// byte-for-byte apart from the links, so CRLF line endings survive. Markdown
// links are not rewritten (same as the Python backend).
func (v *Vault) Move(src, dst string, updateLinks bool) (*MoveResult, error) {
	source, err := v.Resolve(src)
	if err != nil {
		return nil, err
	}
	destination, err := v.Resolve(dst)
	if err != nil {
		return nil, err
	}
	if !exists(source) {
		return nil, NotFound("file not found")
	}
	if exists(destination) {
		return nil, Conflict("destination already exists")
	}
	oldRel, err := v.Rel(source)
	if err != nil {
		return nil, err
	}
	newRel, err := v.Clean(dst)
	if err != nil {
		return nil, err
	}
	if source == v.root {
		return nil, Invalid("cannot move the vault root")
	}
	if strings.HasPrefix(destination, source+string(filepath.Separator)) {
		return nil, Invalid("cannot move a folder into itself")
	}
	touched := []string{}
	if updateLinks && isFile(source) {
		ix := v.Index()
		touched, err = v.rewriteLinks(ix, oldRel, newRel)
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o777); err != nil {
		return nil, err
	}
	if err := os.Rename(source, destination); err != nil {
		return nil, err
	}
	return &MoveResult{Path: newRel, UpdatedLinksIn: touched}, nil
}

// rewriteLinks points every wikilink resolving to oldRel at newRel and
// returns the notes it changed.
func (v *Vault) rewriteLinks(ix *Index, oldRel, newRel string) ([]string, error) {
	renamed := make([]string, 0, len(ix.sorted))
	for _, rel := range ix.sorted {
		if rel != oldRel {
			renamed = append(renamed, rel)
		}
	}
	after := newIndex(append(renamed, newRel))
	short := Name(newRel)
	if IsNote(newRel) {
		short = Stem(newRel)
	}
	replacement := short
	if len(after.byName[pystr.Lower(short)]) != 1 {
		replacement = newRel
		if IsNote(newRel) {
			replacement = newRel[:len(newRel)-3] // Python's new_rel[:-3], even for .markdown
		}
	}
	touched := []string{}
	for _, note := range ix.Notes() {
		if note == oldRel {
			continue
		}
		abs := filepath.Join(v.root, filepath.FromSlash(note))
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		// Decode without newline translation so CRLF files keep their endings.
		text := pystr.Decode(data)
		updated := replaceWikiLinks(text, func(m []string) string {
			if ix.ResolveLink(m[2], note) != oldRel {
				return m[0]
			}
			return m[1] + "[[" + replacement + m[3] + m[4] + "]]"
		})
		if updated == text {
			continue
		}
		target, err := v.confine(realpath(abs))
		if err != nil {
			continue
		}
		if err := atomicWrite(target, []byte(updated)); err != nil {
			return touched, err
		}
		touched = append(touched, note)
	}
	return touched, nil
}

// replaceWikiLinks is WIKILINK_RE.sub(fn, text); fn receives the match and
// its groups ("" for groups that did not participate).
func replaceWikiLinks(text string, fn func(groups []string) string) string {
	matches := WikiLinkRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, loc := range matches {
		groups := make([]string, len(loc)/2)
		for g := range groups {
			if loc[2*g] >= 0 {
				groups[g] = text[loc[2*g]:loc[2*g+1]]
			}
		}
		b.WriteString(text[last:loc[0]])
		b.WriteString(fn(groups))
		last = loc[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// unquote is urllib.parse.unquote: %XX escapes inside runs of ASCII text are
// decoded as UTF-8 (invalid sequences become U+FFFD), malformed escapes are
// kept literally and non-ASCII characters pass through.
func unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] >= 0x80 {
			j := i
			for j < len(s) && s[j] >= 0x80 {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		j := i
		for j < len(s) && s[j] < 0x80 {
			j++
		}
		b.WriteString(pystr.Decode(unquoteBytes(s[i:j])))
		i = j
	}
	return b.String()
}

// unquoteBytes is urllib.parse.unquote_to_bytes for an ASCII string.
func unquoteBytes(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			out = append(out, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return out
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
