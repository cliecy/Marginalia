package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	status, gate := f.status, f.gate
	reply := ""
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	if status != 200 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
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
