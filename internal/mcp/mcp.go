// Package mcp is Marginalia's Model Context Protocol server: a stateless
// JSON-RPC 2.0 handler that lets local AI clients (Claude Code, Codex,
// Cursor, Magpie's agents, …) read and write the vault with the same rules as
// the app itself. Hidden paths are off limits, writes honour etags when given
// and every change is committed to git with an "mcp: …" message.
//
// It is a port of the original Python MCP server and answers every message
// exactly like it did: same tools, Chinese descriptions and texts, input
// schemas, annotations, instructions and protocol versions; notifications
// get no reply and batches are answered element by element.
//
// The package also provides the transports: [HTTPHandler] for the Streamable
// HTTP endpoint (POST /mcp, JSON responses, 202 for notifications),
// [RunStdio] for newline-delimited JSON-RPC on stdin/stdout, and [Bridge],
// which relays a stdio session to a running app's /mcp endpoint so that the
// app stays the single writer of the vault.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"

	"marginalia/internal/knowledge"
	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// Server identity reported by initialize.
const (
	ServerName  = "marginalia"
	ServerTitle = "Marginalia 知识库"
	// DefaultVersion is the serverInfo.version used when Options.Version is
	// empty (the version the Python server reported).
	DefaultVersion = "0.1.0"
)

// LatestProtocolVersion is the newest MCP revision the server speaks; it is
// offered when a client asks for a revision the server does not know.
const LatestProtocolVersion = "2025-06-18"

// protocolVersions lists the supported MCP revisions, newest first.
var protocolVersions = [...]string{LatestProtocolVersion, "2025-03-26", "2024-11-05"}

// ProtocolVersions returns the supported MCP protocol revisions, newest
// first.
func ProtocolVersions() []string { return slices.Clone(protocolVersions[:]) }

// Instructions is the server's usage guide, sent in the initialize result.
const Instructions = "这是用户的个人 Markdown 知识库（与 Obsidian 兼容），主要由 AI 对话沉淀而来。\n" +
	"修改前先调用 get_conventions 阅读 " + knowledge.ConventionsFile + "，并严格遵守其中的目录结构与 frontmatter 约定。\n" +
	"查找知识先用 list_notes（看 summary）或 search_notes，再 read_note。\n" +
	"和用户的有价值对话可以用 save_conversation 存入 Chats/ 收件箱，稍后在网页端由 AI 整理入库。\n" +
	"所有写入都会自动提交到 git，可随时回滚。"

// ErrNoVault is reported by every tool call while no vault is configured
// (desktop first run). The text matches the HTTP API's 409 detail.
var ErrNoVault = errors.New("未选择笔记库")

// Git records vault changes in version control. *gitrepo.Repo satisfies it;
// the HTTP server may pass its own type that forwards to whichever
// repository is live at the time of the call.
type Git interface {
	// Record queues paths for a commit with message; immediate commits now.
	Record(paths []string, message string, immediate bool)
	// Flush commits everything queued so far.
	Flush()
}

// Options configures a Server.
type Options struct {
	// Version is reported as serverInfo.version (the app version);
	// DefaultVersion when empty.
	Version string
	// Logger receives diagnostics (tool failures, recovered panics);
	// slog.Default() when nil. Over stdio it must not write to stdout.
	Logger *slog.Logger
}

// Server answers MCP JSON-RPC messages against one vault. It holds no
// session state, so one Server can serve any number of clients and
// transports concurrently.
type Server struct {
	vault   *vault.Vault
	git     Git
	version string
	log     *slog.Logger

	// writeMu serialises the mutating tools, as the Python server (one event
	// loop) did, so that concurrent clients appending to the same note do
	// not trip over each other's etags.
	writeMu sync.Mutex
}

// New returns a server for v that records changes through git. v may be nil
// when no vault is configured yet: the handshake and tools/list still work
// and every tool call fails with ErrNoVault. git may be nil, also as a nil
// pointer such as (*gitrepo.Repo)(nil), for no history.
func New(v *vault.Vault, git Git, opts Options) *Server {
	if isNil(git) {
		git = noGit{}
	}
	if opts.Version == "" {
		opts.Version = DefaultVersion
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{vault: v, git: git, version: opts.Version, log: opts.Logger}
}

// isNil reports whether git is nil or wraps a nil pointer.
func isNil(git Git) bool {
	if git == nil {
		return true
	}
	v := reflect.ValueOf(git)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// noGit is the Git used when the caller passes none.
type noGit struct{}

func (noGit) Record([]string, string, bool) {}
func (noGit) Flush()                        {}

// Tools describes the tools in the order tools/list reports them. The
// result is a copy the caller may modify.
func (s *Server) Tools() []ToolInfo {
	out := make([]ToolInfo, len(registry))
	for i, t := range registry {
		out[i] = t.info
		out[i].InputSchema = slices.Clone(t.info.InputSchema)
	}
	return out
}

// record commits paths immediately with "mcp: <message>" (cut to 120
// characters), like MCPServer._record.
func (s *Server) record(paths []string, message string) {
	s.git.Record(paths, pystr.Head("mcp: "+message, 120), true)
}

// Handle answers one message: a JSON-RPC request, a notification or a batch
// (JSON array). ok is false when there is nothing to send back — a
// notification, a batch of notifications or an empty batch — which the HTTP
// transport answers with 202 Accepted. Malformed JSON yields a -32700 parse
// error reply (the HTTP transport sends it with status 400, as Python did).
// The reply is compact JSON without a trailing newline.
func (s *Server) Handle(message json.RawMessage) (reply []byte, ok bool) {
	data := trimJSONSpace(message)
	if !json.Valid(data) {
		return parseErrorReply(), true
	}
	if data[0] != '[' {
		reply = s.handleOne(data)
		return reply, reply != nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return parseErrorReply(), true // unreachable: data is valid JSON
	}
	replies := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if r := s.handleOne(item); r != nil {
			replies = append(replies, r)
		}
	}
	if len(replies) == 0 {
		return nil, false
	}
	return encode(replies), true
}

// handleOne answers a single (non-batch) message; nil means no reply. It
// mirrors MCPServer.handle, including its leniencies: params that are falsy
// count as {}, and the id is checked before the method so that any message
// without an id is a notification.
func (s *Server) handleOne(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errorReply(nil, codeInvalidRequest, "invalid request")
	}
	var jsonrpc string
	if err := json.Unmarshal(fields["jsonrpc"], &jsonrpc); err != nil || jsonrpc != "2.0" {
		return errorReply(nil, codeInvalidRequest, "invalid request")
	}
	id := compactID(fields["id"])
	if id == nil {
		return nil // notification, e.g. notifications/initialized
	}
	method := pyValue(fields["method"])
	params := pyValue(fields["params"])
	if !pyyaml.Truthy(params) {
		params = pyyaml.NewMap()
	}
	name, _ := method.(string)
	switch name {
	case "initialize":
		p, ok := params.(*pyyaml.Map)
		if !ok {
			return errorReply(id, codeInvalidParams, "invalid params")
		}
		version := LatestProtocolVersion
		if requested, ok := p.Value("protocolVersion").(string); ok && slices.Contains(protocolVersions[:], requested) {
			version = requested
		}
		return resultReply(id, initializeResult{
			ProtocolVersion: version,
			Capabilities:    capabilities{Tools: toolsCapability{ListChanged: false}},
			ServerInfo:      serverInfo{Name: ServerName, Title: ServerTitle, Version: s.version},
			Instructions:    Instructions,
		})
	case "ping":
		return resultReply(id, struct{}{})
	case "tools/list":
		return resultReply(id, toolsListResult{Tools: s.Tools()})
	case "tools/call":
		p, ok := params.(*pyyaml.Map)
		if !ok {
			return errorReply(id, codeInvalidParams, "invalid params")
		}
		toolName := p.Value("name")
		t := lookup(toolName)
		if t == nil {
			return errorReply(id, codeInvalidParams, "unknown tool: "+pyyaml.Str(toolName))
		}
		result, err := s.callTool(t, p.Value("arguments"))
		if err != nil {
			return errorReply(id, codeInternalError, "internal error")
		}
		return resultReply(id, result)
	}
	return errorReply(id, codeMethodNotFound, "method not found: "+pyyaml.Str(method))
}

// callTool runs t with the client's arguments. Failures of the tool itself
// (bad arguments, vault errors) are reported inside the result with isError
// and an "错误：" prefix, like Python; only a panic is returned as an error.
func (s *Server) callTool(t *tool, arguments any) (result callResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("mcp: tool panicked", "tool", t.info.Name, "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("mcp: tool %s panicked: %v", t.info.Name, p)
		}
	}()
	text, callErr := s.run(t, arguments)
	if callErr != nil {
		s.log.Debug("mcp: tool call failed", "tool", t.info.Name, "error", callErr)
		return callResult{Content: []textContent{{Type: "text", Text: "错误：" + callErr.Error()}}, IsError: true}, nil
	}
	return callResult{Content: []textContent{{Type: "text", Text: text}}, IsError: false}, nil
}

// run binds the arguments and executes the tool.
func (s *Server) run(t *tool, arguments any) (string, error) {
	args, err := bindArgs(t, arguments)
	if err != nil {
		return "", err
	}
	if s.vault == nil {
		return "", ErrNoVault
	}
	if !t.info.Annotations.ReadOnlyHint {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	return t.run(s, s.vault, args)
}

// pyValue decodes a JSON value into the Python value model (nil when the
// value is absent).
func pyValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	v, err := pyyaml.FromJSON(raw)
	if err != nil {
		return nil // unreachable: raw comes from valid JSON
	}
	return v
}

// compactID returns the request id as compact JSON, or nil when the message
// has no id (or a null one) and is therefore a notification. Ids are echoed
// verbatim, so 1.0 stays 1.0 and big integers keep every digit.
func compactID(raw json.RawMessage) json.RawMessage {
	raw = trimJSONSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil // unreachable: raw comes from valid JSON
	}
	return buf.Bytes()
}

// trimJSONSpace strips the whitespace JSON allows around a value.
func trimJSONSpace(b []byte) []byte { return bytes.Trim(b, " \t\r\n") }
