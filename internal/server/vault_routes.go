package server

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"marginalia/internal/knowledge"
	"marginalia/internal/vault"
)

// inlineTypes are the media types /api/file shows in the browser instead of
// downloading (Python INLINE_TYPES; SVG is handled separately).
var inlineTypes = []string{"image/", "application/pdf", "audio/", "video/", "text/plain"}

// GET /api/tree
func (s *Server) tree(w http.ResponseWriter, _ *http.Request, st *state) error {
	writeJSON(w, http.StatusOK, map[string]any{"tree": st.vault.Tree()})
	return nil
}

// GET /api/note?path=
func (s *Server) readNote(w http.ResponseWriter, r *http.Request, st *state) error {
	path, err := queryParam(r, "path")
	if err != nil {
		return err
	}
	note, err := st.vault.ReadNote(path)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, note)
	return nil
}

// PUT /api/note {path, content, base_etag?}: save with optimistic locking;
// committed after the debounce delay.
func (s *Server) saveNote(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Path     *string `json:"path"`
		Content  *string `json:"content"`
		BaseETag *string `json:"base_etag"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Path == nil {
		return required("path")
	}
	if body.Content == nil {
		return required("content")
	}
	if err := maxChars("content", *body.Content, 20_000_000); err != nil {
		return err
	}
	result, err := st.vault.WriteNote(*body.Path, *body.Content, body.BaseETag)
	if err != nil {
		return err
	}
	st.record([]string{result.Path}, "web: edit "+result.Path, false)
	writeJSON(w, http.StatusOK, result)
	return nil
}

// POST /api/note {path, content="", folder=false}: create a note or folder.
func (s *Server) createNote(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Path    *string `json:"path"`
		Content string  `json:"content"`
		Folder  bool    `json:"folder"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Path == nil {
		return required("path")
	}
	result, err := st.vault.Create(*body.Path, body.Content, body.Folder)
	if err != nil {
		return err
	}
	if !body.Folder {
		st.record([]string{result.Path}, "web: create "+result.Path, false)
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

// POST /api/move {path, to, update_links=true}: rename a note or folder and
// rewrite the wikilinks pointing at it; committed at once.
func (s *Server) move(w http.ResponseWriter, r *http.Request, st *state) error {
	body := struct {
		Path        *string `json:"path"`
		To          *string `json:"to"`
		UpdateLinks bool    `json:"update_links"`
	}{UpdateLinks: true}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Path == nil {
		return required("path")
	}
	if body.To == nil {
		return required("to")
	}
	st.flush()
	result, err := st.vault.Move(*body.Path, *body.To, body.UpdateLinks)
	if err != nil {
		return err
	}
	paths := []string{result.Path}
	if src, err := st.vault.Clean(*body.Path); err == nil {
		paths = append([]string{src}, paths...)
	}
	paths = append(paths, result.UpdatedLinksIn...)
	st.record(paths, "web: move "+*body.Path+" -> "+result.Path, true)
	writeJSON(w, http.StatusOK, result)
	return nil
}

// DELETE /api/note?path=: delete a file or an empty folder; the latest
// content is committed first so history keeps it.
func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request, st *state) error {
	path, err := queryParam(r, "path")
	if err != nil {
		return err
	}
	st.flush()
	if err := st.vault.Delete(path); err != nil {
		return err
	}
	if clean, err := st.vault.Clean(path); err == nil {
		st.record([]string{clean}, "web: delete "+path, true)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// GET /api/file?path=&download=: a raw vault file. Inline types are shown
// in the browser; everything else downloads. Non-PDF files are sandboxed by
// CSP so an uploaded HTML or SVG file cannot run script in the app's origin.
func (s *Server) rawFile(w http.ResponseWriter, r *http.Request, st *state) error {
	rel, err := queryParam(r, "path")
	if err != nil {
		return err
	}
	download, err := queryBool(r, "download", false)
	if err != nil {
		return err
	}
	target, err := st.vault.Resolve(rel)
	if err != nil {
		return err
	}
	f, err := os.Open(target)
	if err != nil {
		return errorf(http.StatusNotFound, "file not found")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errorf(http.StatusNotFound, "file not found")
	}
	name := filepath.Base(target)
	media := vault.GuessType(name)
	inline := hasAnyPrefix(media, inlineTypes) && media != "image/svg+xml" && !download
	if strings.HasPrefix(media, "text/") && media != "text/plain" {
		media = "text/plain; charset=utf-8"
	}
	disposition := "attachment"
	if inline || media == "image/svg+xml" {
		disposition = "inline"
	}
	h := w.Header()
	h.Set("Content-Disposition", disposition+"; filename*=UTF-8''"+pyQuote(name))
	h.Set("Cache-Control", "private, max-age=300")
	if !strings.HasPrefix(media, "application/pdf") {
		h.Set("Content-Security-Policy", fileCSP)
	}
	if strings.HasPrefix(media, "text/") && !strings.Contains(strings.ToLower(media), "charset=") {
		media += "; charset=utf-8" // Starlette adds the charset to text types
	}
	h.Set("Content-Type", media)
	http.ServeContent(w, r, "", info.ModTime(), f)
	return nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// pyQuote is Python's urllib.parse.quote(s) (safe="/"): every byte except
// ASCII letters, digits, "_.-~" and "/" is percent-encoded.
func pyQuote(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '_', c == '.', c == '-', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

// multipartOverhead is the room left for multipart framing and the folder
// field on top of MaxUploadBytes.
const multipartOverhead = 1 << 20

// POST /api/upload (multipart: file, folder=""): store an attachment,
// by default in Settings.AssetsDir; 413 "文件太大" above MaxUploadBytes.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, st *state) error {
	limit := st.settings.MaxUploadBytes
	tooLarge := errorf(http.StatusRequestEntityTooLarge, "文件太大")
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return errorf(http.StatusUnprocessableEntity, "expected multipart/form-data with a file field")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+multipartOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		return errorf(http.StatusUnprocessableEntity, "invalid multipart body: %v", err)
	}
	var (
		data     []byte
		filename string
		haveFile bool
		folder   string
	)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return uploadReadError(err, tooLarge)
		}
		switch part.FormName() {
		case "file":
			data, err = io.ReadAll(io.LimitReader(part, limit+1))
			if err != nil {
				return uploadReadError(err, tooLarge)
			}
			if int64(len(data)) > limit {
				return tooLarge
			}
			filename, haveFile = part.FileName(), true
		case "folder":
			value, err := io.ReadAll(io.LimitReader(part, 4096))
			if err != nil {
				return uploadReadError(err, tooLarge)
			}
			folder = string(value)
		}
		_ = part.Close()
	}
	if !haveFile {
		return required("file")
	}
	if folder == "" {
		folder = st.settings.AssetsDir
	}
	if filename == "" {
		filename = "upload"
	}
	rel, err := st.vault.Upload(folder, filename, data)
	if err != nil {
		return err
	}
	st.record([]string{rel}, "web: upload "+rel, false)
	writeJSON(w, http.StatusOK, map[string]string{"path": rel})
	return nil
}

// uploadReadError maps a multipart read failure.
func uploadReadError(err error, tooLarge error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return tooLarge
	}
	return errorf(http.StatusBadRequest, "无法读取上传的文件：%v", err)
}

// GET /api/search?q=
func (s *Server) search(w http.ResponseWriter, r *http.Request, st *state) error {
	q, err := queryParam(r, "q")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": st.vault.Search(q, vault.DefaultSearchLimit)})
	return nil
}

// GET /api/backlinks?path=
func (s *Server) backlinks(w http.ResponseWriter, r *http.Request, st *state) error {
	path, err := queryParam(r, "path")
	if err != nil {
		return err
	}
	links, err := st.vault.Backlinks(path)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"backlinks": links})
	return nil
}

// GET /api/index
func (s *Server) index(w http.ResponseWriter, _ *http.Request, st *state) error {
	writeJSON(w, http.StatusOK, map[string]any{"notes": orEmpty(knowledge.VaultIndex(st.vault))})
	return nil
}

// GET /api/graph?chats=
func (s *Server) graph(w http.ResponseWriter, r *http.Request, st *state) error {
	chats, err := queryBool(r, "chats", false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, knowledge.Graph(st.vault, chats))
	return nil
}
