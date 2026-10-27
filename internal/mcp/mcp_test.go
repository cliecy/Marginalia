package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"marginalia/internal/chats"
	"marginalia/internal/gitrepo"
	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

// fakeGit records what the server asks of the repository.
type fakeGit struct {
	mu      sync.Mutex
	records []recordCall
	flushes int
}

type recordCall struct {
	paths     []string
	message   string
	immediate bool
}

func (g *fakeGit) Record(paths []string, message string, immediate bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.records = append(g.records, recordCall{slices.Clone(paths), message, immediate})
}

func (g *fakeGit) Flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.flushes++
}

func (g *fakeGit) snapshot() ([]recordCall, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.records), g.flushes
}

// pythonVault is the standard test vault.
func pythonVault(t *testing.T) (*vault.Vault, string) {
	t.Helper()
	root := vaulttest.Write(t, map[string][]byte{
		"A.md":          []byte("---\ntags: [ml]\nsummary: about A\n---\n# A\nsee [[B]] and ![[_assets/x.png]]\n"),
		"sub/B.md":      []byte("# B\nback to [[A|alias]]\n"),
		"_assets/x.png": []byte("\x89PNG"),
		".secret.md":    []byte("hidden"),
	})
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return v, root
}

// reply is a decoded JSON-RPC response.
type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// toolResult is a decoded tools/call result.
type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError"`
}

func (r toolResult) text() string {
	if len(r.Content) != 1 {
		return fmt.Sprintf("<%d content blocks>", len(r.Content))
	}
	return r.Content[0].Text
}

// rpc sends one request and decodes the reply.
func rpc(t *testing.T, h Handler, id any, method string, params any) reply {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := h.Handle(data)
	if !ok {
		t.Fatalf("%s: no reply", method)
	}
	var r reply
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("%s: bad reply %s: %v", method, out, err)
	}
	return r
}

// call invokes a tool and decodes its result.
func call(t *testing.T, h Handler, name string, arguments any) toolResult {
	t.Helper()
	r := rpc(t, h, 1, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if r.Error != nil {
		t.Fatalf("%s: JSON-RPC error %+v", name, r.Error)
	}
	var res toolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("%s: bad result %s: %v", name, r.Result, err)
	}
	return res
}

// TestPythonMCP covers the MCP tools (the bearer
// check belongs to the HTTP server and is tested there).
func TestPythonMCP(t *testing.T) {
	v, _ := pythonVault(t)
	srv := New(v, &fakeGit{}, Options{})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	post := func(body string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp, string(data)
	}

	init := rpc(t, srv, 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"},
	})
	var initResult initializeResult
	if err := json.Unmarshal(init.Result, &initResult); err != nil || initResult.ProtocolVersion != "2025-06-18" {
		t.Fatalf("initialize = %s (%v)", init.Result, err)
	}

	resp, body := post(`{"jsonrpc": "2.0", "method": "notifications/initialized"}`)
	if resp.StatusCode != http.StatusAccepted || body != "" {
		t.Fatalf("notification: %d %q, want 202 and no body", resp.StatusCode, body)
	}

	var list toolsListResult
	if err := json.Unmarshal(rpc(t, srv, 1, "tools/list", map[string]any{}).Result, &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, name := range []string{"read_note", "write_note", "search_notes", "save_conversation"} {
		if !names[name] {
			t.Errorf("tools/list lacks %s", name)
		}
	}

	if res := call(t, srv, "search_notes", map[string]any{"query": "alias"}); !strings.Contains(res.text(), "sub/B.md") {
		t.Errorf("search_notes alias = %q", res.text())
	}
	if res := call(t, srv, "read_note", map[string]any{"path": ".git/config"}); !res.IsError {
		t.Errorf("read_note .git/config = %+v, want an error", res)
	}
	if res := call(t, srv, "write_note", map[string]any{"path": "Knowledge/X.md", "content": "# X\n"}); res.IsError {
		t.Fatalf("write_note: %s", res.text())
	}
	if note, err := v.ReadNote("Knowledge/X.md"); err != nil || note.Content != "# X\n" {
		t.Errorf("Knowledge/X.md = %+v, %v", note, err)
	}
	saved := call(t, srv, "save_conversation", map[string]any{
		"title": "t", "messages": []any{map[string]any{"role": "user", "content": "q"}},
	})
	if saved.IsError || !strings.Contains(saved.text(), "Chats/") {
		t.Errorf("save_conversation = %+v", saved)
	}
	if res := call(t, srv, "list_inbox", nil); !strings.Contains(res.text(), "Chats/") {
		t.Errorf("list_inbox = %q", res.text())
	}

	get, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed || get.Header.Get("Allow") != "POST" {
		t.Errorf("GET /mcp = %d Allow=%q, want 405 Allow: POST", get.StatusCode, get.Header.Get("Allow"))
	}
}

func TestHTTPTransport(t *testing.T) {
	v, _ := pythonVault(t)
	ts := httptest.NewServer(New(v, nil, Options{}))
	defer ts.Close()
	cases := []struct {
		name, method, body string
		status             int
		reply              string
	}{
		{"request", "POST", `{"jsonrpc":"2.0","id":7,"method":"ping"}`, 200, `{"jsonrpc":"2.0","id":7,"result":{}}`},
		{"notification", "POST", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, 202, ``},
		{"batch", "POST", `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"x"}]`, 200, `[{"jsonrpc":"2.0","id":1,"result":{}}]`},
		{"batch of notifications", "POST", `[{"jsonrpc":"2.0","method":"x"}]`, 202, ``},
		{"empty batch", "POST", `[]`, 202, ``},
		{"parse error", "POST", `{"jsonrpc":`, 400, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`},
		{"empty body", "POST", ``, 400, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`},
		{"invalid request", "POST", `"ping"`, 200, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}`},
		{"delete", "DELETE", ``, 405, ``},
		{"put", "PUT", `{}`, 405, ``},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, ts.URL+"/mcp", strings.NewReader(c.body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status || string(data) != c.reply {
			t.Errorf("%s: %d %s, want %d %s", c.name, resp.StatusCode, data, c.status, c.reply)
		}
		if c.reply != "" && resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: Content-Type %q", c.name, resp.Header.Get("Content-Type"))
		}
		if c.status == 405 && resp.Header.Get("Allow") != "POST" {
			t.Errorf("%s: Allow %q", c.name, resp.Header.Get("Allow"))
		}
	}
}

func TestBatches(t *testing.T) {
	v, _ := pythonVault(t)
	srv := New(v, nil, Options{})
	cases := []struct {
		name, request string
		reply         string // "" = no reply
	}{
		{
			"mixed batch keeps order and drops notifications",
			`[{"jsonrpc":"2.0","id":"a","method":"ping"},
			  {"jsonrpc":"2.0","method":"notifications/initialized"},
			  {"jsonrpc":"2.0","id":2,"method":"nope"},
			  7, [], {"jsonrpc":"2.0"},
			  {"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_backlinks","arguments":{"path":"A.md"}}}]`,
			`[{"jsonrpc":"2.0","id":"a","result":{}},` +
				`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"method not found: nope"}},` +
				`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}},` +
				`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}},` +
				`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"- sub/B.md: back to [[A|alias]]"}],"isError":false}}]`,
		},
		{"only notifications", `[{"jsonrpc":"2.0","method":"a"},{"jsonrpc":"2.0","id":null,"method":"ping"}]`, ""},
		{"empty", ` [ ] `, ""},
		{"single element", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, `[{"jsonrpc":"2.0","id":1,"result":{}}]`},
		{"ids are echoed verbatim", `{"jsonrpc":"2.0","id":1.0,"method":"ping"}`, `{"jsonrpc":"2.0","id":1.0,"result":{}}`},
		{"big ids keep their digits", `{"jsonrpc":"2.0","id":123456789012345678901234567890,"method":"ping"}`, `{"jsonrpc":"2.0","id":123456789012345678901234567890,"result":{}}`},
		{"object ids are compacted", `{"jsonrpc":"2.0","id":{ "a" : [1, 2] },"method":"ping"}`, `{"jsonrpc":"2.0","id":{"a":[1,2]},"result":{}}`},
		{"no HTML escaping", `{"jsonrpc":"2.0","id":"<&>","method":"x<y>"}`, `{"jsonrpc":"2.0","id":"<&>","error":{"code":-32601,"message":"method not found: x<y>"}}`},
		{"parse error", `{"jsonrpc"`, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`},
		{"non-object params of initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":[1]}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`},
		{"non-object params of tools/call", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"x"}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`},
		{"unknown tool list name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":["a"]}}`, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown tool: ['a']"}}`},
	}
	for _, c := range cases {
		got, ok := srv.Handle(json.RawMessage(c.request))
		switch {
		case c.reply == "" && ok:
			t.Errorf("%s: got %s, want no reply", c.name, got)
		case c.reply != "" && string(got) != c.reply:
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.reply)
		}
	}
}

func TestToolsList(t *testing.T) {
	srv := New(nil, nil, Options{})
	tools := srv.Tools()
	want := []string{"get_conventions", "list_notes", "search_notes", "read_note", "get_backlinks", "write_note",
		"append_to_note", "move_note", "delete_note", "save_conversation", "list_inbox", "mark_chat_distilled"}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
		readOnly := !slices.Contains([]string{"write_note", "append_to_note", "move_note", "delete_note", "save_conversation", "mark_chat_distilled"}, tool.Name)
		if tool.Annotations.ReadOnlyHint != readOnly || tool.Annotations.DestructiveHint != (tool.Name == "delete_note") {
			t.Errorf("%s: annotations %+v", tool.Name, tool.Annotations)
		}
		if !json.Valid(tool.InputSchema) || tool.Description == "" {
			t.Errorf("%s: schema %s, description %q", tool.Name, tool.InputSchema, tool.Description)
		}
	}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %q, want %q", names, want)
	}
	// Tools returns a copy.
	tools[0].Name = "changed"
	tools[0].InputSchema[0] = '['
	if again := srv.Tools()[0]; again.Name != "get_conventions" || again.InputSchema[0] != '{' {
		t.Error("Tools exposes the registry")
	}
	if got := ProtocolVersions(); !slices.Equal(got, []string{"2025-06-18", "2025-03-26", "2024-11-05"}) {
		t.Errorf("ProtocolVersions = %q", got)
	}
}

func TestVersionAndNoVault(t *testing.T) {
	srv := New(nil, nil, Options{Version: "1.2.3"})
	var init initializeResult
	if err := json.Unmarshal(rpc(t, srv, 1, "initialize", nil).Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ServerInfo != (serverInfo{Name: "marginalia", Title: "Marginalia 知识库", Version: "1.2.3"}) ||
		init.ProtocolVersion != LatestProtocolVersion || init.Instructions != Instructions {
		t.Errorf("initialize = %+v", init)
	}
	if res := call(t, srv, "list_notes", nil); !res.IsError || res.text() != "错误：未选择笔记库" {
		t.Errorf("list_notes without vault = %+v", res)
	}
	// Argument errors still come first.
	if res := call(t, srv, "read_note", nil); res.text() != "错误：read_note() missing 1 required positional argument: 'path'" {
		t.Errorf("read_note without vault and path = %q", res.text())
	}
	if got := New(nil, nil, Options{}).version; got != DefaultVersion {
		t.Errorf("default version = %q", got)
	}
}

func TestNilRepoMeansNoHistory(t *testing.T) {
	v, _ := pythonVault(t)
	var repo *gitrepo.Repo // e.g. a Core field before a vault is chosen
	srv := New(v, repo, Options{})
	if res := call(t, srv, "write_note", map[string]any{"path": "N.md", "content": "n"}); res.IsError {
		t.Fatal(res.text())
	}
	if res := call(t, srv, "delete_note", map[string]any{"path": "N.md"}); res.IsError {
		t.Fatal(res.text())
	}
}

func TestArgumentErrors(t *testing.T) {
	v, _ := pythonVault(t)
	srv := New(v, nil, Options{})
	cases := []struct {
		tool string
		args any
		want string
	}{
		{"read_note", map[string]any{"path": 5}, "read_note() argument 'path' must be str, not int"},
		{"read_note", map[string]any{"path": nil}, "read_note() argument 'path' must be str, not NoneType"},
		{"search_notes", map[string]any{"query": 1.5}, "search_notes() argument 'query' must be str, not float"},
		{"write_note", map[string]any{"path": "A.md", "content": "x", "etag": 3}, "write_note() argument 'etag' must be str or None, not int"},
		{"mark_chat_distilled", map[string]any{"path": "Chats/x.md", "notes": "A"}, "mark_chat_distilled() argument 'notes' must be a list of str, not str"},
		{"mark_chat_distilled", map[string]any{"path": "Chats/x.md", "notes": []any{1}}, "mark_chat_distilled() argument 'notes' must be a list of str, not a list containing int"},
		{"save_conversation", map[string]any{"title": "t", "messages": map[string]any{"role": "user"}}, "save_conversation() argument 'messages' must be list, not dict"},
		{"save_conversation", map[string]any{"title": "t", "messages": []any{"hi"}}, "save_conversation() argument 'messages' must be a list of objects, not a list containing str"},
		{"save_conversation", map[string]any{"title": true, "messages": []any{}}, "save_conversation() argument 'title' must be str, not bool"},
		{"save_conversation", map[string]any{"title": "t", "messages": []any{}, "source": []any{}}, "save_conversation() argument 'source' must be str or None, not list"},
		{"move_note", map[string]any{}, "move_note() missing 2 required positional arguments: 'path' and 'to'"},
		{"list_inbox", true, "list_inbox() argument after ** must be a mapping, not bool"},
	}
	for _, c := range cases {
		res := call(t, srv, c.tool, c.args)
		if !res.IsError || res.text() != "错误："+c.want {
			t.Errorf("%s(%v) = %+v, want error %q", c.tool, c.args, res, c.want)
		}
	}
	if got := pyJoin([]string{"'a'", "'b'", "'c'"}); got != "'a', 'b', and 'c'" {
		t.Errorf("pyJoin = %q", got)
	}
}

func TestSaveConversation(t *testing.T) {
	v, _ := pythonVault(t)
	git := &fakeGit{}
	srv := New(v, git, Options{})
	res := call(t, srv, "save_conversation", map[string]any{
		"title": "PPO 讨论",
		"messages": []any{
			map[string]any{"role": "user", "content": "什么是 PPO？"},
			map[string]any{"role": "assistant", "content": "一种策略梯度方法。"},
			map[string]any{"role": "system", "content": "skipped"},
		},
	})
	if res.IsError || !strings.HasPrefix(res.text(), "已保存到 Chats/") || !strings.HasSuffix(res.text(), " PPO 讨论.md") {
		t.Fatalf("save_conversation = %+v", res)
	}
	rel := strings.TrimPrefix(res.text(), "已保存到 ")
	loaded, err := chats.Load(v, rel)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Value("model") != "mcp" || loaded.Meta.Value("status") != "inbox" || loaded.Meta.Value("title") != "PPO 讨论" {
		t.Errorf("meta = %s", must(json.Marshal(loaded.Meta)))
	}
	if len(loaded.Messages) != 2 || loaded.Messages[1].Content != "一种策略梯度方法。" {
		t.Errorf("messages = %+v", loaded.Messages)
	}
	records, _ := git.snapshot()
	if len(records) != 1 || records[0].message != "mcp: chat PPO 讨论" || records[0].paths[0] != rel {
		t.Errorf("records = %+v", records)
	}

	res = call(t, srv, "save_conversation", map[string]any{"title": "second", "messages": []any{}, "source": "claude-code"})
	loaded, err = chats.Load(v, strings.TrimPrefix(res.text(), "已保存到 "))
	if err != nil || loaded.Meta.Value("model") != "claude-code" {
		t.Errorf("source: %+v %v", loaded, err)
	}
	if inbox := call(t, srv, "list_inbox", nil).text(); strings.Count(inbox, "\n") != 1 {
		t.Errorf("list_inbox = %q", inbox)
	}
}

func TestCommitMessagesAreCut(t *testing.T) {
	v, _ := pythonVault(t)
	git := &fakeGit{}
	srv := New(v, git, Options{})
	title := strings.Repeat("长", 200)
	if res := call(t, srv, "save_conversation", map[string]any{"title": title, "messages": []any{}}); res.IsError {
		t.Fatal(res.text())
	}
	records, _ := git.snapshot()
	if got := records[0].message; got != "mcp: chat "+strings.Repeat("长", 110) {
		t.Errorf("message = %q (%d runes)", got, len([]rune(got)))
	}
}

// TestConcurrentAppends checks that mutating calls are serialised: without
// it concurrent appends to one note would fail their etag checks.
func TestConcurrentAppends(t *testing.T) {
	v, _ := pythonVault(t)
	srv := New(v, &fakeGit{}, Options{})
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"append_to_note","arguments":{"path":"Log.md","text":"line %d"}}}`, i, i)
			out, _ := srv.Handle(json.RawMessage(msg))
			if !strings.Contains(string(out), `"isError":false`) {
				errs <- string(out)
			}
			// Readers run alongside.
			srv.Handle(json.RawMessage(`{"jsonrpc":"2.0","id":0,"method":"tools/call","params":{"name":"list_notes"}}`))
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	note, err := v.ReadNote("Log.md")
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if !strings.Contains(note.Content, fmt.Sprintf("line %d\n", i)) {
			t.Errorf("Log.md lacks line %d:\n%s", i, note.Content)
		}
	}
}

func TestPanicIsInternalError(t *testing.T) {
	srv := New(nil, nil, Options{})
	boom := &tool{info: ToolInfo{Name: "boom"}, run: func(*Server, *vault.Vault, *args) (string, error) { panic("boom") }}
	srv.vault = &vault.Vault{}
	if _, err := srv.callTool(boom, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("callTool = %v, want the panic as an error", err)
	}
}

// TestRealGit checks that tool writes end up as commits of a live
// repository (*gitrepo.Repo satisfies Git).
func TestRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	v, root := pythonVault(t)
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := gitrepo.Init(root); err != nil {
		t.Fatal(err)
	}
	repo := gitrepo.New(gitrepo.Options{Root: root, AuthorName: "t", AuthorEmail: "t@x", Enabled: true, Delay: time.Hour})
	defer repo.Close()
	srv := New(v, repo, Options{})
	if res := call(t, srv, "write_note", map[string]any{"path": "Knowledge/X.md", "content": "# X\n"}); res.IsError {
		t.Fatal(res.text())
	}
	if res := call(t, srv, "move_note", map[string]any{"path": "Knowledge/X.md", "to": "Knowledge/Y.md"}); res.IsError {
		t.Fatal(res.text())
	}
	history := repo.History("Knowledge/Y.md", 10)
	var subjects []string
	for _, c := range history {
		subjects = append(subjects, c.Subject)
	}
	want := []string{"mcp: move Knowledge/X.md -> Knowledge/Y.md", "mcp: write Knowledge/X.md"}
	if !slices.Equal(subjects, want) {
		t.Errorf("history = %q, want %q", subjects, want)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
