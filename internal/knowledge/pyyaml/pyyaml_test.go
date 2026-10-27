package pyyaml_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/knowledge/pyyaml/pyyamltest"
	"marginalia/internal/vault/vaulttest"
)

type golden struct {
	Load []struct {
		Src   string          `json:"src"`
		Value json.RawMessage `json:"value"`
		Error string          `json:"error"`
	} `json:"load"`
	Dump []struct {
		Value json.RawMessage `json:"value"`
		Out   string          `json:"out"`
	} `json:"dump"`
	Str []struct {
		Value  json.RawMessage `json:"value"`
		Str    string          `json:"str"`
		Truthy bool            `json:"truthy"`
	} `json:"str"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	var g golden
	vaulttest.Golden(t, "testdata/golden_yaml.json", &g)
	return g
}

func TestLoadMatchesSafeLoad(t *testing.T) {
	g := loadGolden(t)
	if len(g.Load) == 0 {
		t.Fatal("empty golden")
	}
	for _, c := range g.Load {
		got, err := pyyaml.Load(c.Src)
		if c.Error != "" {
			if err == nil {
				t.Errorf("Load(%q) = %s, want error (%s)", c.Src, pyyamltest.Describe(got), c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("Load(%q): unexpected error %v", c.Src, err)
			continue
		}
		want := pyyamltest.MustUntag(c.Value)
		if !pyyamltest.Same(got, want) {
			t.Errorf("Load(%q):\n got %s\nwant %s", c.Src, pyyamltest.Describe(got), pyyamltest.Describe(want))
		}
	}
}

func TestDumpMatchesSafeDump(t *testing.T) {
	g := loadGolden(t)
	if len(g.Dump) == 0 {
		t.Fatal("empty golden")
	}
	for _, c := range g.Dump {
		v := pyyamltest.MustUntag(c.Value)
		got, err := pyyaml.Dump(v)
		if err != nil {
			t.Errorf("Dump(%s): %v", pyyamltest.Describe(v), err)
			continue
		}
		if got != c.Out {
			t.Errorf("Dump(%s):\n got %q\nwant %q", pyyamltest.Describe(v), got, c.Out)
		}
	}
}

func TestDumpRoundTrips(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Dump {
		v := pyyamltest.MustUntag(c.Value)
		if f, ok := v.(*pyyaml.Map); ok {
			if x, ok := f.Value("f").(float64); ok && math.IsNaN(x) {
				continue // NaN never equals itself
			}
		}
		out, err := pyyaml.Dump(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(out, 0x85) {
			continue // PyYAML itself folds a quoted NEL into a space on reload
		}
		back, err := pyyaml.Load(out)
		if err != nil {
			t.Errorf("reload of %q: %v", out, err)
			continue
		}
		if !pyyamltest.Same(back, v) {
			t.Errorf("round trip of %q:\n got %s\nwant %s", out, pyyamltest.Describe(back), pyyamltest.Describe(v))
		}
	}
}

func TestStrAndTruthMatchPython(t *testing.T) {
	g := loadGolden(t)
	for _, c := range g.Str {
		v := pyyamltest.MustUntag(c.Value)
		if got := pyyaml.Str(v); got != c.Str {
			t.Errorf("Str(%s) = %q, want %q", pyyamltest.Describe(v), got, c.Str)
		}
		if got := pyyaml.Truthy(v); got != c.Truthy {
			t.Errorf("Truthy(%s) = %v, want %v", pyyamltest.Describe(v), got, c.Truthy)
		}
	}
}

func TestMapSemantics(t *testing.T) {
	m := pyyaml.NewMap()
	m.Set("b", 1)
	m.Set("a", 2)
	m.Set("b", 3) // keeps position
	m.Set(int64(1), "one")
	m.Set(true, "true replaces 1") // Python: True == 1
	m.Set(1.0, "float too")
	if got := pyyaml.Repr(m); got != "{'b': 3, 'a': 2, 1: 'float too'}" {
		t.Errorf("Repr = %s", got)
	}
	if !m.Delete("a") || m.Delete("a") || m.Len() != 2 {
		t.Error("Delete")
	}
	clone := m.Clone()
	clone.Set("b", []any{"x"})
	if pyyaml.Equal(clone, m) {
		t.Error("clone must be independent")
	}
	var nilMap *pyyaml.Map
	if nilMap.Len() != 0 || nilMap.Value("x") != nil || nilMap.Has("x") {
		t.Error("nil map must behave as empty")
	}
}

func TestJSONEncoding(t *testing.T) {
	m := pyyaml.NewMap()
	m.Set("z", "last-key-first")
	m.Set("date", pyyaml.Date{Year: 2024, Month: 1, Day: 2})
	m.Set("dt", pyyaml.DateTime{Date: pyyaml.Date{Year: 2024, Month: 1, Day: 2}, Hour: 3, HasTZ: true, Offset: 8 * 3600})
	m.Set(int64(1), 1.0)
	m.Set(nil, []any{true, nil, 1e16})
	m.Set("nan", math.NaN())
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":"last-key-first","date":"2024-01-02","dt":"2024-01-02T03:00:00+08:00","1":1.0,"null":[true,null,1e+16],"nan":null}`
	if string(got) != want {
		t.Errorf("JSON:\n got %s\nwant %s", got, want)
	}
	var back pyyaml.Map
	if err := json.Unmarshal([]byte(`{"b":1,"a":[1.5,"x",{"n":null}],"big":123456789012345678901234567890}`), &back); err != nil {
		t.Fatal(err)
	}
	if got := pyyaml.Repr(&back); got != "{'b': 1, 'a': [1.5, 'x', {'n': None}], 'big': 123456789012345678901234567890}" {
		t.Errorf("decoded = %s", got)
	}
}

func TestLoadEdgeCases(t *testing.T) {
	for _, src := range []string{"", "# comment only", "---\n...\n"} {
		v, err := pyyaml.Load(src)
		if err != nil || v != nil {
			t.Errorf("Load(%q) = %v, %v; want nil", src, v, err)
		}
	}
	deep := strings.Repeat("[", 2000) + strings.Repeat("]", 2000)
	if _, err := pyyaml.Load(deep); err == nil {
		t.Error("deep nesting accepted")
	}
	if _, err := pyyaml.Dump(struct{}{}); err == nil {
		t.Error("unsupported type dumped")
	}
}
