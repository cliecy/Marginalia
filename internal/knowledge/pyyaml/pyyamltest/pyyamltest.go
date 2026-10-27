// Package pyyamltest converts between pyyaml values and the type-tagged JSON
// used by the golden files recorded from the reference implementation, e.g.
// {"t":"int","v":"5"} or {"t":"map","v":[[key, value], …]}.
package pyyamltest

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"marginalia/internal/knowledge/pyyaml"
)

// Tagged is the JSON shape of a tagged value.
type Tagged struct {
	T  string          `json:"t"`
	V  json.RawMessage `json:"v,omitempty"`
	TZ *int            `json:"tz,omitempty"`
}

// Untag decodes a tagged value.
func Untag(raw json.RawMessage) (any, error) {
	var t Tagged
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	switch t.T {
	case "none":
		return nil, nil
	case "bool":
		var b bool
		err := json.Unmarshal(t.V, &b)
		return b, err
	case "str":
		var s string
		err := json.Unmarshal(t.V, &s)
		return s, err
	case "int":
		var s string
		if err := json.Unmarshal(t.V, &s); err != nil {
			return nil, err
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil, fmt.Errorf("bad int %q", s)
		}
		if n.IsInt64() {
			return n.Int64(), nil
		}
		return n, nil
	case "float":
		var s string
		if err := json.Unmarshal(t.V, &s); err != nil {
			return nil, err
		}
		switch s {
		case "nan":
			return math.NaN(), nil
		case "inf":
			return math.Inf(1), nil
		case "-inf":
			return math.Inf(-1), nil
		}
		return strconv.ParseFloat(s, 64) // Go parses Python's float.hex() form
	case "date":
		var v []int
		if err := json.Unmarshal(t.V, &v); err != nil {
			return nil, err
		}
		return pyyaml.Date{Year: v[0], Month: v[1], Day: v[2]}, nil
	case "datetime":
		var v []int
		if err := json.Unmarshal(t.V, &v); err != nil {
			return nil, err
		}
		dt := pyyaml.DateTime{Date: pyyaml.Date{Year: v[0], Month: v[1], Day: v[2]},
			Hour: v[3], Minute: v[4], Second: v[5], Microsecond: v[6]}
		if t.TZ != nil {
			dt.HasTZ, dt.Offset = true, *t.TZ
		}
		return dt, nil
	case "list":
		var items []json.RawMessage
		if err := json.Unmarshal(t.V, &items); err != nil {
			return nil, err
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			v, err := Untag(item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case "map":
		var pairs [][2]json.RawMessage
		if err := json.Unmarshal(t.V, &pairs); err != nil {
			return nil, err
		}
		m := pyyaml.NewMap()
		for _, p := range pairs {
			k, err := Untag(p[0])
			if err != nil {
				return nil, err
			}
			v, err := Untag(p[1])
			if err != nil {
				return nil, err
			}
			m.Set(k, v)
		}
		return m, nil
	}
	return nil, fmt.Errorf("unknown tag %q", t.T)
}

// MustUntag is Untag that panics on malformed input.
func MustUntag(raw json.RawMessage) any {
	v, err := Untag(raw)
	if err != nil {
		panic(err)
	}
	return v
}

// Same reports whether two values are identical in type and value (unlike
// pyyaml.Equal, 1 and 1.0 differ, and NaN equals NaN).
func Same(a, b any) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case bool, string, int64, pyyaml.Date, pyyaml.DateTime:
		return a == b
	case *big.Int:
		bb, ok := b.(*big.Int)
		return ok && a.Cmp(bb) == 0
	case float64:
		bf, ok := b.(float64)
		if !ok {
			return false
		}
		if math.IsNaN(a) {
			return math.IsNaN(bf)
		}
		return math.Float64bits(a) == math.Float64bits(bf)
	case []any:
		bl, ok := b.([]any)
		if !ok || len(a) != len(bl) {
			return false
		}
		for i := range a {
			if !Same(a[i], bl[i]) {
				return false
			}
		}
		return true
	case *pyyaml.Map:
		bm, ok := b.(*pyyaml.Map)
		if !ok || a.Len() != bm.Len() {
			return false
		}
		ak, bk := a.Keys(), bm.Keys()
		for i := range ak {
			if !Same(ak[i], bk[i]) || !Same(a.Value(ak[i]), bm.Value(bk[i])) {
				return false
			}
		}
		return true
	}
	return false
}

// Describe renders a value with its Go types for test failure messages.
func Describe(v any) string {
	switch v := v.(type) {
	case []any:
		s := "["
		for i, item := range v {
			if i > 0 {
				s += ", "
			}
			s += Describe(item)
		}
		return s + "]"
	case *pyyaml.Map:
		s := "{"
		first := true
		v.Range(func(k, val any) bool {
			if !first {
				s += ", "
			}
			first = false
			s += Describe(k) + ": " + Describe(val)
			return true
		})
		return s + "}"
	}
	return fmt.Sprintf("%T(%s)", v, pyyaml.Repr(v))
}
