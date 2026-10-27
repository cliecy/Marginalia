package vault

import (
	"sort"
	"strings"

	"marginalia/internal/vault/pystr"
)

// Snippet is a matching line of a search hit.
type Snippet struct {
	Line int    `json:"line"` // 1-based line number
	Text string `json:"text"` // up to 180 characters around the first match, "…"-prefixed when cut
}

// SearchHit is one note matching a search.
type SearchHit struct {
	Path     string    `json:"path"`
	Snippets []Snippet `json:"snippets"` // at most three
}

// DefaultSearchLimit is the result limit of GET /api/search.
const DefaultSearchLimit = 50

// Search finds notes containing every whitespace-separated term of query
// (case-insensitive; a term may also match the note's path). Notes are ranked
// by 100 points per term found in the file name plus one point per
// occurrence in the text; ties keep vault walk order. limit follows Python
// slice semantics (hits[:limit]).
func (v *Vault) Search(query string, limit int) []SearchHit {
	terms := pystr.Fields(query)
	for i, term := range terms {
		terms[i] = pystr.Lower(term)
	}
	hits := []SearchHit{}
	if len(terms) == 0 {
		return hits
	}
	type scored struct {
		score int
		hit   SearchHit
	}
	var found []scored
	for _, rel := range v.Files(true) {
		text, err := v.ReadText(rel)
		if err != nil {
			continue
		}
		lowerText, lowerPath := pystr.Lower(text), pystr.Lower(rel)
		matchesAll := true
		for _, term := range terms {
			if !strings.Contains(lowerText, term) && !strings.Contains(lowerPath, term) {
				matchesAll = false
				break
			}
		}
		if !matchesAll {
			continue
		}
		titleScore, count := 0, 0
		stem := Stem(lowerPath)
		for _, term := range terms {
			if strings.Contains(stem, term) {
				titleScore++
			}
			count += strings.Count(lowerText, term)
		}
		found = append(found, scored{titleScore*100 + count, SearchHit{Path: rel, Snippets: snippets(text, terms)}})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].score > found[j].score })
	end := len(found)
	switch {
	case limit < 0:
		end = max(0, len(found)+limit)
	case limit < end:
		end = limit
	}
	for _, s := range found[:end] {
		hits = append(hits, s.hit)
	}
	return hits
}

// snippets returns up to three matching lines, each cut to 180 characters
// starting 60 characters before the earliest term on the line.
func snippets(text string, terms []string) []Snippet {
	out := []Snippet{}
	for number, line := range pystr.SplitLines(text) {
		lowerLine := pystr.Lower(line)
		position := -1
		for _, term := range terms {
			if i := pystr.Index(lowerLine, term); i >= 0 && (position < 0 || i < position) {
				position = i
			}
		}
		if position < 0 {
			continue
		}
		start := max(0, position-60)
		snippet := pystr.Strip(pystr.Slice(line, start, start+180))
		if start > 0 {
			snippet = "…" + snippet
		}
		out = append(out, Snippet{Line: number + 1, Text: snippet})
		if len(out) >= 3 {
			break
		}
	}
	return out
}
