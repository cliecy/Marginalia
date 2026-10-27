package ai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"marginalia/internal/vault/pystr"
)

// maxStreamLine bounds a single line of a completion stream.
const maxStreamLine = 16 << 20

// streamChunk is the part of a chat.completion.chunk the client reads.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content any `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}

// readStream parses an OpenAI server-sent event stream the way the Python
// client did: every line starting with "data:" carries a JSON chunk (lines
// that do not parse are skipped), "[DONE]" ends the stream, and every
// non-empty choices[].delta.content string is passed to onDelta.
//
// Unlike the Python client, a chunk that carries an "error" object and no
// choices ends the stream with that error instead of being ignored.
//
// Errors from onDelta are returned unchanged; read errors go through
// readErr, which turns them into user-facing errors.
func readStream(body io.Reader, onDelta func(string) error, readErr func(error) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxStreamLine)
	scanner.Split(scanLines)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := pystr.Strip(line[len("data:"):])
		if data == "[DONE]" {
			return nil
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			if msg := streamError(chunk.Error); msg != "" {
				return &Error{Message: "AI 返回错误：" + msg}
			}
		}
		for _, choice := range chunk.Choices {
			if delta, ok := choice.Delta.Content.(string); ok && delta != "" {
				if err := onDelta(delta); err != nil {
					return err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return readErr(err)
	}
	return nil
}

// streamError extracts a message from the "error" member of a chunk:
// {"message": …}, a plain string, or any other non-null JSON value.
func streamError(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("false")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var obj struct {
		Message any `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if msg, ok := obj.Message.(string); ok && msg != "" {
			return msg
		}
	}
	return string(raw)
}

// scanLines splits like httpx's line decoder (and Python's str.splitlines
// for the characters that matter here): "\n", "\r\n" and a lone "\r" all end
// a line.
func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		switch {
		case i+1 < len(data) && data[i+1] == '\n':
			return i + 2, data[:i], nil
		case i+1 < len(data) || atEOF:
			return i + 1, data[:i], nil
		default:
			return 0, nil, nil // a "\n" may follow the "\r"
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// SSEData encodes v as one server-sent event: "data: <json>\n\n". Like the
// Python backend (json.dumps(ensure_ascii=False)) it leaves non-ASCII text
// and HTML characters unescaped.
func SSEData(v any) ([]byte, error) {
	data, err := marshalJSON(v)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(data)+8)
	out = append(out, "data: "...)
	out = append(out, data...)
	return append(out, '\n', '\n'), nil
}

// marshalJSON is json.Marshal without HTML escaping.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
