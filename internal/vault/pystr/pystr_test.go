package pystr

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"
)

type golden struct {
	Decode []struct {
		Bytes    string `json:"bytes"`
		Decode   string `json:"decode"`
		ReadText string `json:"read_text"`
	} `json:"decode"`
	Strings map[string]struct {
		SplitLines []string `json:"splitlines"`
		Lower      string   `json:"lower"`
		Strip      string   `json:"strip"`
		Split      []string `json:"split"`
		LStrip     string   `json:"lstrip"`
		Repr       string   `json:"repr"`
	} `json:"strings"`
	Floats map[string]string `json:"floats"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	data, err := os.ReadFile("testdata/golden_pystr.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestDecodeMatchesPython(t *testing.T) {
	g := loadGolden(t)
	if len(g.Decode) == 0 {
		t.Fatal("empty golden")
	}
	for _, c := range g.Decode {
		data, _ := base64.StdEncoding.DecodeString(c.Bytes)
		if got := Decode(data); got != c.Decode {
			t.Errorf("Decode(%q) = %q, want %q", data, got, c.Decode)
		}
		if got := ReadText(data); got != c.ReadText {
			t.Errorf("ReadText(%q) = %q, want %q", data, got, c.ReadText)
		}
	}
}

func TestStringHelpersMatchPython(t *testing.T) {
	g := loadGolden(t)
	for s, want := range g.Strings {
		if got := SplitLines(s); !slices.Equal(got, want.SplitLines) {
			t.Errorf("SplitLines(%q) = %q, want %q", s, got, want.SplitLines)
		}
		if got := Lower(s); got != want.Lower {
			t.Errorf("Lower(%q) = %q, want %q", s, got, want.Lower)
		}
		if got := Strip(s); got != want.Strip {
			t.Errorf("Strip(%q) = %q, want %q", s, got, want.Strip)
		}
		if got := LStrip(s); got != want.LStrip {
			t.Errorf("LStrip(%q) = %q, want %q", s, got, want.LStrip)
		}
		if got := Fields(s); !slices.Equal(got, want.Split) && !(len(got) == 0 && len(want.Split) == 0) {
			t.Errorf("Fields(%q) = %q, want %q", s, got, want.Split)
		}
		if got := Repr(s); got != want.Repr {
			t.Errorf("Repr(%q) = %s, want %s", s, got, want.Repr)
		}
	}
}

func TestFloatReprMatchesPython(t *testing.T) {
	g := loadGolden(t)
	for hex, want := range g.Floats {
		f, err := strconv.ParseFloat(hex, 64)
		if err != nil {
			t.Fatalf("parse %s: %v", hex, err)
		}
		if got := FloatRepr(f); got != want {
			t.Errorf("FloatRepr(%s) = %s, want %s", hex, got, want)
		}
	}
}

func TestSliceAndIndex(t *testing.T) {
	cases := []struct {
		s          string
		start, end int
		want       string
	}{
		{"héllo", 1, 3, "él"},
		{"héllo", 0, -1, "héllo"},
		{"héllo", 3, 100, "lo"},
		{"héllo", 10, 20, ""},
		{"héllo", 4, 2, ""},
		{"中文字符", 2, 4, "字符"},
		{"", 0, 5, ""},
	}
	for _, c := range cases {
		if got := Slice(c.s, c.start, c.end); got != c.want {
			t.Errorf("Slice(%q, %d, %d) = %q, want %q", c.s, c.start, c.end, got, c.want)
		}
	}
	if got := Index("aé中b", "b"); got != 3 {
		t.Errorf("Index = %d, want 3", got)
	}
	if got := Index("abc", "x"); got != -1 {
		t.Errorf("Index = %d, want -1", got)
	}
	if got := Head("中文字符", 2); got != "中文" {
		t.Errorf("Head = %q", got)
	}
}

func TestNaturalCompareBasics(t *testing.T) {
	ordered := []string{"", "a", "a0", "a00", "a1", "a2", "a10", "b", "Note 1", "note 02", "Note 10", "x²"}
	for i := 1; i < len(ordered); i++ {
		if NaturalCompare(ordered[i-1], ordered[i]) > 0 {
			t.Errorf("%q should not sort after %q", ordered[i-1], ordered[i])
		}
	}
	if NaturalCompare("a01", "a1") != 0 {
		t.Error("leading zeros should compare equal")
	}
	if !NaturalLess("img2", "img10") || NaturalLess("img10", "img2") {
		t.Error("numeric parts must compare by value")
	}
	if !NaturalLess("２", "１０") {
		t.Error("full-width digits must compare by value")
	}
	if !NaturalLess("99999999999999999999998", "99999999999999999999999") {
		t.Error("big numbers must compare without overflow")
	}
}
