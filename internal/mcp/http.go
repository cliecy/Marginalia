package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// MaxMessageBytes bounds one JSON-RPC message (an HTTP request body or a
// stdio line). Whole conversations fit comfortably.
const MaxMessageBytes = 64 << 20

// HTTPHandler serves h as the MCP Streamable HTTP endpoint, the way
// the original server did: POST bodies are JSON-RPC messages or batches,
// answered with application/json; "no reply" is 202 Accepted with an empty
// body; malformed JSON is 400 with a -32700 error. GET (no server-initiated
// stream) and every other method answer 405 with "Allow: POST".
//
// Authentication is not part of this handler: wrap it with the server's
// Bearer / ?token= check.
func HTTPHandler(h Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxMessageBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeJSON(w, http.StatusRequestEntityTooLarge, errorReply(nil, codeInvalidRequest, "message too large"))
				return
			}
			writeJSON(w, http.StatusBadRequest, parseErrorReply())
			return
		}
		if !json.Valid(trimJSONSpace(body)) {
			writeJSON(w, http.StatusBadRequest, parseErrorReply())
			return
		}
		reply, ok := h.Handle(body)
		if !ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, reply)
	})
}

// ServeHTTP makes the server usable as an http.Handler; see HTTPHandler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	HTTPHandler(s).ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
