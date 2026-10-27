package mcp

import (
	"bytes"
	"encoding/json"
)

// JSON-RPC 2.0 error codes used by the server and the bridge.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	// codeUpstreamError is reported by the bridge when the app's /mcp
	// endpoint cannot be reached or refuses the request (implementation-
	// defined server error range).
	codeUpstreamError = -32000
)

// response is a JSON-RPC response; fields are in the order Python wrote them.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // nil encodes as null
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the error member of a response.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// initializeResult is the result of initialize.
type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions"`
}

type capabilities struct {
	Tools toolsCapability `json:"tools"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// toolsListResult is the result of tools/list.
type toolsListResult struct {
	Tools []ToolInfo `json:"tools"`
}

// callResult is the result of tools/call.
type callResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError"`
}

// textContent is a text content block.
type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// resultReply encodes a success response.
func resultReply(id json.RawMessage, result any) json.RawMessage {
	return encode(response{JSONRPC: "2.0", ID: id, Result: result})
}

// errorReply encodes an error response; a nil id is sent as null.
func errorReply(id json.RawMessage, code int, message string) json.RawMessage {
	return encode(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

// parseErrorReply is the reply to a message that is not valid JSON.
func parseErrorReply() json.RawMessage {
	return errorReply(nil, codeParseError, "parse error")
}

// encode marshals v as compact JSON without escaping <, > and & (like
// Python's json.dumps) and without a trailing newline. The values passed
// here are plain structs and valid raw JSON, so encoding cannot fail; should
// it ever, an internal-error response is returned instead.
func encode(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return json.RawMessage(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// object is a JSON object that keeps its members in order, used for the
// tools' input schemas so that they serialise exactly like the Python dicts.
type object []member

// member is one key/value pair of an object.
type member struct {
	Key   string
	Value any
}

// MarshalJSON encodes the members in order.
func (o object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(m.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		value, err := json.Marshal(m.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
