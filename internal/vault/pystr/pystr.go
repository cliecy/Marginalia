// Package pystr reproduces the small set of Python string semantics that the
// original Marginalia backend (Python 3.12) relied on, so that the Go port
// produces byte-identical search snippets, summaries, link targets and
// frontmatter.
//
// Python indexes strings by code point, treats a wider range of characters as
// whitespace than Go's unicode.IsSpace, lowercases with a few context rules,
// splits lines on more separators than "\n", and decodes invalid UTF-8 with
// one replacement character per maximal ill-formed subsequence. Each helper
// here documents the Python expression it stands for.
package pystr

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SpaceClass is a regexp character-class body (without brackets) matching
// exactly the characters Python's str.isspace() and re's \s accept.
const SpaceClass = `\t\n\v\f\r\x1c-\x1f \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// WordClass is a regexp character-class body matching Python's Unicode \w
// (every letter and number category plus the underscore).
const WordClass = `\p{L}\p{N}_`

// IsSpace reports whether r is whitespace for Python's str.isspace().
// Unlike unicode.IsSpace it includes the ASCII separators U+001C..U+001F.
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// Strip is str.strip(): it removes leading and trailing Python whitespace.
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// LStrip is str.lstrip().
func LStrip(s string) string { return strings.TrimLeftFunc(s, IsSpace) }

// RStrip is str.rstrip().
func RStrip(s string) string { return strings.TrimRightFunc(s, IsSpace) }

// StripChars is str.strip(chars): it removes any of the runes in chars from
// both ends of s.
func StripChars(s, chars string) string { return strings.Trim(s, chars) }

// Fields is str.split() without arguments: runs of Python whitespace separate
// fields and empty fields are dropped.
func Fields(s string) []string { return strings.FieldsFunc(s, IsSpace) }

// isLineBreak reports whether r ends a line for str.splitlines().
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// SplitLines is str.splitlines(): it splits on every Python line boundary
// ("\r\n" counts as one) and does not return a trailing empty line.
func SplitLines(s string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isLineBreak(r) {
			i += size
			continue
		}
		lines = append(lines, s[start:i])
		i += size
		if r == '\r' && i < len(s) && s[i] == '\n' {
			i++
		}
		start = i
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// Len is len(s) in Python: the number of code points.
func Len(s string) int { return utf8.RuneCountInString(s) }

// Slice is s[start:end] with Python's clamping for non-negative indices,
// counted in code points. A negative end means "to the end of the string".
func Slice(s string, start, end int) string {
	if start < 0 {
		start = 0
	}
	runeIndex := 0
	from, to := len(s), len(s)
	for byteIndex := range s {
		if runeIndex == start {
			from = byteIndex
		}
		if end >= 0 && runeIndex == end {
			to = byteIndex
			break
		}
		runeIndex++
	}
	if from > to {
		return ""
	}
	return s[from:to]
}

// Head is s[:n] in Python (n >= 0), counted in code points.
func Head(s string, n int) string { return Slice(s, 0, n) }

// Index is str.find(sub) counted in code points (-1 when absent).
func Index(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return utf8.RuneCountInString(s[:i])
}

// Lower is str.lower(). It differs from strings.ToLower in the two places
// Python applies full Unicode case mapping: U+0130 (İ) becomes "i̇" and a
// capital sigma at the end of a word becomes the final form ς.
func Lower(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		switch r {
		case 0x130:
			b.WriteString("i\u0307")
		case 0x3a3:
			if finalSigma(runes, i) {
				b.WriteRune(0x3c2)
			} else {
				b.WriteRune(0x3c3)
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// finalSigma implements CPython's handle_capital_sigma: Σ is final when it
// follows a cased letter and is not followed by one (case-ignorable
// characters are skipped in both directions).
func finalSigma(runes []rune, i int) bool {
	j := i - 1
	for j >= 0 && caseIgnorable(runes[j]) {
		j--
	}
	if j < 0 || !cased(runes[j]) {
		return false
	}
	k := i + 1
	for k < len(runes) && caseIgnorable(runes[k]) {
		k++
	}
	return k == len(runes) || !cased(runes[k])
}

func cased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

func caseIgnorable(r rune) bool {
	switch r {
	// Word_Break = MidLetter, MidNumLet or Single_Quote.
	case '\'', '.', ':', 0xb7, 0x387, 0x55f, 0x5f4, 0x2018, 0x2019, 0x2024, 0x2027,
		0xfe13, 0xfe52, 0xfe55, 0xff07, 0xff0e, 0xff1a:
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk)
}

// Decode is bytes.decode("utf-8", errors="replace"): every maximal
// ill-formed subsequence becomes a single U+FFFD, exactly like CPython.
func Decode(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	b.Grow(len(data) + 8)
	for i := 0; i < len(data); {
		c := data[i]
		if c < utf8.RuneSelf {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		if r != utf8.RuneError || size > 1 {
			b.Write(data[i : i+size])
			i += size
			continue
		}
		b.WriteRune(utf8.RuneError)
		i += invalidLen(data[i:])
	}
	return b.String()
}

// invalidLen returns the length of the maximal ill-formed subsequence at the
// start of b (Unicode 15, section 3.9, "U+FFFD substitution of maximal
// subparts"), which is how CPython's decoder groups bad bytes.
func invalidLen(b []byte) int {
	c := b[0]
	need, lo, hi := 0, byte(0x80), byte(0xbf)
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c == 0xe0:
		need, lo = 2, 0xa0
	case c == 0xed:
		need, hi = 2, 0x9f
	case c >= 0xe1 && c <= 0xef:
		need = 2
	case c == 0xf0:
		need, lo = 3, 0x90
	case c >= 0xf1 && c <= 0xf3:
		need = 3
	case c == 0xf4:
		need, hi = 3, 0x8f
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(b); k++ {
		x := b[n]
		if k == 0 && (x < lo || x > hi) || k > 0 && (x < 0x80 || x > 0xbf) {
			return n
		}
		n++
	}
	return n
}

// ReadText is Path.read_text(encoding="utf-8", errors="replace"): Decode
// followed by universal-newline translation ("\r\n" and "\r" become "\n").
func ReadText(data []byte) string {
	s := Decode(data)
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Repr is repr(str): the quoted, escaped form Python prints for a string.
func Repr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			b.WriteString(`\x` + hex2(int(r)))
		case r < utf8.RuneSelf || unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			b.WriteString(`\x` + hex2(int(r)))
		case r <= 0xffff:
			b.WriteString(`\u` + leftPad(strconv.FormatInt(int64(r), 16), 4))
		default:
			b.WriteString(`\U` + leftPad(strconv.FormatInt(int64(r), 16), 8))
		}
	}
	b.WriteByte(quote)
	return b.String()
}

func hex2(v int) string { return leftPad(strconv.FormatInt(int64(v), 16), 2) }

func leftPad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}

// FloatRepr is repr(float): the shortest round-tripping digits, written in
// positional notation when the decimal exponent is in [-4, 16) and in
// scientific notation otherwise ("1e+16", "1e-05", "100.0").
func FloatRepr(f float64) string {
	switch {
	case f != f:
		return "nan"
	case f > 1.7976931348623157e308:
		return "inf"
	case f < -1.7976931348623157e308:
		return "-inf"
	}
	sign := ""
	if f < 0 || (f == 0 && 1/f < 0) {
		sign = "-"
		f = -f
	}
	if f == 0 {
		return sign + "0.0"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, expText, _ := strings.Cut(sci, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1
	if decpt <= -4 || decpt > 16 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		expSign := "+"
		e := decpt - 1
		if e < 0 {
			expSign, e = "-", -e
		}
		return sign + out + "e" + expSign + leftPad(strconv.Itoa(e), 2)
	}
	switch {
	case decpt <= 0:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	default:
		return sign + digits[:decpt] + "." + digits[decpt:]
	}
}
