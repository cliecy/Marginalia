package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// Handler answers JSON-RPC messages. ok is false when the message needs no
// reply (notifications). *Server implements it.
type Handler interface {
	Handle(message json.RawMessage) (reply []byte, ok bool)
}

// HandlerFunc adapts an ordinary function to Handler.
type HandlerFunc func(message json.RawMessage) (reply []byte, ok bool)

// Handle calls f(message).
func (f HandlerFunc) Handle(message json.RawMessage) ([]byte, bool) { return f(message) }

// RunStdio serves h over the MCP stdio transport: one JSON-RPC message (or
// batch) per line on in, one reply per line on out. Blank lines are ignored,
// notifications produce no output, and a line longer than MaxMessageBytes
// is answered with an "invalid request" error. Messages are handled one at
// a time, in order. out carries protocol messages only; diagnostics belong
// on stderr (the server logs through slog).
//
// RunStdio returns nil when in reaches EOF, ctx.Err() when ctx is cancelled
// and otherwise the first read or write error. After a cancellation, a
// reader blocked in in.Read is abandoned until that call returns.
func RunStdio(ctx context.Context, in io.Reader, out io.Writer, h Handler) error {
	return serveLines(ctx, in, out, func(_ context.Context, line []byte) ([]byte, bool) {
		return h.Handle(line)
	})
}

// line is one message read from the input.
type line struct {
	data    []byte
	tooLong bool
	err     error // terminal: io.EOF or a read error (data may still hold a final line)
}

// serveLines runs the newline-delimited loop shared by RunStdio and Bridge.
func serveLines(ctx context.Context, in io.Reader, out io.Writer, handle func(context.Context, []byte) ([]byte, bool)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lines := make(chan line)
	go readLines(ctx, in, lines)
	for {
		var l line
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l = <-lines:
		}
		if msg := trimJSONSpace(l.data); len(msg) > 0 || l.tooLong {
			var reply []byte
			ok := true
			if l.tooLong {
				reply = errorReply(nil, codeInvalidRequest, "message too large")
			} else {
				reply, ok = handle(ctx, msg)
			}
			if ok && len(reply) > 0 {
				if err := writeLine(out, reply); err != nil {
					return err
				}
			}
		}
		if l.err != nil {
			if errors.Is(l.err, io.EOF) {
				return nil
			}
			return l.err
		}
	}
}

// readLines sends every line of in to lines; the last value sent carries
// the terminal error. It stops early when ctx is cancelled.
func readLines(ctx context.Context, in io.Reader, lines chan<- line) {
	r := bufio.NewReaderSize(in, 64<<10)
	for {
		data, tooLong, err := readLine(r, MaxMessageBytes)
		select {
		case lines <- line{data: data, tooLong: tooLong, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// readLine reads up to and including the next '\n'. Lines longer than max
// bytes are consumed but not kept (tooLong).
func readLine(r *bufio.Reader, max int) (data []byte, tooLong bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(data)+len(chunk) > max+2 { // allow for "\r\n"
				tooLong, data = true, nil
			} else {
				data = append(data, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return data, tooLong, err
	}
}

// writeLine writes reply followed by a newline in a single Write call.
// Replies containing line breaks (pretty-printed JSON from a foreign
// handler or upstream) are compacted first so that framing stays intact.
func writeLine(out io.Writer, reply []byte) error {
	if bytes.ContainsAny(reply, "\r\n") {
		var compact bytes.Buffer
		if err := json.Compact(&compact, reply); err == nil {
			reply = compact.Bytes()
		}
	}
	buf := make([]byte, 0, len(reply)+1)
	buf = append(append(buf, reply...), '\n')
	_, err := out.Write(buf)
	return err
}
