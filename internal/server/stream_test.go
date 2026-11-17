package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOpenAI is an OpenAI-compatible chat service. Each completion answers
// the next scripted reply as a stream of deltas; when gate is set, it waits
// for it after the first delta.
type fakeOpenAI struct {
	*httptest.Server
	mu       sync.Mutex
	replies  []string
	status   int
	requests []map[string]any
	gate     chan struct{}
	chatFn   func(w http.ResponseWriter, r *http.Request, body map[string]any)
}
func newFakeOpenAI(t *testing.T, replies ...string) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{replies: replies, status: 200}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOpenAI) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/models":
		_, _ = io.WriteString(w, `{"data":[{"id":"m1","display_name":"Model One"},{"id":"text-embedding-3"},{"id":"m2"}]}`)
		return
	case "/v1/chat/completions":
	default:
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	status, gate, chatFn := f.status, f.gate, f.chatFn
	reply := ""
	if chatFn == nil && len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	if status != 200 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
		return
	}
	if chatFn != nil {
		chatFn(w, r, body)
		return
	}
	flusher := w.(http.Flusher)
	for i, piece := range splitReply(reply) {
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": piece}}}})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		if i == 0 && gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Second):
				return
			}
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// splitReply cuts a reply into deltas at "|".
func splitReply(reply string) []string { return strings.Split(reply, "|") }

func (f *fakeOpenAI) lastRequest() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

// aiFixture is a fixture whose AI service is fake (loopback: no key needed).
func aiFixture(t *testing.T, ai *fakeOpenAI) *fixture {
	return newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) { s.AI.BaseURL, s.AI.DefaultModel = ai.URL+"/v1", "m1" }
	})
}

func TestChatStreamsOverTCPAndFlushesEachEvent(t *testing.T) {
	ai := newFakeOpenAI(t, "Hel|lo")
	ai.gate = make(chan struct{})
	f := aiFixture(t, ai)
	srv := httptest.NewServer(f.srv.NetHandler())
	defer srv.Close()

	body := `{"messages":[{"role":"user","content":"hi"}],"scope":"note","path":"A.md"}`
	req, _ := http.NewRequest("POST", srv.URL+"/api/ai/chat", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("response %d %v", resp.StatusCode, resp.Header)
	}
	events := make(chan string, 8)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				events <- data
			}
		}
	}()
	next := func() string {
		t.Helper()
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("stream ended early")
			}
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("no event: the delta was not flushed")
		}
		return ""
	}
	// The first delta arrives while the model is still "thinking".
	if e := next(); e != `{"delta":"Hel"}` {
		t.Fatalf("first event = %s", e)
	}
	close(ai.gate)
	if e := next(); e != `{"delta":"lo"}` {
		t.Fatalf("second event = %s", e)
	}
	if e := next(); e != `{"done":true}` {
		t.Fatalf("last event = %s", e)
	}

	sent := ai.lastRequest()
	msgs := sent["messages"].([]any)
	if sent["model"] != "m1" || sent["stream"] != true || msgs[0].(map[string]any)["role"] != "system" ||
		!strings.Contains(msgs[0].(map[string]any)["content"].(string), "A.md") {
		t.Fatalf("model request = %v", sent)
	}
}

// flushRecorder records the body length at every Flush.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes []int
}

func (r *flushRecorder) Flush() {
	r.flushes = append(r.flushes, r.Body.Len())
	r.ResponseRecorder.Flush()
}

func TestChatThroughAppHandlerFlushesAfterEveryEvent(t *testing.T) {
	ai := newFakeOpenAI(t, "a|b|c")
	f := aiFixture(t, ai)
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(`{"messages":[{"role":"user","content":"q"}],"scope":"none"}`))
	req.RemoteAddr = "mygo:1"
	req.Header.Set("Origin", "mygo://localhost")
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	f.srv.AppHandler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if got := sseLines(body); strings.Join(got, " ") != `{"delta":"a"} {"delta":"b"} {"delta":"c"} {"done":true}` {
		t.Fatalf("events = %q", got)
	}
	// Headers first, then one flush right after each event.
	if len(rec.flushes) < 5 || rec.flushes[0] != 0 {
		t.Fatalf("flushes at %v", rec.flushes)
	}
	end := 0
	for _, e := range sseLines(body) {
		end += len("data: ") + len(e) + 2
		found := false
		for _, at := range rec.flushes {
			found = found || at == end
		}
		if !found {
			t.Fatalf("no flush after event ending at %d (flushes at %v)", end, rec.flushes)
		}
	}
}

func TestChatErrors(t *testing.T) {
	ai := newFakeOpenAI(t)
	ai.status = 500
	f := aiFixture(t, ai)
	c := f.loggedIn()
	res := c.do("POST", "/api/ai/chat", jsonMap{"messages": []jsonMap{{"role": "user", "content": "q"}}, "scope": "none"})
	expect(t, res, 200)
	events := sseLines(res.Body.String())
	if len(events) != 1 || !strings.Contains(events[0], `"error":"AI 请求失败（HTTP 500）`) {
		t.Fatalf("events = %q", events)
	}
	// Problems with the request answer before the stream starts.
	expect(t, c.do("POST", "/api/ai/chat", jsonMap{"messages": []any{}, "scope": "note", "path": "missing.md"}), 404)
	expect(t, c.do("POST", "/api/ai/chat", jsonMap{"messages": []any{}, "scope": "note", "path": "../x.md"}), 400)
	expect(t, c.do("POST", "/api/ai/chat", jsonMap{"scope": "none"}), 422)

	// Not configured: an error event, as in Python.
	g := newFixture(t)
	res = g.loggedIn().do("POST", "/api/ai/chat", jsonMap{"messages": []jsonMap{{"role": "user", "content": "q"}}, "scope": "none"})
	expect(t, res, 200)
	if events := sseLines(res.Body.String()); len(events) != 1 || !strings.Contains(events[0], "AI 未配置") {
		t.Fatalf("events = %q", events)
	}
}

func TestAgentStreamsEvents(t *testing.T) {
	plan := `{"summary": "补充 B", "operations": [{"op": "update", "path": "sub/B.md", "reason": "补充", "instructions": "加一句"}]}`
	ai := newFakeOpenAI(t, plan, "# B\n|更多内容\n")
	f := aiFixture(t, ai)
	res := f.loggedIn().do("POST", "/api/ai/agent", jsonMap{"task": "整理", "chats": []string{}})
	expect(t, res, 200)
	if !strings.HasPrefix(res.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("headers = %v", res.Header())
	}
	var kinds []string
	var proposal map[string]any
	for _, line := range sseLines(res.Body.String()) {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("event %q: %v", line, err)
		}
		kinds = append(kinds, e["event"].(string))
		if e["event"] == "proposal" {
			proposal = e["proposal"].(map[string]any)
		}
	}
	if strings.Join(kinds, ",") != "status,plan,status,proposal,done" {
		t.Fatalf("events = %v\n%s", kinds, res.Body)
	}
	if proposal["op"] != "update" || proposal["path"] != "sub/B.md" || proposal["base_etag"] == nil ||
		!strings.Contains(proposal["after"].(string), "更多内容") {
		t.Fatalf("proposal = %v", proposal)
	}
	// Invalid chat paths are rejected before streaming.
	expect(t, f.loggedIn().do("POST", "/api/ai/agent", jsonMap{"task": "x", "chats": []string{"../x"}}), 400)

	// A model failure ends the stream with an error event.
	ai.mu.Lock()
	ai.status = 502
	ai.mu.Unlock()
	res = f.loggedIn().do("POST", "/api/ai/agent", jsonMap{"task": "x"})
	events := sseLines(res.Body.String())
	if last := events[len(events)-1]; !strings.Contains(last, `"event":"error"`) {
		t.Fatalf("events = %q", events)
	}
}

func TestModels(t *testing.T) {
	ai := newFakeOpenAI(t)
	f := aiFixture(t, ai)
	got := decode[jsonMap](t, f.loggedIn().do("GET", "/api/ai/models", nil))
	if fmt.Sprint(got["models"]) != "[m1 m2]" || got["default"] != "m1" || fmt.Sprint(got["labels"]) != "map[m1:Model One]" || got["error"] != nil {
		t.Fatalf("models = %v", got)
	}
	g := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) { s.AI.BaseURL = closedURL(t) + "/v1"; s.AI.DefaultModel = "fallback" }
	})
	got = decode[jsonMap](t, g.loggedIn().do("GET", "/api/ai/models", nil))
	if fmt.Sprint(got["models"]) != "[fallback]" || got["default"] != "fallback" || got["error"] == nil {
		t.Fatalf("models with the service down = %v", got)
	}
}

func TestPrompts(t *testing.T) {
	f := newFixture(t)
	writeFile(t, f.notes, "copilot/copilot-custom-prompts/总结.md", "---\ncopilot-command-context-menu-enabled: true\n---\n\n  总结 {}  \n")
	writeFile(t, f.notes, "copilot/other.md", "not a prompt")
	got := decode[jsonMap](t, f.loggedIn().do("GET", "/api/ai/prompts", nil))
	if fmt.Sprint(got["prompts"]) != "[map[name:总结 path:copilot/copilot-custom-prompts/总结.md template:总结 {}]]" {
		t.Fatalf("prompts = %v", got)
	}
}

func TestChatToolCallingStreamsProposalsAndApplies(t *testing.T) {
	needGit(t)
	ai := newFakeOpenAI(t)
	var turn int
	ai.chatFn = func(w http.ResponseWriter, _ *http.Request, body map[string]any) {
		turn++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		sendChunk := func(v any) {
			data, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		if turn == 1 {
			if body["tools"] == nil {
				t.Errorf("expected tools on chat request, got %v", body)
			}
			sendChunk(map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{
					"tool_calls": []any{
						map[string]any{
							"index": 0,
							"id":    "call_1",
							"type":  "function",
							"function": map[string]any{
								"name":      "edit_note",
								"arguments": `{"path":"A.md","old_text":"# A","new_text":"# A (revised)","reason":"精简修订"}`,
							},
						},
					},
				},
			}}})
			sendChunk(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}})
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		sendChunk(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content":"已生成修改建议。"}}}})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	f := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) {
			s.AI.BaseURL, s.AI.DefaultModel = ai.URL+"/v1", "m1"
			s.Git.Autocommit = true
		}
	})
	if err := os.RemoveAll(filepath.Join(f.notes, ".git")); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, f.notes, "init", "-q")
	if err := f.core.Apply(f.core.Settings()); err != nil {
		t.Fatal(err)
	}
	c := f.loggedIn()
	// Save an inbox chat first so we can verify POST /api/ai/apply with chats: [] leaves it inbox.
	savedChat := decode[jsonMap](t, c.do("PUT", "/api/chats", jsonMap{
		"title":    "测试对话",
		"messages": []jsonMap{{"role": "user", "content": "hi"}},
	}))
	chatPath := savedChat["path"].(string)

	srv := httptest.NewServer(f.srv.NetHandler())
	defer srv.Close()

	reqBody := `{"messages":[{"role":"user","content":"改一下 A.md"}],"scope":"note","path":"A.md"}`
	req, _ := http.NewRequest("POST", srv.URL+"/api/ai/chat", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	rawBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	lines := sseLines(string(rawBytes))
	var gotStatus, gotDelta, gotDone bool
	var proposal map[string]any
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad event %q: %v", line, err)
		}
		if _, ok := ev["status"]; ok {
			gotStatus = true
		}
		if ev["delta"] == "已生成修改建议。" {
			gotDelta = true
		}
		if p, ok := ev["proposal"].(map[string]any); ok {
			proposal = p
		}
		if ev["done"] == true {
			gotDone = true
		}
	}
	if !gotStatus || !gotDelta || proposal == nil || !gotDone {
		t.Fatalf("missing events in %q", lines)
	}
	if proposal["op"] != "update" || proposal["path"] != "A.md" || proposal["base_etag"] == nil {
		t.Fatalf("proposal = %+v", proposal)
	}

	// Note on disk must still be untouched until apply.
	beforeNote := decode[jsonMap](t, c.do("GET", "/api/note?path=A.md", nil))
	if strings.Contains(beforeNote["content"].(string), "revised") {
		t.Fatalf("note modified before apply: %v", beforeNote)
	}

	// Apply the returned proposal with chats: [].
	delete(proposal, "before")
	applyRes := c.do("POST", "/api/ai/apply", jsonMap{
		"summary":    proposal["reason"],
		"operations": []any{proposal},
		"chats":      []string{},
	})
	expect(t, applyRes, 200)

	afterNote := decode[jsonMap](t, c.do("GET", "/api/note?path=A.md", nil))
	if !strings.Contains(afterNote["content"].(string), "A (revised)") {
		t.Fatalf("note not updated after apply: %v", afterNote)
	}

	// Verify git history contains ai: commit and chat remains inbox.
	hist := decode[jsonMap](t, c.do("GET", "/api/history?path=A.md", nil))
	commits, _ := hist["history"].([]any)
	if len(commits) == 0 || !strings.HasPrefix(commits[0].(map[string]any)["subject"].(string), "ai: ") {
		t.Fatalf("history = %v", hist)
	}
	loadedChat := decode[jsonMap](t, c.do("GET", "/api/chats/load?path="+url.QueryEscape(chatPath), nil))
	meta := loadedChat["meta"].(map[string]any)
	if meta["status"] != "inbox" {
		t.Fatalf("chat status = %v, want inbox", meta["status"])
	}
}
