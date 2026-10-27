package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"marginalia/internal/config"
)

// fakeOpenAI is an OpenAI-compatible test server. /v1/models answers with
// models (or modelsStatus); /v1/chat/completions is handled by chat.
type fakeOpenAI struct {
	*httptest.Server

	mu           sync.Mutex
	requests     []fakeRequest
	models       string
	modelsStatus int
	chat         func(w http.ResponseWriter, r *http.Request, body chatRequest)
}

type fakeRequest struct {
	Method, Path, Auth, ContentType, UserAgent string
	Body                                       chatRequest
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{models: `{"object":"list","data":[]}`}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeOpenAI) serve(w http.ResponseWriter, r *http.Request) {
	rec := fakeRequest{
		Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"),
		ContentType: r.Header.Get("Content-Type"), UserAgent: r.Header.Get("User-Agent"),
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&rec.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	f.mu.Lock()
	f.requests = append(f.requests, rec)
	chat, models, status := f.chat, f.models, f.modelsStatus
	f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		if status != 0 {
			http.Error(w, "nope", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, models)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" && chat != nil:
		w.Header().Set("Content-Type", "text/event-stream")
		chat(w, r, rec.Body)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOpenAI) setChat(fn func(w http.ResponseWriter, r *http.Request, body chatRequest)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chat = fn
}

func (f *fakeOpenAI) setModels(body string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models, f.modelsStatus = body, status
}

func (f *fakeOpenAI) recorded() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests...)
}

// client returns a Client for the fake server with the given key.
func (f *fakeOpenAI) client(key string) *Client {
	return NewClient(Options{
		BaseURL:      f.URL + "/v1/",
		APIKey:       func() string { return key },
		DefaultModel: "group/default",
	})
}

// writeSSE writes raw stream text and flushes it to the client.
func writeSSE(w http.ResponseWriter, text string) {
	io.WriteString(w, text)
	w.(http.Flusher).Flush()
}

// deltaEvent is one SSE event carrying content.
func deltaEvent(content string) string {
	data, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}},
	})
	return "data: " + string(data) + "\n\n"
}

// streamDeltas answers a completion with the given pieces and [DONE].
func streamDeltas(pieces ...string) func(http.ResponseWriter, *http.Request, chatRequest) {
	return func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		for _, p := range pieces {
			writeSSE(w, deltaEvent(p))
		}
		writeSSE(w, "data: [DONE]\n\n")
	}
}

func collect(t *testing.T, c *Client, model string, messages []Message) ([]string, error) {
	t.Helper()
	var deltas []string
	err := c.Stream(context.Background(), model, messages, func(d string) error {
		deltas = append(deltas, d)
		return nil
	})
	return deltas, err
}

func TestStreamParsesServerSentEvents(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		writeSSE(w, ": keep-alive\n\n")
		writeSSE(w, "event: message\n"+`data: {"choices":[{"delta":{"role":"assistant"}}]}`+"\n\n")
		writeSSE(w, `data: {"choices":[{"delta":{"content":"你好"}}]}`+"\r\n\r\n")
		writeSSE(w, "data:not json\n\n")
		writeSSE(w, `data: {"choices":[]}`+"\n\n")
		writeSSE(w, `data:{"choices":[{"delta":{"content":"，"}},{"delta":{"content":"世"}}]}`+"\n")
		half := `data: {"choices":[{"delta":{"content":"界"}}]}`
		writeSSE(w, half[:20]) // a line split across network writes
		writeSSE(w, half[20:]+"\n\n")
		writeSSE(w, `data: {"choices":[{"delta":{"content":null}},{"delta":{"content":["x"]}}]}`+"\n\n")
		writeSSE(w, `data: {"choices":[{"delta":{"content":"!"}, "finish_reason":"stop"}], "usage":{"total_tokens":3}}`+"\n\n")
		writeSSE(w, "data: [DONE]\n\n")
		writeSSE(w, deltaEvent("after done"))
	})
	messages := []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi <&>"}}
	deltas, err := collect(t, f.client("sk-test"), "", messages)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"你好", "，", "世", "界", "!"}; !reflect.DeepEqual(deltas, want) {
		t.Errorf("deltas = %q, want %q", deltas, want)
	}

	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "/v1/chat/completions" {
		t.Errorf("request %s %s", r.Method, r.Path)
	}
	if r.Auth != "Bearer sk-test" || r.ContentType != "application/json" || r.UserAgent != "Marginalia" {
		t.Errorf("headers: auth %q, content type %q, user agent %q", r.Auth, r.ContentType, r.UserAgent)
	}
	want := chatRequest{Model: "group/default", Messages: messages, Stream: true}
	if !reflect.DeepEqual(r.Body, want) {
		t.Errorf("body = %+v, want %+v", r.Body, want)
	}
}

func TestStreamUsesRequestedModel(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(streamDeltas("ok"))
	if _, err := collect(t, f.client("k"), "codex/gpt-6.1-sol", nil); err != nil {
		t.Fatal(err)
	}
	body := f.recorded()[0].Body
	if body.Model != "codex/gpt-6.1-sol" || body.Messages == nil || len(body.Messages) != 0 {
		t.Errorf("body = %+v", body)
	}
}

func TestCompleteJoinsDeltas(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(streamDeltas("# Ti", "tle\n", "", "body"))
	got, err := f.client("k").Complete(context.Background(), "m", []Message{{Role: RoleUser, Content: "x"}})
	if err != nil || got != "# Title\nbody" {
		t.Errorf("Complete = %q, %v", got, err)
	}
}

func TestStreamHTTPError(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, strings.Repeat("错", 400))
	})
	deltas, err := collect(t, f.client("k"), "m", nil)
	var aiErr *Error
	if !errors.As(err, &aiErr) {
		t.Fatalf("error %v (%T), want *Error", err, err)
	}
	want := "AI 请求失败（HTTP 502）：" + strings.Repeat("错", 300)
	if aiErr.Message != want || aiErr.StatusCode != http.StatusBadGateway || len(deltas) != 0 {
		t.Errorf("error = %q (status %d), deltas %q", aiErr.Message, aiErr.StatusCode, deltas)
	}
}

func TestStreamHTTPErrorInvalidUTF8(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate \xff limited"))
	})
	_, err := collect(t, f.client("k"), "m", nil)
	if err == nil || err.Error() != "AI 请求失败（HTTP 429）：rate � limited" {
		t.Errorf("error = %v", err)
	}
}

func TestStreamErrorChunk(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		writeSSE(w, deltaEvent("partial"))
		writeSSE(w, `data: {"error":{"message":"quota exceeded","type":"insufficient_quota"}}`+"\n\n")
		writeSSE(w, deltaEvent("never"))
	})
	deltas, err := collect(t, f.client("k"), "m", nil)
	if err == nil || err.Error() != "AI 返回错误：quota exceeded" {
		t.Errorf("error = %v", err)
	}
	if !reflect.DeepEqual(deltas, []string{"partial"}) {
		t.Errorf("deltas = %q", deltas)
	}
}

func TestStreamErrorValues(t *testing.T) {
	for raw, want := range map[string]string{
		`"plain"`:         "plain",
		`{"message":"m"}`: "m",
		`{"code":42}`:     `{"code":42}`,
		`null`:            "",
		`false`:           "",
		``:                "",
		`{"message":""}`:  `{"message":""}`,
	} {
		if got := streamError(json.RawMessage(raw)); got != want {
			t.Errorf("streamError(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestStreamCallbackErrorStops(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(streamDeltas("a", "b", "c"))
	stop := errors.New("client went away")
	calls := 0
	err := f.client("k").Stream(context.Background(), "m", nil, func(string) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("err = %v after %d calls", err, calls)
	}
}

func TestStreamContextCanceled(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, r *http.Request, _ chatRequest) {
		writeSSE(w, deltaEvent("first"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []string
	err := f.client("k").Stream(ctx, "m", nil, func(d string) error {
		got = append(got, d)
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v (%T), want context.Canceled", err, err)
	}
	if !reflect.DeepEqual(got, []string{"first"}) {
		t.Errorf("deltas = %q", got)
	}
}

func TestStreamReadTimeout(t *testing.T) {
	f := newFakeOpenAI(t)
	release := make(chan struct{})
	defer close(release)
	f.setChat(func(w http.ResponseWriter, r *http.Request, _ chatRequest) {
		writeSSE(w, deltaEvent("first"))
		select { // stall without closing the stream
		case <-r.Context().Done():
		case <-release:
		}
	})
	c := f.client("k")
	c.stream = timeouts{connect: time.Second, write: time.Second, read: 150 * time.Millisecond}
	start := time.Now()
	deltas, err := collect(t, c, "m", nil)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "AI 服务响应超时") {
		t.Errorf("err = %v, want a read timeout", err)
	}
	if !reflect.DeepEqual(deltas, []string{"first"}) {
		t.Errorf("deltas = %q", deltas)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v", elapsed)
	}
}

func TestStreamTimeoutBeforeHeaders(t *testing.T) {
	f := newFakeOpenAI(t)
	release := make(chan struct{})
	defer close(release)
	f.setChat(func(w http.ResponseWriter, r *http.Request, _ chatRequest) {
		select { // never send the headers
		case <-r.Context().Done():
		case <-release:
		}
	})
	c := f.client("k")
	c.stream = timeouts{connect: 50 * time.Millisecond, write: 50 * time.Millisecond, read: 100 * time.Millisecond}
	_, err := collect(t, c, "m", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a timeout", err)
	}
}

func TestStreamSlowButSteadyIsNotATimeout(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		for i := range 4 {
			time.Sleep(60 * time.Millisecond)
			writeSSE(w, deltaEvent(fmt.Sprint(i)))
		}
		writeSSE(w, "data: [DONE]\n\n")
	})
	c := f.client("k")
	// The whole answer takes longer than read, but no single wait does.
	c.stream = timeouts{connect: time.Second, write: time.Second, read: 150 * time.Millisecond}
	deltas, err := collect(t, c, "m", nil)
	if err != nil || strings.Join(deltas, "") != "0123" {
		t.Errorf("deltas %q, err %v", deltas, err)
	}
}

func TestConnectionRefused(t *testing.T) {
	f := newFakeOpenAI(t)
	c := f.client("k")
	f.Close()
	_, err := collect(t, c, "m", nil)
	var aiErr *Error
	if !errors.As(err, &aiErr) || !strings.HasPrefix(aiErr.Message, "无法连接 AI 服务（"+f.URL+"/v1）：") {
		t.Errorf("err = %v", err)
	}
	if _, err := c.Models(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "无法连接 AI 服务") {
		t.Errorf("Models err = %v", err)
	}
}

func TestAPIKeyIsReadPerRequestAndOptional(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(streamDeltas("ok"))
	var key atomic.Value
	key.Store("")
	var reads atomic.Int32
	c := NewClient(Options{BaseURL: f.URL + "/v1", APIKey: func() string {
		reads.Add(1)
		return key.Load().(string)
	}})
	if !c.Configured() {
		t.Error("a loopback endpoint without a key should count as configured")
	}
	if _, err := collect(t, c, "m", nil); err != nil {
		t.Fatal(err)
	}
	key.Store("rotated")
	if _, err := collect(t, c, "m", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.recorded()
	if reqs[0].Auth != "" {
		t.Errorf("Authorization %q sent without a key", reqs[0].Auth)
	}
	if reqs[1].Auth != "Bearer rotated" {
		t.Errorf("Authorization = %q after rotation", reqs[1].Auth)
	}
	if reads.Load() < 2 {
		t.Errorf("key read %d times", reads.Load())
	}
}

func TestNotConfigured(t *testing.T) {
	ctx := context.Background()
	remote := NewClient(Options{BaseURL: "https://api.example.com/v1", KeyFile: "/data/ai_api_key"})
	_, err := remote.Models(ctx)
	if !errors.Is(err, ErrNotConfigured) || err.Error() != "AI 未配置：请把 API Key 写入 /data/ai_api_key" {
		t.Errorf("remote without key: %v", err)
	}
	if remote.Configured() {
		t.Error("remote endpoint without key reported as configured")
	}
	err = remote.Stream(ctx, "m", nil, func(string) error { return nil })
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Stream: %v", err)
	}

	empty := NewClient(Options{APIKey: func() string { return "k" }})
	if _, err := empty.Complete(ctx, "m", nil); !errors.Is(err, ErrNotConfigured) || err.Error() != "AI 未配置：请先设置 AI 服务地址" {
		t.Errorf("no base URL: %v", err)
	}
	noFile := NewClient(Options{BaseURL: "https://api.example.com/v1"})
	if _, err := noFile.Models(ctx); err == nil || err.Error() != "AI 未配置：请先设置 API Key" {
		t.Errorf("no key file: %v", err)
	}
	withKey := NewClient(Options{BaseURL: "https://api.example.com/v1/", APIKey: func() string { return "k" }})
	if !withKey.Configured() || withKey.BaseURL() != "https://api.example.com/v1" {
		t.Errorf("Configured %v, BaseURL %q", withKey.Configured(), withKey.BaseURL())
	}
}

func TestNewFromSettings(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(streamDeltas("ok"))
	s := config.Defaults(config.ModeDesktop)
	s.DataDir = t.TempDir()
	s.AI.BaseURL = f.URL + "/v1"
	s.AI.DefaultModel = "group/gpt-sol-main"
	c := NewFromSettings(s)
	if c.DefaultModel() != "group/gpt-sol-main" {
		t.Errorf("DefaultModel = %q", c.DefaultModel())
	}
	if err := s.SetAIAPIKey("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, c, "", nil); err != nil {
		t.Fatal(err)
	}
	r := f.recorded()[0]
	if r.Auth != "Bearer magpie" || r.Body.Model != "group/gpt-sol-main" {
		t.Errorf("auth %q, model %q", r.Auth, r.Body.Model)
	}

	s.AI.BaseURL = "https://api.example.com/v1"
	if err := s.SetAIAPIKey(""); err != nil {
		t.Fatal(err)
	}
	want := "AI 未配置：请把 API Key 写入 " + filepath.Join(s.DataDir, "ai_api_key")
	if _, err := NewFromSettings(s).Models(context.Background()); err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// magpieModels is shaped like the /v1/models answer of Magpie's gateway,
// plus entries a generic provider might send.
const magpieModels = `{"object":"list","data":[
 {"id":"group/default","display_name":"default","magpie_label":"default · routing group","context_length":1000000,"owned_by":"llmhub","modalities":{"input":["text","image"]},"object":"model"},
 {"id":"codex/gpt-6.1-sol","display_name":"GPT-6.1-Sol","magpie_label":"GPT-6.1-Sol · Codex","context_length":1000000,"owned_by":"codex","modalities":{"input":["text","image"],"output":["text"]}},
 {"id":"llmhub/coder390","magpie_label":"coder390 · LLMHUB","modalities":null},
 {"id":"gpt-4o"},
 {"id":"gpt-image-1","display_name":"GPT Image"},
 {"id":"text-embedding-3-small"},
 {"id":"tts-1-hd"},
 {"id":"whisper-1"},
 {"id":"bge-reranker-v2"},
 {"id":"Sora-Video"},
 {"id":"FLUX-Image-Pro"},
 {"id":"painter","modalities":{"output":["image"]}},
 {"id":"group/default","display_name":"duplicate"},
 {"id":42},
 {"display_name":"no id"},
 "not an object",
 {"id":"  "}
]}`

func TestModels(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setModels(magpieModels, 0)
	models, err := f.client("magpie").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{
		{ID: "group/default", Label: "default", Detail: "default · routing group", OwnedBy: "llmhub", ContextLength: 1000000},
		{ID: "codex/gpt-6.1-sol", Label: "GPT-6.1-Sol", Detail: "GPT-6.1-Sol · Codex", OwnedBy: "codex", ContextLength: 1000000},
		{ID: "llmhub/coder390", Label: "coder390 · LLMHUB", Detail: "coder390 · LLMHUB"},
		{ID: "gpt-4o"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Errorf("Models =\n%+v\nwant\n%+v", models, want)
	}
	if ids := ModelIDs(models); !reflect.DeepEqual(ids, []string{"group/default", "codex/gpt-6.1-sol", "llmhub/coder390", "gpt-4o"}) {
		t.Errorf("ModelIDs = %q", ids)
	}
	wantLabels := map[string]string{"group/default": "default", "codex/gpt-6.1-sol": "GPT-6.1-Sol", "llmhub/coder390": "coder390 · LLMHUB"}
	if labels := ModelLabels(models); !reflect.DeepEqual(labels, wantLabels) {
		t.Errorf("ModelLabels = %v", labels)
	}
	r := f.recorded()[0]
	if r.Method != http.MethodGet || r.Path != "/v1/models" || r.Auth != "Bearer magpie" || r.ContentType != "" {
		t.Errorf("request %+v", r)
	}
}

func TestModelsEmptyAndErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeOpenAI(t)

	f.setModels(`{"object":"list"}`, 0)
	if models, err := f.client("k").Models(ctx); err != nil || models == nil || len(models) != 0 {
		t.Errorf("no data: %v, %v", models, err)
	}

	f.setModels(`<html>not json</html>`, 0)
	if _, err := f.client("k").Models(ctx); err == nil || !strings.HasPrefix(err.Error(), "AI 服务返回的模型列表无法解析：") {
		t.Errorf("bad JSON: %v", err)
	}

	f.setModels("", http.StatusUnauthorized)
	_, err := f.client("k").Models(ctx)
	var aiErr *Error
	if !errors.As(err, &aiErr) || aiErr.Message != "获取模型列表失败（HTTP 401）" || aiErr.StatusCode != 401 {
		t.Errorf("401: %v", err)
	}
}

func TestReadStreamLineEndings(t *testing.T) {
	text := "data: " + `{"choices":[{"delta":{"content":"a"}}]}` + "\r" +
		"data: " + `{"choices":[{"delta":{"content":"b"}}]}` + "\r\n" +
		"data: " + `{"choices":[{"delta":{"content":"c"}}]}` + "\n" +
		"data: " + `{"choices":[{"delta":{"content":"d"}}]}` // no final newline
	for name, r := range map[string]io.Reader{
		"whole":    strings.NewReader(text),
		"one byte": iotest.OneByteReader(strings.NewReader(text)),
	} {
		var got []string
		err := readStream(r, func(d string) error { got = append(got, d); return nil }, func(err error) error { return err })
		if err != nil || strings.Join(got, "") != "abcd" {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
}

func TestReadStreamReadError(t *testing.T) {
	broken := io.MultiReader(strings.NewReader(deltaEvent("x")), iotest.ErrReader(errors.New("connection reset")))
	var got []string
	err := readStream(broken, func(d string) error { got = append(got, d); return nil }, func(err error) error {
		return &Error{Message: "wrapped: " + err.Error(), Err: err}
	})
	if err == nil || err.Error() != "wrapped: connection reset" || !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("err %v, deltas %q", err, got)
	}
}

func TestStreamReadErrorMessage(t *testing.T) {
	f := newFakeOpenAI(t)
	f.setChat(func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		writeSSE(w, deltaEvent("x"))
		// Abort the connection mid-stream.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	_, err := collect(t, f.client("k"), "m", nil)
	var aiErr *Error
	if !errors.As(err, &aiErr) || !strings.HasPrefix(aiErr.Message, "读取 AI 响应失败：") {
		t.Errorf("err = %v", err)
	}
}

func TestSSEData(t *testing.T) {
	got, err := SSEData(map[string]any{"delta": "<b>&中文\n"})
	if err != nil || string(got) != "data: {\"delta\":\"<b>&中文\\n\"}\n\n" {
		t.Errorf("SSEData = %q, %v", got, err)
	}
	got, err = SSEData(Event{Kind: EventStatus, Message: "a<b"})
	if err != nil || string(got) != `data: {"event":"status","message":"a<b"}`+"\n\n" {
		t.Errorf("SSEData(event) = %q, %v", got, err)
	}
}
