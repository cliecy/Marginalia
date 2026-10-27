package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// pipeSession runs RunStdio (or Bridge) over io.Pipes and lets the test
// talk to it like an MCP client.
type pipeSession struct {
	t     *testing.T
	in    *io.PipeWriter
	out   *bufio.Scanner
	done  chan error
	close func()
}

func startSession(t *testing.T, serve func(ctx context.Context, in io.Reader, out io.Writer) error) *pipeSession {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	s := &pipeSession{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1)}
	s.out.Buffer(nil, 1<<20)
	go func() {
		err := serve(ctx, inR, outW)
		outW.Close()
		s.done <- err
	}()
	s.close = func() { cancel(); inW.Close(); outR.Close() }
	t.Cleanup(s.close)
	return s
}

// send writes one raw line.
func (s *pipeSession) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatalf("write %q: %v", line, err)
	}
}

// recv reads the next output line.
func (s *pipeSession) recv() string {
	s.t.Helper()
	lines := make(chan string, 1)
	go func() {
		if s.out.Scan() {
			lines <- s.out.Text()
		} else {
			lines <- "<EOF>"
		}
	}()
	select {
	case l := <-lines:
		return l
	case <-time.After(10 * time.Second):
		s.t.Fatal("timed out waiting for a reply")
		return ""
	}
}

// finish closes stdin and returns RunStdio's result.
func (s *pipeSession) finish() error {
	s.t.Helper()
	s.in.Close()
	// Drain anything left so the server can exit.
	for s.recv() != "<EOF>" {
	}
	select {
	case err := <-s.done:
		return err
	case <-time.After(10 * time.Second):
		s.t.Fatal("server did not stop at EOF")
		return nil
	}
}

func TestStdioRoundTrip(t *testing.T) {
	v, _ := pythonVault(t)
	srv := New(v, nil, Options{Version: "9.9.9"})
	s := startSession(t, func(ctx context.Context, in io.Reader, out io.Writer) error {
		return RunStdio(ctx, in, out, srv)
	})

	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	var init struct {
		ID     int              `json:"id"`
		Result initializeResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(s.recv()), &init); err != nil || init.ID != 1 ||
		init.Result.ProtocolVersion != "2025-03-26" || init.Result.ServerInfo.Version != "9.9.9" {
		t.Fatalf("initialize = %+v (%v)", init, err)
	}

	// Notifications and blank lines produce nothing: the next line out
	// answers the ping.
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	s.send(``)
	s.send("   \r")
	s.send(`{"jsonrpc":"2.0","id":"p","method":"ping"}` + "\r")
	if got := s.recv(); got != `{"jsonrpc":"2.0","id":"p","result":{}}` {
		t.Errorf("ping = %s", got)
	}

	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_note","arguments":{"path":"sub/B.md"}}}`)
	var read struct {
		Result toolResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(s.recv()), &read); err != nil || !strings.HasSuffix(read.Result.text(), "\n\n# B\nback to [[A|alias]]\n") {
		t.Errorf("read_note = %+v (%v)", read, err)
	}

	s.send(`[{"jsonrpc":"2.0","id":3,"method":"ping"},{"jsonrpc":"2.0","method":"n"},{"jsonrpc":"2.0","id":4,"method":"ping"}]`)
	if got := s.recv(); got != `[{"jsonrpc":"2.0","id":3,"result":{}},{"jsonrpc":"2.0","id":4,"result":{}}]` {
		t.Errorf("batch = %s", got)
	}

	s.send(`{"jsonrpc":"2.0","id":5,`)
	if got := s.recv(); got != `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}` {
		t.Errorf("parse error = %s", got)
	}

	// A final message without a trailing newline is still answered.
	if _, err := io.WriteString(s.in, `{"jsonrpc":"2.0","id":6,"method":"ping"}`); err != nil {
		t.Fatal(err)
	}
	s.in.Close()
	if got := s.recv(); got != `{"jsonrpc":"2.0","id":6,"result":{}}` {
		t.Errorf("last line = %s", got)
	}
	if err := s.finish(); err != nil {
		t.Errorf("RunStdio at EOF = %v, want nil", err)
	}
}

func TestStdioCompactsMultiLineReplies(t *testing.T) {
	pretty := HandlerFunc(func(json.RawMessage) ([]byte, bool) {
		return []byte("{\n  \"jsonrpc\": \"2.0\",\n  \"id\": 1,\n  \"result\": {}\n}"), true
	})
	var out bytes.Buffer
	if err := RunStdio(context.Background(), strings.NewReader("{}\n"), &out, pretty); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n" {
		t.Errorf("output = %q", got)
	}
}

func TestStdioCancel(t *testing.T) {
	inR, inW := io.Pipe() // never written: the reader blocks
	defer inW.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunStdio(ctx, inR, io.Discard, New(nil, nil, Options{})) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("RunStdio after cancel = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunStdio ignored the cancellation")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestStdioWriteError(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	err := RunStdio(context.Background(), in, failingWriter{}, New(nil, nil, Options{}))
	if err == nil || err.Error() != "broken pipe" {
		t.Errorf("RunStdio = %v, want the write error", err)
	}
}

type failingReader struct{ data *strings.Reader }

func (r failingReader) Read(p []byte) (int, error) {
	if r.data.Len() == 0 {
		return 0, errors.New("read failed")
	}
	return r.data.Read(p)
}

func TestStdioReadError(t *testing.T) {
	var out bytes.Buffer
	in := failingReader{strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")}
	err := RunStdio(context.Background(), in, &out, New(nil, nil, Options{}))
	if err == nil || err.Error() != "read failed" {
		t.Errorf("RunStdio = %v, want the read error", err)
	}
	if out.String() != `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n" {
		t.Errorf("output before the error = %q", out.String())
	}
}

func TestReadLineLimit(t *testing.T) {
	input := "short\n" + strings.Repeat("x", 100) + "\nnext\r\n" + strings.Repeat("y", 40) + "\nlast"
	r := bufio.NewReaderSize(strings.NewReader(input), 16)
	want := []struct {
		data    string
		tooLong bool
		eof     bool
	}{
		{"short\n", false, false},
		{"", true, false},
		{"next\r\n", false, false},
		{strings.Repeat("y", 40) + "\n", false, false},
		{"last", false, true},
	}
	for i, w := range want {
		data, tooLong, err := readLine(r, 40)
		if string(data) != w.data || tooLong != w.tooLong || (err == io.EOF) != w.eof || (err != nil && err != io.EOF) {
			t.Errorf("line %d = %q %v %v, want %q %v eof=%v", i, data, tooLong, err, w.data, w.tooLong, w.eof)
		}
	}
}
