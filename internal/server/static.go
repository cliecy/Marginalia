package server

import (
	"io"
	"net/http"
	"path"
	"strings"

	"marginalia/internal/webui"
)

// spa serves the web UI: existing files as they are, every other path
// index.html (client-side routing), like the Python catch-all route. Hashed
// build assets are cached for a year; index.html never.
func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, &apiError{Status: http.StatusMethodNotAllowed, Detail: "Method Not Allowed"})
		return
	}
	if s.static == nil {
		webui.FallbackHandler().ServeHTTP(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name != "" && name != "index.html" && !hiddenPath(name) {
		if s.serveFile(w, r, name) {
			return
		}
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r) // a stale hashed asset: never answer it with HTML
			return
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	if !s.serveFile(w, r, "index.html") {
		webui.FallbackHandler().ServeHTTP(w, r)
	}
}

// sandbox serves /sandbox.html, the page AI "visual" blocks run in. Its CSP
// is set by securityHeaders.
func (s *Server) sandbox(w http.ResponseWriter, r *http.Request) {
	if s.static == nil || !s.serveFile(w, r, "sandbox.html") {
		http.NotFound(w, r)
	}
}

// serveFile serves a regular file of the UI build and reports whether it
// existed. Range requests and conditional GETs are handled by
// http.ServeContent.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := s.static.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	if name == "index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	content, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		content = strings.NewReader(string(data))
	}
	// Embedded files have a zero ModTime: ServeContent then sends no
	// Last-Modified.
	http.ServeContent(w, r, name, info.ModTime(), content)
	return true
}

// hiddenPath reports whether a UI path has a dot-prefixed segment (the
// committed dist/.keep, editor files…), which are never served.
func hiddenPath(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
