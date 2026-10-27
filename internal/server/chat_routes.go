package server

import (
	"net/http"

	"marginalia/internal/chats"
	"marginalia/internal/knowledge/pyyaml"
	"marginalia/internal/vault/pystr"
)

// GET /api/chats: saved conversations, newest first.
func (s *Server) listChats(w http.ResponseWriter, _ *http.Request, st *state) error {
	writeJSON(w, http.StatusOK, map[string]any{"chats": orEmpty(chats.List(st.vault))})
	return nil
}

// GET /api/chats/load?path=
func (s *Server) loadChat(w http.ResponseWriter, r *http.Request, st *state) error {
	path, err := queryParam(r, "path")
	if err != nil {
		return err
	}
	loaded, err := chats.Load(st.vault, path)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, loaded)
	return nil
}

// PUT /api/chats {path?, title, model, messages, base_etag?}: save a
// conversation into Chats/ (a new file when path is empty).
func (s *Server) saveChat(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Path     *string          `json:"path"`
		Title    string           `json:"title"`
		Model    string           `json:"model"`
		Messages *[]chats.Message `json:"messages"`
		BaseETag *string          `json:"base_etag"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Messages == nil {
		return required("messages")
	}
	rel := ""
	if body.Path != nil {
		rel = *body.Path
	}
	saved, err := chats.Save(st.vault, rel, body.Title, body.Model, *body.Messages, body.BaseETag)
	if err != nil {
		return err
	}
	st.record([]string{saved.Path}, pystr.Head("chat: "+pyyaml.Str(saved.Meta.Value("title")), 120), false)
	writeJSON(w, http.StatusOK, saved)
	return nil
}
