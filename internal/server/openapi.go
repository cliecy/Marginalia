package server

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// openAPIDoc is the hand-written OpenAPI 3.1 description of the public API
// (every documented route of the route table plus /mcp; never /api/app/*).
// Its info.version and servers are filled in per request.
//
//go:embed openapi.json
var openAPIDoc []byte

// GET /api/openapi.json: the OpenAPI document, public and secret-free, with
// the app version and this server's URL.
func (s *Server) openAPI(w http.ResponseWriter, r *http.Request, st *state) error {
	server := map[string]string{"url": requestScheme(r, trustsProxy(r, st)) + "://" + r.Host, "description": "当前服务"}
	if authOf(r).kind == authApp {
		// The app's own scheme is no use to other programs: point them at
		// the loopback API.
		server = map[string]string{"url": s.APIStatus().URL, "description": "本机 API"}
	}
	doc, err := renderOpenAPI(s.core.version, server)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(doc)
	return nil
}

// renderOpenAPI returns the embedded document with info.version and
// servers replaced, keeping the authored key order (the API page lists the
// operations in document order).
func renderOpenAPI(version string, server map[string]string) ([]byte, error) {
	doc, err := parseOrderedObject(openAPIDoc)
	if err != nil {
		return nil, fmt.Errorf("openapi.json: %w", err)
	}
	info, err := parseOrderedObject(doc.values["info"])
	if err != nil {
		return nil, fmt.Errorf("openapi.json info: %w", err)
	}
	if version != "" {
		if err := info.set("version", version); err != nil {
			return nil, err
		}
	}
	if err := doc.set("info", info); err != nil {
		return nil, err
	}
	if err := doc.set("servers", []map[string]string{server}); err != nil {
		return nil, err
	}
	return marshalJSON(doc)
}

// orderedObject is a JSON object whose members keep their order.
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

// parseOrderedObject decodes the members of a JSON object, unparsed.
func parseOrderedObject(data []byte) (*orderedObject, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	o := &orderedObject{values: map[string]json.RawMessage{}}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if _, dup := o.values[key]; !dup {
			o.keys = append(o.keys, key)
		}
		o.values[key] = value
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	return o, nil
}

// set replaces (or appends) a member.
func (o *orderedObject) set(key string, v any) error {
	raw, err := marshalJSON(v)
	if err != nil {
		return err
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
	return nil
}

// MarshalJSON writes the members in order.
func (o *orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := marshalJSON(key)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(o.values[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
