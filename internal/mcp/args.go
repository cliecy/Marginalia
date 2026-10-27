package mcp

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"marginalia/internal/chats"
	"marginalia/internal/knowledge/pyyaml"
)

// args holds the keyword arguments of one tool call, bound and checked
// against the tool's parameters. Values use the Python value model of
// package pyyaml (string, bool, int64, float64, []any, *pyyaml.Map, nil).
type args struct {
	tool   string
	values *pyyaml.Map
}

// bindArgs reproduces Python's fn(**(params.get("arguments") or {})): falsy
// arguments mean none, anything else must be a mapping; unknown keywords are
// rejected before missing required ones, with CPython's TypeError texts
// (without the qualified-name prefix of the nested Python functions).
func bindArgs(t *tool, arguments any) (*args, error) {
	name := t.info.Name
	values := pyyaml.NewMap()
	if pyyaml.Truthy(arguments) {
		m, ok := arguments.(*pyyaml.Map)
		if !ok {
			return nil, fmt.Errorf("%s() argument after ** must be a mapping, not %s", name, pyTypeName(arguments))
		}
		values = m
	}
	for _, key := range values.Keys() {
		k, _ := key.(string)
		if t.param(k) == nil {
			return nil, fmt.Errorf("%s() got an unexpected keyword argument '%s'", name, k)
		}
	}
	var missing []string
	for _, p := range t.params {
		if p.required && !values.Has(p.name) {
			missing = append(missing, "'"+p.name+"'")
		}
	}
	if len(missing) > 0 {
		noun := "argument"
		if len(missing) > 1 {
			noun = "arguments"
		}
		return nil, fmt.Errorf("%s() missing %d required positional %s: %s", name, len(missing), noun, pyJoin(missing))
	}
	return &args{tool: name, values: values}, nil
}

// pyJoin formats names the way CPython lists missing arguments:
// 'a' / 'a' and 'b' / 'a', 'b', and 'c'.
func pyJoin(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
}

// typeError reports an argument of the wrong type. Python crashed on these
// (500); the port answers with a tool error instead.
func (a *args) typeError(name, want string, got any) error {
	return fmt.Errorf("%s() argument '%s' must be %s, not %s", a.tool, name, want, pyTypeName(got))
}

// str returns a required string argument.
func (a *args) str(name string) (string, error) {
	v := a.values.Value(name)
	s, ok := v.(string)
	if !ok {
		return "", a.typeError(name, "str", v)
	}
	return s, nil
}

// optStr returns an optional string argument; nil when absent or null.
func (a *args) optStr(name string) (*string, error) {
	v := a.values.Value(name)
	if v == nil {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, a.typeError(name, "str or None", v)
	}
	return &s, nil
}

// flag returns the truth value of an optional argument (false when absent),
// as Python's `if value` did.
func (a *args) flag(name string) bool { return pyyaml.Truthy(a.values.Value(name)) }

// strList returns an optional list of strings (Python: `value or []`).
func (a *args) strList(name string) ([]string, error) {
	v := a.values.Value(name)
	if !pyyaml.Truthy(v) {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, a.typeError(name, "a list of str", v)
	}
	out := make([]string, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s() argument '%s' must be a list of str, not a list containing %s", a.tool, name, pyTypeName(item))
		}
		out[i] = s
	}
	return out, nil
}

// messages returns a required list of chat messages ({role, content}
// objects). Content of any type is converted like Python's str() and
// messages with other roles are skipped when rendering, as in chats.Render.
func (a *args) messages(name string) ([]chats.Message, error) {
	v := a.values.Value(name)
	list, ok := v.([]any)
	if !ok {
		return nil, a.typeError(name, "list", v)
	}
	out := make([]chats.Message, len(list))
	for i, item := range list {
		if _, ok := item.(*pyyaml.Map); !ok {
			return nil, fmt.Errorf("%s() argument '%s' must be a list of objects, not a list containing %s", a.tool, name, pyTypeName(item))
		}
		data, err := pyyaml.ToJSON(item)
		if err == nil {
			err = json.Unmarshal(data, &out[i])
		}
		if err != nil {
			return nil, fmt.Errorf("%s() argument '%s': message %d: %v", a.tool, name, i, err)
		}
	}
	return out, nil
}

// pyTypeName is type(v).__name__ for a JSON-decoded value.
func pyTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int, int64, *big.Int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case *pyyaml.Map:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}
