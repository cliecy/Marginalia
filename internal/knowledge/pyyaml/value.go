// Package pyyaml reproduces the observable behaviour of PyYAML 6
// (yaml.safe_load and yaml.safe_dump with allow_unicode=True,
// sort_keys=False, default_flow_style=False) together with the Python value
// semantics the Marginalia backend applied to frontmatter: str(), truth
// testing, dict ordering and FastAPI's JSON encoding.
//
// Parsing is delegated to go.yaml.in/yaml/v3 (a libyaml port); this package
// adds PyYAML's YAML 1.1 implicit typing ("yes" is a bool, "2024-01-01" is a
// date, "1:30" is 90) and its constructors. Dumping is a line-by-line port of
// PyYAML's emitter, so files written by the Go app are byte-identical to the
// ones the Python app wrote.
//
// Values are represented as:
//
//	nil          None
//	bool         bool
//	int64        int (*big.Int when it does not fit)
//	float64      float
//	string       str
//	Date         datetime.date
//	DateTime     datetime.datetime
//	[]any        list
//	*Map         dict (insertion ordered)
//
// Plain Go int values are accepted wherever a value is consumed.
package pyyaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"

	"marginalia/internal/vault/pystr"
)

// Map is an insertion-ordered mapping with Python dict semantics: setting an
// existing key keeps its position, and keys compare with Python equality
// (1, 1.0 and True are the same key). The zero value is an empty map.
type Map struct {
	keys   []any
	values []any
}

// NewMap returns an empty map.
func NewMap() *Map { return &Map{} }

// Len returns the number of entries; a nil map is empty.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

func (m *Map) index(key any) int {
	if m == nil {
		return -1
	}
	for i, k := range m.keys {
		if Equal(k, key) {
			return i
		}
	}
	return -1
}

// Get returns the value stored under key.
func (m *Map) Get(key any) (any, bool) {
	if i := m.index(key); i >= 0 {
		return m.values[i], true
	}
	return nil, false
}

// Value is dict.get(key): the value under key, or nil when absent.
func (m *Map) Value(key any) any {
	v, _ := m.Get(key)
	return v
}

// Has reports whether key is present.
func (m *Map) Has(key any) bool { return m.index(key) >= 0 }

// Set stores value under key, keeping the key's position when it exists and
// appending it otherwise.
func (m *Map) Set(key, value any) {
	key, value = normalize(key), normalize(value)
	if i := m.index(key); i >= 0 {
		m.values[i] = value
		return
	}
	m.keys = append(m.keys, key)
	m.values = append(m.values, value)
}

// Delete removes key and reports whether it was present.
func (m *Map) Delete(key any) bool {
	i := m.index(key)
	if i < 0 {
		return false
	}
	m.keys = append(m.keys[:i], m.keys[i+1:]...)
	m.values = append(m.values[:i], m.values[i+1:]...)
	return true
}

// Keys returns the keys in insertion order.
func (m *Map) Keys() []any {
	if m == nil {
		return nil
	}
	return append([]any(nil), m.keys...)
}

// Range calls fn for every entry in order until fn returns false.
func (m *Map) Range(fn func(key, value any) bool) {
	if m == nil {
		return
	}
	for i := range m.keys {
		if !fn(m.keys[i], m.values[i]) {
			return
		}
	}
}

// Clone returns a deep copy of m (lists and nested maps are copied too).
func (m *Map) Clone() *Map {
	out := NewMap()
	m.Range(func(k, v any) bool {
		out.keys = append(out.keys, k)
		out.values = append(out.values, cloneValue(v))
		return true
	})
	return out
}

func cloneValue(v any) any {
	switch v := v.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneValue(item)
		}
		return out
	case *Map:
		return v.Clone()
	case *big.Int:
		return new(big.Int).Set(v)
	}
	return v
}

// normalize converts accepted Go values to the canonical representation.
func normalize(v any) any {
	switch v := v.(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float32:
		return float64(v)
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out
	}
	return v
}

// Date is a calendar date (datetime.date).
type Date struct {
	Year, Month, Day int
}

// String is date.isoformat(): "2024-01-31".
func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// DateTime is a datetime.datetime with an optional fixed UTC offset.
type DateTime struct {
	Date
	Hour, Minute, Second, Microsecond int
	HasTZ                             bool // aware datetime
	Offset                            int  // UTC offset in seconds when HasTZ
}

// Isoformat is datetime.isoformat(sep).
func (t DateTime) Isoformat(sep string) string {
	s := fmt.Sprintf("%s%s%02d:%02d:%02d", t.Date.String(), sep, t.Hour, t.Minute, t.Second)
	if t.Microsecond != 0 {
		s += fmt.Sprintf(".%06d", t.Microsecond)
	}
	if t.HasTZ {
		off, sign := t.Offset, "+"
		if off < 0 {
			off, sign = -off, "-"
		}
		s += fmt.Sprintf("%s%02d:%02d", sign, off/3600, off/60%60)
		if off%60 != 0 {
			s += fmt.Sprintf(":%02d", off%60)
		}
	}
	return s
}

// String is str(datetime): the ISO form with a space separator.
func (t DateTime) String() string { return t.Isoformat(" ") }

// utcSeconds returns the instant as seconds since 0001-01-01 UTC-ish, for
// equality of aware datetimes.
func (t DateTime) utcSeconds() (int64, int) {
	days := daysFromCivil(t.Year, t.Month, t.Day)
	secs := days*86400 + int64(t.Hour*3600+t.Minute*60+t.Second) - int64(t.Offset)
	return secs, t.Microsecond
}

// daysFromCivil counts days since 1970-01-01 in the proleptic Gregorian
// calendar.
func daysFromCivil(y, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 && y%400 != 0 {
		era--
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return int64(era)*146097 + int64(doe) - 719468
}

// Equal is Python's == for the supported values.
func Equal(a, b any) bool {
	a, b = normalize(a), normalize(b)
	if na, ok := numeric(a); ok {
		nb, ok := numeric(b)
		return ok && na.Cmp(nb) == 0
	}
	switch a := a.(type) {
	case nil:
		return b == nil
	case string:
		s, ok := b.(string)
		return ok && a == s
	case Date:
		d, ok := b.(Date)
		return ok && a == d
	case DateTime:
		t, ok := b.(DateTime)
		if !ok || a.HasTZ != t.HasTZ {
			return false
		}
		if !a.HasTZ {
			return a == t
		}
		as, au := a.utcSeconds()
		bs, bu := t.utcSeconds()
		return as == bs && au == bu
	case []any:
		l, ok := b.([]any)
		if !ok || len(a) != len(l) {
			return false
		}
		for i := range a {
			if !Equal(a[i], l[i]) {
				return false
			}
		}
		return true
	case *Map:
		m, ok := b.(*Map)
		if !ok || a.Len() != m.Len() {
			return false
		}
		equal := true
		a.Range(func(k, v any) bool {
			other, found := m.Get(k)
			equal = found && Equal(v, other)
			return equal
		})
		return equal
	}
	return false
}

// numeric returns the value of bools, ints and finite floats as a big.Float
// so that True == 1 == 1.0 as in Python.
func numeric(v any) (*big.Float, bool) {
	switch v := v.(type) {
	case bool:
		if v {
			return big.NewFloat(1), true
		}
		return big.NewFloat(0), true
	case int64:
		return new(big.Float).SetInt64(v), true
	case *big.Int:
		return new(big.Float).SetInt(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false
		}
		return big.NewFloat(v), true
	}
	return nil, false
}

// Truthy is Python's bool(v).
func Truthy(v any) bool {
	switch v := normalize(v).(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case *big.Int:
		return v.Sign() != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case *Map:
		return v.Len() > 0
	}
	return true
}

// Or is Python's `a or b`: a when truthy, else b.
func Or(a, b any) any {
	if Truthy(a) {
		return a
	}
	return b
}

// Str is Python's str(v).
func Str(v any) string {
	switch v := normalize(v).(type) {
	case string:
		return v
	case Date:
		return v.String()
	case DateTime:
		return v.String()
	}
	return Repr(v)
}

// Repr is Python's repr(v).
func Repr(v any) string {
	switch v := normalize(v).(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(v, 10)
	case *big.Int:
		return v.String()
	case float64:
		return pystr.FloatRepr(v)
	case string:
		return pystr.Repr(v)
	case Date:
		return fmt.Sprintf("datetime.date(%d, %d, %d)", v.Year, v.Month, v.Day)
	case DateTime:
		fields := []int{v.Year, v.Month, v.Day, v.Hour, v.Minute, v.Second, v.Microsecond}
		if fields[6] == 0 {
			fields = fields[:6]
			if fields[5] == 0 {
				fields = fields[:5]
			}
		}
		parts := make([]string, len(fields))
		for i, f := range fields {
			parts[i] = strconv.Itoa(f)
		}
		s := "datetime.datetime(" + strings.Join(parts, ", ")
		if v.HasTZ {
			s += ", tzinfo=" + reprTimezone(v.Offset)
		}
		return s + ")"
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = Repr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *Map:
		var parts []string
		v.Range(func(k, val any) bool {
			parts = append(parts, Repr(k)+": "+Repr(val))
			return true
		})
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// reprTimezone is repr(datetime.timezone(timedelta(seconds=offset))).
func reprTimezone(offset int) string {
	if offset == 0 {
		return "datetime.timezone.utc"
	}
	days, secs := offset/86400, offset%86400
	if secs < 0 {
		days--
		secs += 86400
	}
	var args []string
	if days != 0 {
		args = append(args, fmt.Sprintf("days=%d", days))
	}
	if secs != 0 {
		args = append(args, fmt.Sprintf("seconds=%d", secs))
	}
	return "datetime.timezone(datetime.timedelta(" + strings.Join(args, ", ") + "))"
}

// MarshalJSON encodes the map the way FastAPI's jsonable_encoder followed by
// json.dumps did: insertion order, dates in ISO format, non-string keys
// converted to their JSON text ("1", "true", "null").
func (m *Map) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ToJSON encodes any supported value as JSON (see Map.MarshalJSON).
func ToJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	err := writeJSON(&buf, v)
	return buf.Bytes(), err
}

func writeJSON(buf *bytes.Buffer, v any) error {
	switch v := normalize(v).(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case int64:
		buf.WriteString(strconv.FormatInt(v, 10))
	case *big.Int:
		buf.WriteString(v.String())
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			buf.WriteString("null") // Python refused to encode these
		} else {
			buf.WriteString(pystr.FloatRepr(v))
		}
	case string:
		return writeJSONString(buf, v)
	case Date:
		return writeJSONString(buf, v.String())
	case DateTime:
		return writeJSONString(buf, v.Isoformat("T"))
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSON(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case *Map:
		buf.WriteByte('{')
		var err error
		first := true
		v.Range(func(k, val any) bool {
			if !first {
				buf.WriteByte(',')
			}
			first = false
			if err = writeJSONString(buf, jsonKey(k)); err != nil {
				return false
			}
			buf.WriteByte(':')
			err = writeJSON(buf, val)
			return err == nil
		})
		if err != nil {
			return err
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("pyyaml: cannot encode %T as JSON", v)
	}
	return nil
}

// jsonKey is how json.dumps turns a dict key into an object key.
func jsonKey(k any) string {
	switch k := normalize(k).(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(k)
	case float64:
		switch {
		case math.IsNaN(k):
			return "NaN"
		case math.IsInf(k, 1):
			return "Infinity"
		case math.IsInf(k, -1):
			return "-Infinity"
		}
		return pystr.FloatRepr(k)
	case DateTime:
		return k.Isoformat("T")
	}
	return Str(k)
}

func writeJSONString(buf *bytes.Buffer, s string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	buf.Write(data)
	return nil
}

// UnmarshalJSON decodes a JSON object, keeping key order. Integers become
// int64 (or *big.Int), numbers with a fraction or exponent float64, as with
// Python's json module.
func (m *Map) UnmarshalJSON(data []byte) error {
	v, err := FromJSON(data)
	if err != nil {
		return err
	}
	decoded, ok := v.(*Map)
	if !ok {
		return fmt.Errorf("pyyaml: JSON value is not an object")
	}
	*m = *decoded
	return nil
}

// FromJSON decodes any JSON value into the representation described in the
// package documentation, keeping object key order.
func FromJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeJSON(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("pyyaml: trailing data after JSON value")
	}
	return v, nil
}

func decodeJSON(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := NewMap()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyTok.(string)
				val, err := decodeJSON(dec)
				if err != nil {
					return nil, err
				}
				m.Set(key, val)
			}
			_, err := dec.Token()
			return m, err
		case '[':
			list := []any{}
			for dec.More() {
				val, err := decodeJSON(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, val)
			}
			_, err := dec.Token()
			return list, err
		}
	case json.Number:
		text := t.String()
		if !strings.ContainsAny(text, ".eE") {
			if n, err := strconv.ParseInt(text, 10, 64); err == nil {
				return n, nil
			}
			if n, ok := new(big.Int).SetString(text, 10); ok {
				return n, nil
			}
		}
		return strconv.ParseFloat(text, 64)
	case string, bool, nil:
		return t, nil
	}
	return nil, fmt.Errorf("pyyaml: unexpected JSON token %v", tok)
}
