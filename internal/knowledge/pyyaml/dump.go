package pyyaml

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"marginalia/internal/vault/pystr"
)

// Dump serialises v exactly like
//
//	yaml.safe_dump(v, allow_unicode=True, sort_keys=False, default_flow_style=False)
//
// (block style, two-space indentation, 80-column folding of long scalars,
// PyYAML's quoting rules). Values that are not part of the supported set
// yield an error, like PyYAML's RepresenterError.
func Dump(v any) (string, error) {
	root, err := represent(v, 0)
	if err != nil {
		return "", err
	}
	e := newEmitter()
	e.emitDocument(root)
	return e.out.String(), nil
}

// node is PyYAML's representation graph (ScalarNode, SequenceNode,
// MappingNode) with the implicit flags the serializer computes.
type node struct {
	kind  nodeKind
	tag   string
	value []rune // scalars
	items []*node
	pairs [][2]*node
	// implicit is the serializer's (plain-implicit, quoted-implicit) pair.
	implicit [2]bool
}

type nodeKind int

const (
	scalarNode nodeKind = iota
	sequenceNode
	mappingNode
)

func scalar(tag, value string) *node {
	return &node{
		kind:     scalarNode,
		tag:      tag,
		value:    []rune(value),
		implicit: [2]bool{tag == resolvePlain(value), tag == tagStr},
	}
}

// represent is SafeRepresenter.represent_data.
func represent(v any, depth int) (*node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("pyyaml: value is nested too deeply")
	}
	switch v := normalize(v).(type) {
	case nil:
		return scalar(tagNull, "null"), nil
	case string:
		return scalar(tagStr, v), nil
	case bool:
		if v {
			return scalar(tagBool, "true"), nil
		}
		return scalar(tagBool, "false"), nil
	case int64:
		return scalar(tagInt, strconv.FormatInt(v, 10)), nil
	case *big.Int:
		return scalar(tagInt, v.String()), nil
	case float64:
		return scalar(tagFloat, representFloat(v)), nil
	case Date:
		return scalar(tagTimestamp, v.String()), nil
	case DateTime:
		return scalar(tagTimestamp, v.Isoformat(" ")), nil
	case []any:
		n := &node{kind: sequenceNode, tag: tagSeq}
		for _, item := range v {
			child, err := represent(item, depth+1)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, child)
		}
		return n, nil
	case *Map:
		n := &node{kind: mappingNode, tag: tagMap}
		var err error
		v.Range(func(key, value any) bool {
			var k, val *node
			if k, err = represent(key, depth+1); err != nil {
				return false
			}
			if val, err = represent(value, depth+1); err != nil {
				return false
			}
			n.pairs = append(n.pairs, [2]*node{k, val})
			return true
		})
		if err != nil {
			return nil, err
		}
		return n, nil
	}
	return nil, fmt.Errorf("pyyaml: cannot represent an object of type %T", v)
}

// representFloat is SafeRepresenter.represent_float.
func representFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return ".nan"
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	}
	value := strings.ToLower(pystr.FloatRepr(f))
	if !strings.Contains(value, ".") && strings.Contains(value, "e") {
		value = strings.Replace(value, "e", ".0e", 1)
	}
	return value
}

// scalarAnalysis is PyYAML's ScalarAnalysis.
type scalarAnalysis struct {
	empty, multiline                     bool
	allowFlowPlain, allowBlockPlain      bool
	allowSingleQuoted, allowDoubleQuoted bool
	allowBlock                           bool
}

// emitter is a port of PyYAML's Emitter for the event stream the safe
// representer produces (no anchors, no explicit document markers).
type emitter struct {
	out strings.Builder

	indents []int
	indent  int // -1 stands for Python's None

	flowLevel int

	rootContext, sequenceContext, mappingContext, simpleKeyContext bool

	column     int
	whitespace bool
	indention  bool
	openEnded  bool

	allowUnicode bool
	bestIndent   int
	bestWidth    int
}

func newEmitter() *emitter {
	return &emitter{indent: -1, whitespace: true, indention: true, allowUnicode: true, bestIndent: 2, bestWidth: 80}
}

func (e *emitter) emitDocument(root *node) {
	// The first document is implicit (no "---").
	e.expectNode(root, true, false, false, false)
	// expect_document_end
	e.writeIndent()
	// StreamEndEvent in expect_document_start
	if e.openEnded {
		e.writeIndicator("...", true, false, false)
		e.writeIndent()
	}
}

func (e *emitter) increaseIndent(flow, indentless bool) {
	e.indents = append(e.indents, e.indent)
	if e.indent < 0 {
		if flow {
			e.indent = e.bestIndent
		} else {
			e.indent = 0
		}
	} else if !indentless {
		e.indent += e.bestIndent
	}
}

func (e *emitter) popIndent() {
	e.indent = e.indents[len(e.indents)-1]
	e.indents = e.indents[:len(e.indents)-1]
}

func (e *emitter) expectNode(n *node, root, sequence, mapping, simpleKey bool) {
	e.rootContext, e.sequenceContext, e.mappingContext, e.simpleKeyContext = root, sequence, mapping, simpleKey
	switch n.kind {
	case scalarNode:
		analysis := analyzeScalar(n.value, e.allowUnicode)
		style := e.chooseScalarStyle(n, analysis)
		e.processTag(n, style)
		// expect_scalar
		e.increaseIndent(true, false)
		e.processScalar(n, style)
		e.popIndent()
	case sequenceNode:
		if e.flowLevel > 0 || len(n.items) == 0 {
			e.expectFlowSequence(n)
		} else {
			e.expectBlockSequence(n)
		}
	case mappingNode:
		if e.flowLevel > 0 || len(n.pairs) == 0 {
			e.expectFlowMapping(n)
		} else {
			e.expectBlockMapping(n)
		}
	}
}

func (e *emitter) expectFlowSequence(n *node) {
	e.writeIndicator("[", true, true, false)
	e.flowLevel++
	e.increaseIndent(true, false)
	for i, item := range n.items {
		if i > 0 {
			e.writeIndicator(",", false, false, false)
		}
		if e.column > e.bestWidth {
			e.writeIndent()
		}
		e.expectNode(item, false, true, false, false)
	}
	e.popIndent()
	e.flowLevel--
	e.writeIndicator("]", false, false, false)
}

func (e *emitter) expectFlowMapping(n *node) {
	e.writeIndicator("{", true, true, false)
	e.flowLevel++
	e.increaseIndent(true, false)
	for i, pair := range n.pairs {
		if i > 0 {
			e.writeIndicator(",", false, false, false)
		}
		if e.column > e.bestWidth {
			e.writeIndent()
		}
		if e.checkSimpleKey(pair[0]) {
			e.expectNode(pair[0], false, false, true, true)
			e.writeIndicator(":", false, false, false)
			e.expectNode(pair[1], false, false, true, false)
		} else {
			e.writeIndicator("?", true, false, false)
			e.expectNode(pair[0], false, false, true, false)
			if e.column > e.bestWidth {
				e.writeIndent()
			}
			e.writeIndicator(":", true, false, false)
			e.expectNode(pair[1], false, false, true, false)
		}
	}
	e.popIndent()
	e.flowLevel--
	e.writeIndicator("}", false, false, false)
}

func (e *emitter) expectBlockSequence(n *node) {
	indentless := e.mappingContext && !e.indention
	e.increaseIndent(false, indentless)
	for _, item := range n.items {
		e.writeIndent()
		e.writeIndicator("-", true, false, true)
		e.expectNode(item, false, true, false, false)
	}
	e.popIndent()
}

func (e *emitter) expectBlockMapping(n *node) {
	e.increaseIndent(false, false)
	for _, pair := range n.pairs {
		e.writeIndent()
		if e.checkSimpleKey(pair[0]) {
			e.expectNode(pair[0], false, false, true, true)
			e.writeIndicator(":", false, false, false)
			e.expectNode(pair[1], false, false, true, false)
		} else {
			e.writeIndicator("?", true, false, true)
			e.expectNode(pair[0], false, false, true, false)
			e.writeIndent()
			e.writeIndicator(":", true, false, true)
			e.expectNode(pair[1], false, false, true, false)
		}
	}
	e.popIndent()
}

// checkSimpleKey is Emitter.check_simple_key. The event always carries a
// tag, whose prepared form counts towards the 128-character limit.
func (e *emitter) checkSimpleKey(n *node) bool {
	length := len([]rune(n.tag))
	switch n.kind {
	case scalarNode:
		analysis := analyzeScalar(n.value, e.allowUnicode)
		length += len(n.value)
		return length < 128 && !analysis.empty && !analysis.multiline
	case sequenceNode:
		return length < 128 && len(n.items) == 0
	default:
		return length < 128 && len(n.pairs) == 0
	}
}

// chooseScalarStyle is Emitter.choose_scalar_style for unstyled scalars:
// "" (plain), "'" or "\"".
func (e *emitter) chooseScalarStyle(n *node, a scalarAnalysis) string {
	if n.implicit[0] && !(e.simpleKeyContext && (a.empty || a.multiline)) &&
		(e.flowLevel > 0 && a.allowFlowPlain || e.flowLevel == 0 && a.allowBlockPlain) {
		return ""
	}
	if a.allowSingleQuoted && !(e.simpleKeyContext && a.multiline) {
		return "'"
	}
	return `"`
}

// processTag writes an explicit tag when the chosen style would otherwise
// change how the scalar resolves.
func (e *emitter) processTag(n *node, style string) {
	if style == "" && n.implicit[0] || style != "" && n.implicit[1] {
		return
	}
	e.writeIndicator(n.tag, true, false, false)
}

func (e *emitter) processScalar(n *node, style string) {
	split := !e.simpleKeyContext
	switch style {
	case `"`:
		e.writeDoubleQuoted(n.value, split)
	case "'":
		e.writeSingleQuoted(n.value, split)
	default:
		e.writePlain(n.value, split)
	}
}

func isBreakRune(ch rune) bool { return ch == '\n' || ch == 0x85 || ch == 0x2028 || ch == 0x2029 }

func isSpaceOrBreak(ch rune) bool {
	return ch == 0 || ch == ' ' || ch == '\t' || ch == '\r' || isBreakRune(ch)
}

// analyzeScalar is Emitter.analyze_scalar.
func analyzeScalar(s []rune, allowUnicode bool) scalarAnalysis {
	if len(s) == 0 {
		return scalarAnalysis{empty: true, allowBlockPlain: true, allowSingleQuoted: true, allowDoubleQuoted: true}
	}
	var blockIndicators, flowIndicators, lineBreaks, specialCharacters bool
	var leadingSpace, leadingBreak, trailingSpace, trailingBreak, breakSpace, spaceBreak bool
	if hasPrefix(s, "---") || hasPrefix(s, "...") {
		blockIndicators, flowIndicators = true, true
	}
	precededByWhitespace := true
	followedByWhitespace := len(s) == 1 || isSpaceOrBreak(s[1])
	previousSpace, previousBreak := false, false
	for index := 0; index < len(s); {
		ch := s[index]
		if index == 0 {
			if strings.ContainsRune("#,[]{}&*!|>'\"%@`", ch) {
				flowIndicators, blockIndicators = true, true
			}
			if ch == '?' || ch == ':' {
				flowIndicators = true
				if followedByWhitespace {
					blockIndicators = true
				}
			}
			if ch == '-' && followedByWhitespace {
				flowIndicators, blockIndicators = true, true
			}
		} else {
			if strings.ContainsRune(",?[]{}", ch) {
				flowIndicators = true
			}
			if ch == ':' {
				flowIndicators = true
				if followedByWhitespace {
					blockIndicators = true
				}
			}
			if ch == '#' && precededByWhitespace {
				flowIndicators, blockIndicators = true, true
			}
		}
		if isBreakRune(ch) {
			lineBreaks = true
		}
		if !(ch == '\n' || ch >= 0x20 && ch <= 0x7e) {
			if (ch == 0x85 || ch >= 0xa0 && ch <= 0xd7ff || ch >= 0xe000 && ch <= 0xfffd ||
				ch >= 0x10000 && ch < 0x10ffff) && ch != 0xfeff {
				if !allowUnicode {
					specialCharacters = true
				}
			} else {
				specialCharacters = true
			}
		}
		switch {
		case ch == ' ':
			if index == 0 {
				leadingSpace = true
			}
			if index == len(s)-1 {
				trailingSpace = true
			}
			if previousBreak {
				breakSpace = true
			}
			previousSpace, previousBreak = true, false
		case isBreakRune(ch):
			if index == 0 {
				leadingBreak = true
			}
			if index == len(s)-1 {
				trailingBreak = true
			}
			if previousSpace {
				spaceBreak = true
			}
			previousSpace, previousBreak = false, true
		default:
			previousSpace, previousBreak = false, false
		}
		index++
		precededByWhitespace = isSpaceOrBreak(ch)
		followedByWhitespace = index+1 >= len(s) || isSpaceOrBreak(s[index+1])
	}
	a := scalarAnalysis{
		multiline:         lineBreaks,
		allowFlowPlain:    true,
		allowBlockPlain:   true,
		allowSingleQuoted: true,
		allowDoubleQuoted: true,
		allowBlock:        true,
	}
	if leadingSpace || leadingBreak || trailingSpace || trailingBreak {
		a.allowFlowPlain, a.allowBlockPlain = false, false
	}
	if trailingSpace {
		a.allowBlock = false
	}
	if breakSpace {
		a.allowFlowPlain, a.allowBlockPlain, a.allowSingleQuoted = false, false, false
	}
	if spaceBreak || specialCharacters {
		a.allowFlowPlain, a.allowBlockPlain, a.allowSingleQuoted, a.allowBlock = false, false, false, false
	}
	if lineBreaks {
		a.allowFlowPlain, a.allowBlockPlain = false, false
	}
	if flowIndicators {
		a.allowFlowPlain = false
	}
	if blockIndicators {
		a.allowBlockPlain = false
	}
	return a
}

func hasPrefix(s []rune, prefix string) bool {
	p := []rune(prefix)
	if len(s) < len(p) {
		return false
	}
	for i := range p {
		if s[i] != p[i] {
			return false
		}
	}
	return true
}

// write appends data and advances the column by its code points.
func (e *emitter) write(data string) {
	e.column += pystr.Len(data)
	e.out.WriteString(data)
}

func (e *emitter) writeIndicator(indicator string, needWhitespace, whitespace, indention bool) {
	data := indicator
	if !e.whitespace && needWhitespace {
		data = " " + indicator
	}
	e.whitespace = whitespace
	e.indention = e.indention && indention
	e.openEnded = false
	e.write(data)
}

func (e *emitter) writeIndent() {
	indent := max(e.indent, 0)
	if !e.indention || e.column > indent || e.column == indent && !e.whitespace {
		e.writeLineBreak("\n")
	}
	if e.column < indent {
		e.whitespace = true
		e.out.WriteString(strings.Repeat(" ", indent-e.column))
		e.column = indent
	}
}

func (e *emitter) writeLineBreak(data string) {
	e.whitespace = true
	e.indention = true
	e.column = 0
	e.out.WriteString(data)
}

// writeBreaks writes the line breaks text[start:end] the way the scalar
// writers do: a leading "\n" is doubled because folding would eat it.
func (e *emitter) writeBreaks(text []rune) {
	if text[0] == '\n' {
		e.writeLineBreak("\n")
	}
	for _, br := range text {
		e.writeLineBreak(string(br))
	}
}

func (e *emitter) writePlain(text []rune, split bool) {
	if e.rootContext {
		e.openEnded = true
	}
	if len(text) == 0 {
		return
	}
	if !e.whitespace {
		e.write(" ")
	}
	e.whitespace, e.indention = false, false
	spaces, breaks := false, false
	start := 0
	for end := 0; end <= len(text); end++ {
		ch := rune(-1)
		if end < len(text) {
			ch = text[end]
		}
		switch {
		case spaces:
			if ch != ' ' {
				if start+1 == end && e.column > e.bestWidth && split {
					e.writeIndent()
					e.whitespace, e.indention = false, false
				} else {
					e.write(string(text[start:end]))
				}
				start = end
			}
		case breaks:
			if !isBreakRune(ch) {
				e.writeBreaks(text[start:end])
				e.writeIndent()
				e.whitespace, e.indention = false, false
				start = end
			}
		default:
			if ch < 0 || ch == ' ' || isBreakRune(ch) {
				e.write(string(text[start:end]))
				start = end
			}
		}
		if ch >= 0 {
			spaces, breaks = ch == ' ', isBreakRune(ch)
		}
	}
}

func (e *emitter) writeSingleQuoted(text []rune, split bool) {
	e.writeIndicator("'", true, false, false)
	spaces, breaks := false, false
	start := 0
	for end := 0; end <= len(text); end++ {
		ch := rune(-1)
		if end < len(text) {
			ch = text[end]
		}
		switch {
		case spaces:
			if ch != ' ' {
				if start+1 == end && e.column > e.bestWidth && split && start != 0 && end != len(text) {
					e.writeIndent()
				} else {
					e.write(string(text[start:end]))
				}
				start = end
			}
		case breaks:
			if ch < 0 || !isBreakRune(ch) {
				e.writeBreaks(text[start:end])
				e.writeIndent()
				start = end
			}
		default:
			if ch < 0 || ch == ' ' || isBreakRune(ch) || ch == '\'' {
				if start < end {
					e.write(string(text[start:end]))
					start = end
				}
			}
		}
		if ch == '\'' {
			e.write("''")
			start = end + 1
		}
		if ch >= 0 {
			spaces, breaks = ch == ' ', isBreakRune(ch)
		}
	}
	e.writeIndicator("'", false, false, false)
}

var escapeReplacements = map[rune]string{
	0: "0", 0x07: "a", 0x08: "b", 0x09: "t", 0x0a: "n", 0x0b: "v", 0x0c: "f", 0x0d: "r", 0x1b: "e",
	'"': `"`, '\\': `\`, 0x85: "N", 0xa0: "_", 0x2028: "L", 0x2029: "P",
}

func (e *emitter) writeDoubleQuoted(text []rune, split bool) {
	e.writeIndicator(`"`, true, false, false)
	start := 0
	for end := 0; end <= len(text); end++ {
		ch := rune(-1)
		if end < len(text) {
			ch = text[end]
		}
		if ch < 0 || ch == '"' || ch == '\\' || ch == 0x85 || ch == 0x2028 || ch == 0x2029 || ch == 0xfeff ||
			!(ch >= 0x20 && ch <= 0x7e || e.allowUnicode && (ch >= 0xa0 && ch <= 0xd7ff || ch >= 0xe000 && ch <= 0xfffd)) {
			if start < end {
				e.write(string(text[start:end]))
				start = end
			}
			if ch >= 0 {
				var data string
				switch {
				case escapeReplacements[ch] != "":
					data = `\` + escapeReplacements[ch]
				case ch <= 0xff:
					data = fmt.Sprintf(`\x%02X`, ch)
				case ch <= 0xffff:
					data = fmt.Sprintf(`\u%04X`, ch)
				default:
					data = fmt.Sprintf(`\U%08X`, ch)
				}
				e.write(data)
				start = end + 1
			}
		}
		if 0 < end && end < len(text)-1 && (ch == ' ' || start >= end) &&
			e.column+(end-start) > e.bestWidth && split {
			data := string(text[start:end]) + `\`
			if start < end {
				start = end
			}
			e.write(data)
			e.writeIndent()
			e.whitespace, e.indention = false, false
			if text[start] == ' ' {
				e.write(`\`)
			}
		}
	}
	e.writeIndicator(`"`, false, false, false)
}
