package server

import (
	"errors"
	"net/http"
	"strings"

	"marginalia/internal/ai"
	"marginalia/internal/chats"
	"marginalia/internal/knowledge"
	"marginalia/internal/vault"
	"marginalia/internal/vault/pystr"
)

// promptsDir holds the Obsidian Copilot custom prompts the AI panel offers.
const promptsDir = "copilot/copilot-custom-prompts"

// maxTaskChars bounds an agent task (Python AgentBody.task max_length).
const maxTaskChars = 20000

// GET /api/ai/models: the chat models of the configured service, with
// display labels (Magpie's names). A failure still answers 200 with the
// default model and the error, as in Python.
func (s *Server) models(w http.ResponseWriter, r *http.Request, st *state) error {
	def := st.settings.AI.DefaultModel
	models, err := st.ai.Models(r.Context())
	if err != nil {
		fallback := []string{}
		if def != "" {
			fallback = append(fallback, def)
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": fallback, "default": def, "labels": map[string]string{}, "error": err.Error()})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": orEmpty(ai.ModelIDs(models)), "default": def, "labels": ai.ModelLabels(models)})
	return nil
}

// prompt is one prompt template of GET /api/ai/prompts.
type prompt struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Path     string `json:"path"`
}

// GET /api/ai/prompts: the notes under copilot/copilot-custom-prompts.
func (s *Server) prompts(w http.ResponseWriter, _ *http.Request, st *state) error {
	found := []prompt{}
	for _, rel := range st.vault.Files(true) {
		if !strings.HasPrefix(rel, promptsDir+"/") {
			continue
		}
		text, err := st.vault.ReadText(rel)
		if err != nil {
			s.log.Warn("cannot read prompt", "path", rel, "err", err)
			continue
		}
		_, body := knowledge.SplitFrontmatter(text)
		found = append(found, prompt{Name: vault.Stem(rel), Template: pystr.Strip(body), Path: rel})
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": found})
	return nil
}

// eventStream writes server-sent events, flushing after each one so the
// client sees tokens as they arrive (also through MyGo's scheme handler).
type eventStream struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	broken bool // a write failed: the client went away
}

// startEvents sends the SSE response headers.
func startEvents(w http.ResponseWriter) *eventStream {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // reverse proxies: do not buffer
	w.WriteHeader(http.StatusOK)
	es := &eventStream{w: w, rc: http.NewResponseController(w)}
	es.flush()
	return es
}

// send writes one "data: <json>\n\n" event.
func (es *eventStream) send(v any) error {
	if es.broken {
		return errStreamClosed
	}
	data, err := ai.SSEData(v)
	if err != nil {
		return err
	}
	if _, err := es.w.Write(data); err != nil {
		es.broken = true
		return err
	}
	return es.flush()
}

func (es *eventStream) flush() error {
	if err := es.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		es.broken = true
		return err
	}
	return nil
}

var errStreamClosed = errors.New("event stream closed")

// POST /api/ai/chat {model, messages, scope="note", path?, selection?}:
// streams {"delta"} events, then {"done": true} or {"error"}.
func (s *Server) chat(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Model     string        `json:"model"`
		Messages  *[]ai.Message `json:"messages"`
		Scope     *string       `json:"scope"`
		Path      *string       `json:"path"`
		Selection *string       `json:"selection"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Messages == nil {
		return required("messages")
	}
	scope := ai.ScopeNote
	if body.Scope != nil {
		scope = *body.Scope
	}
	messages, err := ai.BuildChatMessages(st.vault, st.settings.AI.VaultCharBudget, *body.Messages, scope, deref(body.Path), deref(body.Selection))
	if err != nil {
		return err // before any event, as in Python (e.g. 404 for a missing note)
	}
	ctx := r.Context()
	events := startEvents(w)
	err = st.ai.Stream(ctx, body.Model, messages, func(delta string) error {
		return events.send(map[string]string{"delta": delta})
	})
	switch {
	case err == nil:
		_ = events.send(map[string]bool{"done": true})
	case ctx.Err() == nil && !events.broken:
		s.log.Warn("chat failed", "err", err)
		_ = events.send(map[string]string{"error": err.Error()})
	}
	return nil
}

// POST /api/ai/agent {model, task, chats=[], path?}: plans and drafts a
// changeset, streaming agent events (status, plan, proposal…, done | error).
func (s *Server) agent(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Model string   `json:"model"`
		Task  *string  `json:"task"`
		Chats []string `json:"chats"`
		Path  *string  `json:"path"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Task == nil {
		return required("task")
	}
	if err := maxChars("task", *body.Task, maxTaskChars); err != nil {
		return err
	}
	chatPaths := make([]string, 0, len(body.Chats))
	for _, p := range body.Chats {
		clean, err := st.vault.Clean(p)
		if err != nil {
			return err
		}
		chatPaths = append(chatPaths, clean)
	}
	ctx := r.Context()
	events := startEvents(w)
	req := ai.AgentRequest{Model: body.Model, Task: *body.Task, Chats: chatPaths, Path: deref(body.Path)}
	err := ai.RunAgent(ctx, st.ai, st.vault, st.settings.AI.VaultCharBudget, req, func(e ai.Event) error {
		return events.send(e)
	})
	if err != nil && ctx.Err() == nil && !events.broken {
		s.log.Warn("agent failed", "err", err)
		_ = events.send(ai.ErrorEvent(err))
	}
	return nil
}

// POST /api/ai/apply {summary, operations, chats=[]}: applies a reviewed
// changeset, marks the distilled chats and commits everything at once.
func (s *Server) apply(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Summary    string         `json:"summary"`
		Operations *[]ai.Proposal `json:"operations"`
		Chats      []string       `json:"chats"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	if body.Operations == nil {
		return required("operations")
	}
	message := ai.CommitMessage(body.Summary)
	st.flush()
	touched, err := ai.ApplyChangeset(st.vault, *body.Operations)
	if err != nil {
		st.record(touched, message, true) // keep what was written before the failure
		return err
	}
	written := ai.WrittenPaths(*body.Operations)
	for _, rel := range body.Chats {
		clean, err := st.vault.Clean(rel)
		if err != nil {
			continue
		}
		if path, err := chats.SetStatus(st.vault, clean, chats.StatusDistilled, written); err == nil {
			touched = append(touched, path)
		}
	}
	st.record(touched, message, true)
	writeJSON(w, http.StatusOK, map[string]any{"touched": orEmpty(touched)})
	return nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
