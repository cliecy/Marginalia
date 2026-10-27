package server

import (
	"bytes"
	"net/http"

	"marginalia/internal/gitrepo"
)

// GET /api/git/status: the repository status plus the deploy key's public
// half (when the server uses one) and the autosync interval.
func (s *Server) gitStatus(w http.ResponseWriter, _ *http.Request, st *state) error {
	status, err := st.git.Status()
	if err != nil {
		return err
	}
	data, err := marshalJSON(status)
	if err != nil {
		return err
	}
	publicKey := ""
	if s.core.deployKey != "" {
		publicKey = gitrepo.PublicKey(s.core.deployKey)
	}
	extra, err := marshalJSON(struct {
		PublicKey       string  `json:"public_key"`
		AutosyncMinutes float64 `json:"autosync_minutes"`
	}{publicKey, st.settings.Git.AutosyncMinutes})
	if err != nil {
		return err
	}
	// {**git.status(), "public_key": …, "autosync_minutes": …} keeping the
	// status' key order.
	merged := extra
	if !bytes.Equal(data, []byte("{}")) {
		merged = append(append(bytes.TrimSuffix(data, []byte("}")), ','), extra[1:]...)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(merged)
	return nil
}

// POST /api/git/sync: fetch, rebase and push; every failure is a 409 with
// git's (or the conflict) message.
func (s *Server) gitSync(w http.ResponseWriter, _ *http.Request, st *state) error {
	result, err := st.git.Sync()
	if err != nil {
		return errorf(http.StatusConflict, "%s", err.Error())
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

// GET /api/history?path=
func (s *Server) history(w http.ResponseWriter, r *http.Request, st *state) error {
	path, err := queryParam(r, "path")
	if err != nil {
		return err
	}
	clean, err := st.vault.Clean(path)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": orEmpty(st.git.History(clean, gitrepo.DefaultHistoryLimit))})
	return nil
}

// GET /api/history/version?path=&rev=: a note's content at a revision
// (404 for unknown revisions).
func (s *Server) historyVersion(w http.ResponseWriter, r *http.Request, st *state) error {
	path, err := queryParam(r, "path")
	if err != nil {
		return err
	}
	rev, err := queryParam(r, "rev")
	if err != nil {
		return err
	}
	clean, err := st.vault.Clean(path)
	if err != nil {
		return err
	}
	content, err := st.git.Show(rev, clean)
	if err != nil {
		return errorf(http.StatusNotFound, "%s", err.Error())
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
	return nil
}
