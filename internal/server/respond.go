package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"marginalia/internal/vault"
)

// Request body limits.
const (
	// maxJSONBody bounds JSON request bodies of authenticated routes. A note
	// may hold 20 million characters (the Python limit), which is up to 80 MB
	// of UTF-8.
	maxJSONBody = 96 << 20
	// maxLoginBody bounds the login body: a password of 512 characters fits
	// even when every character is a JSON-escaped surrogate pair.
	maxLoginBody = 8 << 10
	// maxPublicBody bounds the body of any request NetHandler could not
	// authenticate, so no public route can be made to buffer large bodies.
	maxPublicBody = 64 << 10
	// publicReadTimeout bounds how long NetHandler waits for the body of an
	// unauthenticated request (a client trickling bytes holds a goroutine).
	publicReadTimeout = 30 * time.Second
)

// apiError is an error with an HTTP status and the {"detail": …} body the
// Python backend (FastAPI) answered with. Code is an optional machine code
// for the frontend ("no_vault"…); Extra adds fields to the body.
type apiError struct {
	Status int
	Detail string
	Code   string
	Extra  map[string]any
}

func (e *apiError) Error() string { return e.Detail }

// errorf builds an apiError.
func errorf(status int, format string, args ...any) *apiError {
	return &apiError{Status: status, Detail: fmt.Sprintf(format, args...)}
}

// Errors shared by many routes.
var (
	errLoginRequired = &apiError{Status: http.StatusUnauthorized, Detail: "login required"}
	errNoVault       = &apiError{Status: http.StatusConflict, Detail: "未选择笔记库", Code: "no_vault"}
	errAppOnly       = &apiError{Status: http.StatusForbidden, Detail: "外部程序不能修改应用设置：请在 Marginalia 里操作", Code: "app_only"}
	errOrigin        = &apiError{Status: http.StatusForbidden, Detail: "origin is not allowed"}
	errNotFound      = &apiError{Status: http.StatusNotFound, Detail: "Not Found"}
)

// writeJSON writes v as JSON with status. Like Starlette's JSONResponse the
// output is compact and leaves non-ASCII and HTML characters unescaped.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := marshalJSON(v)
	if err != nil {
		slog.Error("encode response", "err", err)
		status, data = http.StatusInternalServerError, []byte(`{"detail":"Internal Server Error"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
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

// writeError answers err: an *apiError with its own status, a vault error
// with vault.StatusOf (400/404/409), anything else with 500.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var api *apiError
	var verr *vault.Error
	switch {
	case errors.As(err, &api):
	case errors.As(err, &verr):
		api = &apiError{Status: vault.StatusOf(err), Detail: err.Error()}
	default:
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		api = &apiError{Status: http.StatusInternalServerError, Detail: "Internal Server Error"}
	}
	body := map[string]any{"detail": api.Detail}
	if api.Code != "" {
		body["code"] = api.Code
	}
	for k, v := range api.Extra {
		body[k] = v
	}
	if api.Status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Marginalia"`)
	}
	writeJSON(w, api.Status, body)
}

// decodeJSON reads a JSON request body into dst. Malformed bodies answer
// 422 like FastAPI's validation errors (with a string detail), oversized
// ones 413, empty ones 422.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeBody(w, r, dst, false, maxJSONBody)
}

// decodeSmallJSON is decodeJSON with a smaller body limit.
func decodeSmallJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	return decodeBody(w, r, dst, false, limit)
}

// decodeOptionalJSON is decodeJSON for endpoints whose body is optional:
// an empty body leaves dst unchanged. (The Content-Length cannot tell: the
// desktop webview may send bodies without it.)
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeBody(w, r, dst, true, maxJSONBody)
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any, optional bool, limit int64) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errorf(http.StatusRequestEntityTooLarge, "请求太大")
		}
		return errorf(http.StatusBadRequest, "无法读取请求：%v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if optional {
			return nil
		}
		return errorf(http.StatusUnprocessableEntity, "request body is required")
	}
	if err := json.Unmarshal(data, dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			return errorf(http.StatusUnprocessableEntity, "invalid value for %s: expected %s", typeErr.Field, typeErr.Type)
		}
		return errorf(http.StatusUnprocessableEntity, "invalid JSON body: %v", err)
	}
	return nil
}

// required reports a missing body field (FastAPI: 422 "field required").
func required(name string) error {
	return errorf(http.StatusUnprocessableEntity, "field required: %s", name)
}

// maxChars rejects a string field longer than limit characters (pydantic
// max_length counts code points).
func maxChars(name, value string, limit int) error {
	if len(value) > limit && utf8.RuneCountInString(value) > limit {
		return errorf(http.StatusUnprocessableEntity, "%s must have at most %d characters", name, limit)
	}
	return nil
}

// queryParam returns a required query parameter (422 when missing).
func queryParam(r *http.Request, name string) (string, error) {
	values, ok := r.URL.Query()[name]
	if !ok || len(values) == 0 {
		return "", errorf(http.StatusUnprocessableEntity, "missing query parameter: %s", name)
	}
	return values[0], nil
}

// queryBool parses an optional boolean query parameter with pydantic's
// rules (true/false, 1/0, yes/no, on/off, t/f, y/n; case-insensitive).
func queryBool(r *http.Request, name string, def bool) (bool, error) {
	values, ok := r.URL.Query()[name]
	if !ok || len(values) == 0 {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(values[0])) {
	case "1", "true", "yes", "on", "t", "y":
		return true, nil
	case "0", "false", "no", "off", "f", "n":
		return false, nil
	}
	return false, errorf(http.StatusUnprocessableEntity, "query parameter %s must be a boolean", name)
}

// orEmpty returns s, or an empty (non-nil) slice so it encodes as [].
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
