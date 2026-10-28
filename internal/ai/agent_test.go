package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"marginalia/internal/vault"
	"marginalia/internal/vault/vaulttest"
)

// agentVault is a small vault for end-to-end agent runs.
func agentVault(t *testing.T) *vault.Vault {
	t.Helper()
	v, err := vault.New(vaulttest.Strings(t, map[string]string{
		"AGENTS.md":         "# 约定\n\n笔记放在 Knowledge/。\n",
		"index.md":          "# 知识地图\n\n- [[b]]\n",
		"Knowledge/b.md":    "# B\n\nold text\n",
		"Inbox/a.md":        "# A\n\nsee [[b]]\n",
		"Inbox/old.md":      "# Old\n",
		"Knowledge/link.md": "[[a]] and [[old]]\n",
		"Chats/2026-10-27 0900 c.md": "---\ntype: chat\ntitle: c\nstatus: inbox\n---\n\n" +
			"<!-- chat:user -->\n### 🧑 我\n\nexplain b\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// planServer answers planning requests with plan and writing requests with
// the note's path as its content, wrapped in a markdown fence, streaming
// every answer in small pieces.
func planServer(t *testing.T, plan string) *fakeOpenAI {
	t.Helper()
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, body chatRequest) {
		if len(body.Messages) != 2 {
			http.Error(w, "want 2 messages", http.StatusBadRequest)
			return
		}
		var answer string
		switch body.Messages[0].Content {
		case PlanPrompt:
			answer = "计划如下：\n```json\n" + plan + "\n```"
		case WritePrompt:
			_, note, _ := strings.Cut(body.Messages[1].Content, "## 你要写的笔记：")
			note, _, _ = strings.Cut(note, "\n")
			answer = "```markdown\n# " + note + "\n\nwritten by AI\n```"
		default:
			http.Error(w, "unexpected system prompt", http.StatusBadRequest)
			return
		}
		for runes := []rune(answer); len(runes) > 0; {
			n := min(len(runes), 7)
			writeSSE(w, deltaEvent(string(runes[:n])))
			runes = runes[n:]
		}
		writeSSE(w, "data: [DONE]\n\n")
	})
	return f
}

const e2ePlan = `{
  "summary": "整理 B",
  "operations": [
    {"op": "update", "path": "Knowledge/b.md", "reason": "补充", "instructions": "加入对话内容"},
    {"op": "create", "path": "Knowledge/new.md", "reason": "新主题"},
    {"op": "move", "path": "Inbox/a.md", "to": "Knowledge/a.md", "reason": "归位"},
    {"op": "delete", "path": "Inbox/old.md", "reason": "已合并"}
  ]
}`

// runAgent runs the agent and returns the SSE payloads it emitted.
func runAgent(t *testing.T, c Completer, v *vault.Vault, req AgentRequest) ([]string, error) {
	t.Helper()
	var payloads []string
	err := RunAgent(context.Background(), c, v, 200000, req, func(e Event) error {
		data, err := SSEData(e)
		if err != nil {
			return err
		}
		payload, ok := strings.CutPrefix(string(data), "data: ")
		if !ok || !strings.HasSuffix(payload, "\n\n") {
			t.Errorf("bad SSE frame %q", data)
		}
		payloads = append(payloads, strings.TrimSuffix(payload, "\n\n"))
		return nil
	})
	return payloads, err
}

func TestAgentEndToEnd(t *testing.T) {
	freezeClock(t, time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local))
	v := agentVault(t)
	f := planServer(t, e2ePlan)
	chat := "Chats/2026-10-27 0900 c.md"
	payloads, err := runAgent(t, f.client("magpie"), v, AgentRequest{Task: "整理", Chats: []string{chat}, Path: "Knowledge/b.md"})
	if err != nil {
		t.Fatal(err)
	}

	etag := func(rel string) string {
		_, data, err := v.ReadBytes(rel)
		if err != nil {
			t.Fatal(err)
		}
		return vault.ETag(data)
	}
	want := []string{
		`{"event":"status","message":"正在分析笔记库并制定修改计划…"}`,
		`{"event":"plan","summary":"整理 B","count":4}`,
		`{"event":"status","message":"（1/4）正在撰写 Knowledge/b.md…"}`,
		`{"event":"proposal","proposal":{"op":"update","path":"Knowledge/b.md","reason":"补充","before":"# B\n\nold text\n","base_etag":"` +
			etag("Knowledge/b.md") + `","after":"# Knowledge/b.md\n\nwritten by AI\n"}}`,
		`{"event":"status","message":"（2/4）正在撰写 Knowledge/new.md…"}`,
		`{"event":"proposal","proposal":{"op":"create","path":"Knowledge/new.md","reason":"新主题","before":"","base_etag":null,"after":"# Knowledge/new.md\n\nwritten by AI\n"}}`,
		`{"event":"proposal","proposal":{"op":"move","path":"Inbox/a.md","reason":"归位","before":"# A\n\nsee [[b]]\n","base_etag":"` +
			etag("Inbox/a.md") + `","to":"Knowledge/a.md"}}`,
		`{"event":"proposal","proposal":{"op":"delete","path":"Inbox/old.md","reason":"已合并","before":"# Old\n","base_etag":"` +
			etag("Inbox/old.md") + `"}}`,
		`{"event":"done","summary":"整理 B","chats":["Chats/2026-10-27 0900 c.md"]}`,
	}
	if len(payloads) != len(want) {
		t.Fatalf("got %d events:\n%s", len(payloads), strings.Join(payloads, "\n"))
	}
	for i := range want {
		// SSEData encodes newlines as \n escapes; compare as JSON text.
		var wantValue, gotValue any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(want[i], "\n", `\n`)), &wantValue); err != nil {
			t.Fatalf("want[%d]: %v", i, err)
		}
		if err := json.Unmarshal([]byte(payloads[i]), &gotValue); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Errorf("event %d:\n got %s\nwant %s", i, payloads[i], want[i])
		}
		// Key order matters for readability of the stream; check it too.
		if keys(t, payloads[i]) != keys(t, strings.ReplaceAll(want[i], "\n", `\n`)) {
			t.Errorf("event %d key order: %s", i, payloads[i])
		}
	}

	reqs := f.recorded()
	if len(reqs) != 3 {
		t.Fatalf("%d model requests, want 3 (plan + 2 notes)", len(reqs))
	}
	for _, r := range reqs {
		if r.Body.Model != "group/default" || !r.Body.Stream || r.Auth != "Bearer magpie" {
			t.Errorf("request model %q stream %v auth %q", r.Body.Model, r.Body.Stream, r.Auth)
		}
	}
	planUser := reqs[0].Body.Messages[1].Content
	for _, part := range []string{
		"今天是 2026-10-27。", "## 知识库约定\n# 约定", "## 笔记库\n", "# 笔记库索引（共 7 篇，路径、标题与摘要）",
		`<note path="Knowledge/b.md">`, "## 待整理的原始对话\n" + `<chat path="` + chat + `">`, "## 任务\n整理",
	} {
		if !strings.Contains(planUser, part) {
			t.Errorf("plan request lacks %q", part)
		}
	}
	// The focus note is the first full note in the context.
	if strings.Index(planUser, `<note path="Knowledge/b.md"`) > strings.Index(planUser, "<note path=") {
		t.Error("focus note is not ranked first")
	}
	writeUser := reqs[1].Body.Messages[1].Content
	for _, part := range []string{
		"## 本次整体任务\n整理\n\n## 本次修改计划\n整理 B\n- update Knowledge/b.md\n- create Knowledge/new.md\n- move Inbox/a.md\n- delete Inbox/old.md",
		"## 你要写的笔记：Knowledge/b.md\n要求：加入对话内容\n\n## 这篇笔记的当前内容\n" + `<note path="Knowledge/b.md">` + "\n# B\n\nold text\n</note>",
		"## 待整理的原始对话\n" + `<chat path="` + chat + `">`,
	} {
		if !strings.Contains(writeUser, part) {
			t.Errorf("write request lacks %q", part)
		}
	}
	// The writer does not carry the vault map: it writes the one note the
	// planner already chose.
	if strings.Contains(writeUser, "## 笔记库索引") {
		t.Error("write request carries the vault map")
	}
	if !strings.HasSuffix(reqs[2].Body.Messages[1].Content, "要求：新主题\n\n## 这是一篇新笔记") {
		t.Errorf("create request ends %q", reqs[2].Body.Messages[1].Content[len(reqs[2].Body.Messages[1].Content)-60:])
	}

	// The vault is untouched until the changeset is applied.
	if _, err := os.Stat(filepath.Join(v.Root(), "Knowledge", "new.md")); !errors.Is(err, os.ErrNotExist) {
		t.Error("RunAgent wrote to the vault")
	}

	// Apply what the frontend sends back (proposals without "before").
	var ops []Proposal
	for _, p := range payloads {
		var e struct {
			Event    string          `json:"event"`
			Proposal json.RawMessage `json:"proposal"`
		}
		json.Unmarshal([]byte(p), &e)
		if e.Event != EventProposal {
			continue
		}
		var op map[string]any
		json.Unmarshal(e.Proposal, &op)
		delete(op, "before")
		raw, _ := json.Marshal(op)
		var decoded Proposal
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, decoded)
	}
	touched, err := ApplyChangeset(v, ops)
	if err != nil {
		t.Fatal(err)
	}
	wantTouched := []string{"Knowledge/b.md", "Knowledge/new.md", "Inbox/a.md", "Knowledge/a.md", "Inbox/old.md"}
	if !reflect.DeepEqual(touched, wantTouched) {
		t.Errorf("touched = %q, want %q", touched, wantTouched)
	}
	for rel, want := range map[string]string{
		"Knowledge/b.md":   "# Knowledge/b.md\n\nwritten by AI\n",
		"Knowledge/new.md": "# Knowledge/new.md\n\nwritten by AI\n",
		"Knowledge/a.md":   "# A\n\nsee [[b]]\n",
	} {
		if got, err := os.ReadFile(filepath.Join(v.Root(), filepath.FromSlash(rel))); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", rel, got, err)
		}
	}
	for _, gone := range []string{"Inbox/a.md", "Inbox/old.md"} {
		if _, err := os.Stat(filepath.Join(v.Root(), filepath.FromSlash(gone))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", gone)
		}
	}
	if got := WrittenPaths(ops); !reflect.DeepEqual(got, []string{"Knowledge/b.md", "Knowledge/new.md", "Knowledge/a.md"}) {
		t.Errorf("WrittenPaths = %q", got)
	}
}

// keys returns the object keys of a JSON text in document order.
func keys(t *testing.T, text string) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(text))
	var out []string
	var walk func()
	walk = func() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("keys(%s): %v", text, err)
		}
		switch tok {
		case json.Delim('{'):
			for dec.More() {
				key, _ := dec.Token()
				out = append(out, key.(string))
				walk()
			}
			dec.Token()
		case json.Delim('['):
			for dec.More() {
				walk()
			}
			dec.Token()
		}
	}
	walk()
	return strings.Join(out, ",")
}

func TestAgentModelError(t *testing.T) {
	v := agentVault(t)
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	})
	payloads, err := runAgent(t, f.client("k"), v, AgentRequest{Task: "x"})
	var aiErr *Error
	if !errors.As(err, &aiErr) || aiErr.Message != "AI 请求失败（HTTP 500）：upstream exploded\n" {
		t.Errorf("err = %v", err)
	}
	if len(payloads) != 1 {
		t.Errorf("events before the failure: %q", payloads)
	}
	data, _ := json.Marshal(ErrorEvent(err))
	if string(data) != `{"event":"error","message":"AI 请求失败（HTTP 500）：upstream exploded\n"}` {
		t.Errorf("ErrorEvent = %s", data)
	}
}

func TestAgentStopsWhenEmitFails(t *testing.T) {
	v := agentVault(t)
	f := planServer(t, e2ePlan)
	gone := errors.New("client disconnected")
	var kinds []string
	err := RunAgent(context.Background(), f.client("k"), v, 200000, AgentRequest{Task: "x"}, func(e Event) error {
		kinds = append(kinds, e.Kind)
		if e.Kind == EventPlan {
			return gone
		}
		return nil
	})
	if !errors.Is(err, gone) {
		t.Errorf("err = %v", err)
	}
	if !reflect.DeepEqual(kinds, []string{EventStatus, EventPlan}) {
		t.Errorf("events = %q", kinds)
	}
	if n := len(f.recorded()); n != 1 {
		t.Errorf("%d model requests after the client left, want 1", n)
	}
}

func TestAgentCanceled(t *testing.T) {
	v := agentVault(t)
	f := planServer(t, e2ePlan)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := RunAgent(ctx, f.client("k"), v, 200000, AgentRequest{Task: "x"}, func(e Event) error {
		if e.Kind == EventPlan {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestEventJSON(t *testing.T) {
	for _, c := range []struct {
		event Event
		want  string
	}{
		{Event{Kind: EventPlan}, `{"event":"plan","summary":"","count":0}`},
		{Event{Kind: EventDone}, `{"event":"done","summary":"","chats":[]}`},
		{Event{Kind: EventError, Message: "坏了"}, `{"event":"error","message":"坏了"}`},
		{Event{Kind: EventProposal, Proposal: &Proposal{Op: OpDelete, Path: "a.md"}},
			`{"event":"proposal","proposal":{"op":"delete","path":"a.md","reason":"","before":"","base_etag":null}}`},
	} {
		data, err := json.Marshal(c.event)
		if err != nil || string(data) != c.want {
			t.Errorf("Marshal(%+v) = %s, %v; want %s", c.event, data, err, c.want)
		}
	}
	if _, err := json.Marshal(Event{Kind: "bogus"}); err == nil {
		t.Error("unknown kind marshalled")
	}
}

func TestExtractJSONValues(t *testing.T) {
	plan, err := ExtractJSON("```json\n{\"summary\": \"s\", \"operations\": [{\"op\": \"move\"}, 3, \"x\"]}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if ops := planOperations(plan); len(ops) != 1 || ops[0].Value("op") != "move" {
		t.Errorf("operations = %v", ops)
	}
	for _, bad := range []string{`{"operations": "abc"}`, `{"operations": {"op": "x"}}`, `{"operations": 5}`, `{}`} {
		plan, err := ExtractJSON(bad)
		if err != nil {
			t.Fatal(err)
		}
		if ops := planOperations(plan); len(ops) != 0 {
			t.Errorf("%s: operations = %v", bad, ops)
		}
	}
}
