package pystr

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// NaturalCompare orders two names the way the Python backend's tree sort did:
//
//	key = [int(p) if p.isdigit() else p.lower() for p in re.split(r"(\d+)", name)]
//
// Digit runs (any Unicode decimal digits) compare numerically, the text
// between them compares case-insensitively by code point, and a key that is
// a prefix of another sorts first. It returns -1, 0 or +1.
func NaturalCompare(a, b string) int {
	pa, pb := naturalParts(a), naturalParts(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var c int
		if i%2 == 1 {
			c = compareDigits(pa[i], pb[i])
		} else {
			c = strings.Compare(Lower(pa[i]), Lower(pb[i]))
		}
		if c != 0 {
			return c
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	}
	return 0
}

// NaturalLess reports whether a sorts before b under NaturalCompare.
func NaturalLess(a, b string) bool { return NaturalCompare(a, b) < 0 }

// naturalParts is re.split(r"(\d+)", s): text and digit runs alternate, the
// list starts and ends with a (possibly empty) text part.
func naturalParts(s string) []string {
	parts := make([]string, 0, 3)
	start, inDigits := 0, false
	for i, r := range s {
		if d := unicode.IsDigit(r); d != inDigits {
			parts = append(parts, s[start:i])
			start, inDigits = i, d
		}
	}
	parts = append(parts, s[start:])
	if inDigits {
		parts = append(parts, "")
	}
	return parts
}

// compareDigits compares two runs of decimal digits by numeric value.
func compareDigits(a, b string) int {
	da, db := digitValues(a), digitValues(b)
	if len(da) != len(db) {
		if len(da) < len(db) {
			return -1
		}
		return 1
	}
	for i := range da {
		if da[i] != db[i] {
			if da[i] < db[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// digitValues returns the digit values of s with leading zeros removed.
func digitValues(s string) []int {
	values := make([]int, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		v := digitValue(r)
		if v == 0 && len(values) == 0 {
			continue
		}
		values = append(values, v)
	}
	return values
}

// digitValue returns the value of a Unicode decimal digit. Decimal digits are
// always encoded in contiguous runs of 0..9, so the value is the number of
// digits immediately preceding r, modulo ten.
func digitValue(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	n := 0
	for unicode.IsDigit(r - rune(n) - 1) {
		n++
	}
	return n % 10
}
