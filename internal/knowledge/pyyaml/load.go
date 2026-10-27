package pyyaml

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"marginalia/internal/vault/pystr"
)

// Short forms of the YAML 1.1 core tags.
const (
	tagNull      = "!!null"
	tagBool      = "!!bool"
	tagInt       = "!!int"
	tagFloat     = "!!float"
	tagStr       = "!!str"
	tagTimestamp = "!!timestamp"
	tagMerge     = "!!merge"
	tagValue     = "!!value"
	tagSeq       = "!!seq"
	tagMap       = "!!map"
)

// implicitResolver is one of PyYAML's implicit resolvers: plain scalars whose
// first character is in first and that match re get tag.
type implicitResolver struct {
	tag   string
	re    *regexp.Regexp
	first string
}

// pyAnchor turns a Python `^(?:...)$` pattern into a Go one; Python's `$`
// also matches before a final newline.
func pyAnchor(body string) *regexp.Regexp {
	return regexp.MustCompile(`\A(?:` + body + `)\n?\z`)
}

// resolvers are PyYAML's Resolver implicit resolvers in registration order.
var resolvers = []implicitResolver{
	{tagBool, pyAnchor(`yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF`), "yYnNtTfFoO"},
	{tagFloat, pyAnchor(`[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?` +
		`|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?` +
		`|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*` +
		`|[-+]?\.(?:inf|Inf|INF)` +
		`|\.(?:nan|NaN|NAN)`), "-+0123456789."},
	{tagInt, pyAnchor(`[-+]?0b[0-1_]+` +
		`|[-+]?0[0-7_]+` +
		`|[-+]?(?:0|[1-9][0-9_]*)` +
		`|[-+]?0x[0-9a-fA-F_]+` +
		`|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+`), "-+0123456789"},
	{tagMerge, pyAnchor(`<<`), "<"},
	{tagNull, pyAnchor(`~|null|Null|NULL|`), "~nN"},
	{tagTimestamp, pyAnchor(`[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]` +
		`|[0-9][0-9][0-9][0-9]-[0-9][0-9]?-[0-9][0-9]?` +
		`(?:[Tt]|[ \t]+)[0-9][0-9]?` +
		`:[0-9][0-9]:[0-9][0-9](?:\.[0-9]*)?` +
		`(?:[ \t]*(?:Z|[-+][0-9][0-9]?(?::[0-9][0-9])?))?`), "0123456789"},
	{tagValue, pyAnchor(`=`), "="},
}

// resolvePlain returns the tag PyYAML gives a plain scalar.
func resolvePlain(value string) string {
	if value == "" {
		return tagNull
	}
	for _, r := range resolvers {
		if strings.IndexByte(r.first, value[0]) >= 0 && r.re.MatchString(value) {
			return r.tag
		}
	}
	return tagStr
}

// Load parses a single YAML document the way yaml.safe_load does. An empty
// document yields nil. Errors are returned for invalid YAML, multiple
// documents and values PyYAML's SafeLoader refuses (unknown tags, unhashable
// keys, impossible dates, …). The !!binary, !!set, !!omap and !!pairs types
// are not supported and also yield an error.
func Load(src string) (any, error) {
	dec := yaml.NewDecoder(strings.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("expected a single document in the stream")
	}
	c := &constructor{}
	return c.construct(&doc)
}

// constructor turns yaml.v3 nodes into Python-like values.
type constructor struct {
	depth int
}

const maxDepth = 512

// tagOf returns the tag PyYAML would assign to n.
func tagOf(n *yaml.Node) string {
	n = deref(n)
	if n.Style&yaml.TaggedStyle != 0 {
		return n.Tag
	}
	switch n.Kind {
	case yaml.SequenceNode:
		return tagSeq
	case yaml.MappingNode:
		return tagMap
	case yaml.ScalarNode:
		if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return tagStr
		}
		return resolvePlain(n.Value)
	}
	return ""
}

// deref follows aliases to the anchored node.
func deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < maxDepth; i++ {
		n = n.Alias
	}
	return n
}

func (c *constructor) construct(n *yaml.Node) (any, error) {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxDepth {
		return nil, errors.New("document is nested too deeply")
	}
	if n == nil {
		return nil, nil
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return c.construct(n.Content[0])
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, errors.New("unknown alias")
		}
		return c.construct(n.Alias)
	}
	tag := tagOf(n)
	switch n.Kind {
	case yaml.SequenceNode:
		if tag != tagSeq {
			return nil, fmt.Errorf("could not determine a constructor for the tag %q", tag)
		}
		list := make([]any, 0, len(n.Content))
		for _, item := range n.Content {
			v, err := c.construct(item)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, nil
	case yaml.MappingNode:
		if tag != tagMap {
			return nil, fmt.Errorf("could not determine a constructor for the tag %q", tag)
		}
		return c.constructMapping(n)
	case yaml.ScalarNode:
		return constructScalar(tag, n.Value)
	}
	return nil, fmt.Errorf("unexpected YAML node kind %d", n.Kind)
}

// constructMapping is SafeConstructor.construct_mapping, including merge
// keys ("<<") and the "=" value key.
func (c *constructor) constructMapping(n *yaml.Node) (*Map, error) {
	pairs, err := flatten(n, 0)
	if err != nil {
		return nil, err
	}
	m := NewMap()
	for _, pair := range pairs {
		var key any
		if tagOf(pair[0]) == tagValue {
			key = deref(pair[0]).Value
		} else if key, err = c.construct(pair[0]); err != nil {
			return nil, err
		}
		switch key.(type) {
		case []any, *Map:
			return nil, errors.New("while constructing a mapping: found unhashable key")
		}
		value, err := c.construct(pair[1])
		if err != nil {
			return nil, err
		}
		m.Set(key, value)
	}
	return m, nil
}

// flatten is SafeConstructor.flatten_mapping: merged pairs come first so
// that explicit keys override them.
func flatten(n *yaml.Node, depth int) ([][2]*yaml.Node, error) {
	if depth > maxDepth {
		return nil, errors.New("merge keys are nested too deeply")
	}
	var merged, own [][2]*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if tagOf(key) != tagMerge {
			own = append(own, [2]*yaml.Node{key, value})
			continue
		}
		value = deref(value)
		switch value.Kind {
		case yaml.MappingNode:
			sub, err := flatten(value, depth+1)
			if err != nil {
				return nil, err
			}
			merged = append(merged, sub...)
		case yaml.SequenceNode:
			var groups [][][2]*yaml.Node
			for _, item := range value.Content {
				item = deref(item)
				if item.Kind != yaml.MappingNode {
					return nil, errors.New("while constructing a mapping: expected a mapping for merging")
				}
				sub, err := flatten(item, depth+1)
				if err != nil {
					return nil, err
				}
				groups = append(groups, sub)
			}
			for i := len(groups) - 1; i >= 0; i-- {
				merged = append(merged, groups[i]...)
			}
		default:
			return nil, errors.New("while constructing a mapping: expected a mapping or list of mappings for merging")
		}
	}
	return append(merged, own...), nil
}

var boolValues = map[string]bool{"yes": true, "no": false, "true": true, "false": false, "on": true, "off": false}

// constructScalar applies SafeConstructor's scalar constructors.
func constructScalar(tag, value string) (any, error) {
	switch tag {
	case tagNull:
		return nil, nil
	case tagStr:
		return value, nil
	case tagBool:
		if b, ok := boolValues[pystr.Lower(value)]; ok {
			return b, nil
		}
		return nil, fmt.Errorf("invalid boolean %q", value)
	case tagInt:
		return constructInt(value)
	case tagFloat:
		return constructFloat(value)
	case tagTimestamp:
		return constructTimestamp(value)
	}
	return nil, fmt.Errorf("could not determine a constructor for the tag %q", tag)
}

// constructInt is SafeConstructor.construct_yaml_int.
func constructInt(value string) (any, error) {
	value = strings.ReplaceAll(value, "_", "")
	if value == "" {
		return nil, errors.New("invalid integer")
	}
	negative := value[0] == '-'
	if value[0] == '+' || value[0] == '-' {
		value = value[1:]
	}
	n := new(big.Int)
	var ok bool
	switch {
	case value == "0":
		return int64(0), nil
	case strings.HasPrefix(value, "0b"):
		_, ok = n.SetString(value[2:], 2)
	case strings.HasPrefix(value, "0x"):
		_, ok = n.SetString(value[2:], 16)
	case strings.HasPrefix(value, "0"):
		_, ok = n.SetString(value, 8)
	case strings.Contains(value, ":"):
		ok = true
		base := big.NewInt(1)
		parts := strings.Split(value, ":")
		for i := len(parts) - 1; i >= 0; i-- {
			digit, good := new(big.Int).SetString(parts[i], 10)
			if !good {
				ok = false
				break
			}
			n.Add(n, digit.Mul(digit, base))
			base.Mul(base, big.NewInt(60))
		}
	default:
		_, ok = n.SetString(value, 10)
	}
	if !ok {
		return nil, fmt.Errorf("invalid integer %q", value)
	}
	if negative {
		n.Neg(n)
	}
	if n.IsInt64() {
		return n.Int64(), nil
	}
	return n, nil
}

// constructFloat is SafeConstructor.construct_yaml_float.
func constructFloat(value string) (any, error) {
	value = pystr.Lower(strings.ReplaceAll(value, "_", ""))
	if value == "" {
		return nil, errors.New("invalid float")
	}
	sign := 1.0
	if value[0] == '-' {
		sign = -1
	}
	if value[0] == '+' || value[0] == '-' {
		value = value[1:]
	}
	switch {
	case value == ".inf":
		return sign * math.Inf(1), nil
	case value == ".nan":
		return math.NaN(), nil
	case strings.Contains(value, ":"):
		total, base := 0.0, 1.0
		parts := strings.Split(value, ":")
		for i := len(parts) - 1; i >= 0; i-- {
			digit, err := pyFloat(parts[i])
			if err != nil {
				return nil, err
			}
			total += digit * base
			base *= 60
		}
		return sign * total, nil
	}
	f, err := pyFloat(value)
	if err != nil {
		return nil, err
	}
	return sign * f, nil
}

// pyFloat is Python's float(str) for decimal strings.
func pyFloat(s string) (float64, error) {
	s = pystr.Strip(s)
	t := strings.TrimLeft(s, "+-")
	if strings.HasPrefix(t, "0x") || strings.HasPrefix(t, "0X") || strings.Contains(t, "_") {
		return 0, fmt.Errorf("could not convert string to float: %q", s)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && errors.Is(numErr.Err, strconv.ErrRange) {
			return f, nil // Python overflows to inf / underflows to 0 too
		}
		return 0, fmt.Errorf("could not convert string to float: %q", s)
	}
	return f, nil
}

var timestampRE = regexp.MustCompile(`\A([0-9][0-9][0-9][0-9])-([0-9][0-9]?)-([0-9][0-9]?)` +
	`(?:(?:[Tt]|[ \t]+)([0-9][0-9]?):([0-9][0-9]):([0-9][0-9])(?:\.([0-9]*))?` +
	`(?:[ \t]*(Z|([-+])([0-9][0-9]?)(?::([0-9][0-9]))?))?)?\n?\z`)

// constructTimestamp is SafeConstructor.construct_yaml_timestamp, with
// Python's datetime range checks.
func constructTimestamp(value string) (any, error) {
	m := timestampRE.FindStringSubmatch(value)
	if m == nil {
		return nil, fmt.Errorf("invalid timestamp %q", value)
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	date := Date{Year: atoi(m[1]), Month: atoi(m[2]), Day: atoi(m[3])}
	if !validDate(date) {
		return nil, fmt.Errorf("invalid date %q", value)
	}
	if m[4] == "" {
		return date, nil
	}
	t := DateTime{Date: date, Hour: atoi(m[4]), Minute: atoi(m[5]), Second: atoi(m[6])}
	if t.Hour > 23 || t.Minute > 59 || t.Second > 59 {
		return nil, fmt.Errorf("invalid time %q", value)
	}
	if fraction := m[7]; fraction != "" {
		if len(fraction) > 6 {
			fraction = fraction[:6]
		}
		fraction += strings.Repeat("0", 6-len(fraction))
		t.Microsecond = atoi(fraction)
	}
	switch {
	case m[9] != "":
		offset := atoi(m[10])*3600 + atoi(m[11])*60
		if m[9] == "-" {
			offset = -offset
		}
		if offset <= -86400 || offset >= 86400 {
			return nil, fmt.Errorf("invalid UTC offset %q", value)
		}
		t.HasTZ, t.Offset = true, offset
	case m[8] != "":
		t.HasTZ = true
	}
	return t, nil
}

func validDate(d Date) bool {
	if d.Year < 1 || d.Month < 1 || d.Month > 12 || d.Day < 1 {
		return false
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[d.Month-1]
	if d.Month == 2 && (d.Year%4 == 0 && d.Year%100 != 0 || d.Year%400 == 0) {
		days = 29
	}
	return d.Day <= days
}
