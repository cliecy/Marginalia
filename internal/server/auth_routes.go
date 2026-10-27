package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"marginalia/internal/config"
	"marginalia/internal/magpie"
	"marginalia/internal/mcp"
)

// POST /api/auth/login {password}: password login (rate limited per client
// address). Through AppHandler it just reports the user: the app needs no
// login.
//
// Unlike the Python backend, the rate limit is checked before the body is
// read, and the body may be at most maxLoginBody bytes: the route is public,
// so a limited client must not be able to make the server buffer anything.
func (s *Server) login(w http.ResponseWriter, r *http.Request, st *state) error {
	app := authOf(r).kind == authApp
	key, now := clientIP(r, trustsProxy(r, st)), s.now()
	if !app && !s.limiter.allow(key, now) {
		return errorf(http.StatusTooManyRequests, "尝试次数过多，请 5 分钟后再试")
	}
	var body struct {
		Password *string `json:"password"`
	}
	if err := decodeSmallJSON(w, r, &body, maxLoginBody); err != nil {
		return err
	}
	if body.Password == nil {
		return required("password")
	}
	if err := maxChars("password", *body.Password, 512); err != nil {
		return err
	}
	user := st.settings.LoginUser
	if app {
		writeJSON(w, http.StatusOK, map[string]string{"user": user})
		return nil
	}
	if !s.core.checkPassword(*body.Password) {
		s.limiter.failure(key, now)
		return errorf(http.StatusUnauthorized, "密码错误")
	}
	s.setSession(w, st, user)
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
	return nil
}

// POST /api/auth/logout
func (s *Server) logout(w http.ResponseWriter, r *http.Request, st *state) error {
	if authOf(r).kind != authApp {
		clearSession(w, st)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// meVault describes the open vault in GET /api/auth/me.
type meVault struct {
	Configured bool   `json:"configured"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"` // app and session only
}

// meFeatures tells the frontend which Go-only APIs exist (the Python
// backend sends none, and the frontend hides those UIs).
type meFeatures struct {
	Settings     bool `json:"settings"`
	Integrations bool `json:"integrations"`
	OpenAPI      bool `json:"openapi"`
}

// meResponse is GET /api/auth/me: Python's fields first, then the
// desktop extensions.
type meResponse struct {
	User          string     `json:"user"`
	AIConfigured  bool       `json:"ai_configured"`
	DefaultModel  string     `json:"default_model"`
	Git           bool       `json:"git"`
	AssetsDir     string     `json:"assets_dir"`
	Mode          string     `json:"mode"`
	Version       string     `json:"version"`
	Desktop       bool       `json:"desktop"`
	Auth          string     `json:"auth"`
	Vault         meVault    `json:"vault"`
	AIBaseURL     string     `json:"ai_base_url"`
	AIProvider    string     `json:"ai_provider"`
	Features      meFeatures `json:"features"`
	EnvOverridden []string   `json:"env_overrides"`
}

// GET /api/auth/me: who is signed in and what this installation offers.
func (s *Server) me(w http.ResponseWriter, r *http.Request, st *state) error {
	info := authOf(r)
	resp := meResponse{
		User:          info.user,
		AIConfigured:  st.settings.AIConfigured(),
		DefaultModel:  st.settings.AI.DefaultModel,
		Git:           st.git != nil && st.git.Enabled(),
		AssetsDir:     st.settings.AssetsDir,
		Mode:          modeName(s.core.mode),
		Version:       s.core.version,
		Desktop:       info.kind == authApp,
		Auth:          info.kind.String(),
		AIBaseURL:     st.settings.AI.BaseURL,
		AIProvider:    providerOf(st.settings.AI.BaseURL),
		Features:      meFeatures{Settings: true, Integrations: true, OpenAPI: true},
		EnvOverridden: orEmpty(st.settings.EnvOverrides()),
	}
	if resp.User == "" {
		resp.User = st.settings.LoginUser
	}
	if st.vault != nil {
		resp.Vault = meVault{Configured: true, Name: filepath.Base(st.vault.Root())}
		if info.kind == authApp || info.kind == authSession {
			resp.Vault.Path = st.vault.Root()
		}
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func modeName(m config.Mode) string {
	if m == config.ModeServer {
		return "server"
	}
	return "desktop"
}

// providerOf is "magpie" for Magpie's gateway, else "custom".
func providerOf(baseURL string) string {
	if magpie.IsGatewayURL(baseURL) {
		return "magpie"
	}
	return "custom"
}

// POST /api/client-error: frontend crash reports, logged.
func (s *Server) clientError(w http.ResponseWriter, r *http.Request, _ *state) error {
	var body struct {
		Message   string `json:"message"`
		Stack     string `json:"stack"`
		URL       string `json:"url"`
		UserAgent string `json:"userAgent"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return err
	}
	for _, f := range []struct {
		name, value string
		limit       int
	}{{"message", body.Message, 4000}, {"stack", body.Stack, 12000}, {"url", body.URL, 2000}, {"userAgent", body.UserAgent, 1000}} {
		if err := maxChars(f.name, f.value, f.limit); err != nil {
			return err
		}
	}
	s.log.Error("browser error", "message", body.Message, "url", body.URL, "ua", body.UserAgent, "stack", body.Stack)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// GET /api/mcp-info: the API token, for the API page.
func (s *Server) mcpInfo(w http.ResponseWriter, _ *http.Request, _ *state) error {
	writeJSON(w, http.StatusOK, map[string]string{"token": s.core.Token()})
	return nil
}

// mcpHandler serves /mcp (MCP Streamable HTTP, JSON responses). Only POST
// is supported (GET and DELETE answer 405 "Allow: POST" without
// authentication, like Python). NetHandler requests need the API token as a
// Bearer header or ?token=; AppHandler requests are trusted.
func (s *Server) mcpHandler() http.Handler {
	inner := mcp.HTTPHandler(mcp.HandlerFunc(func(message json.RawMessage) ([]byte, bool) {
		return s.core.MCP().Handle(message)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if kind := authOf(r).kind; kind != authApp && kind != authToken {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing bearer token"})
			return
		}
		inner.ServeHTTP(w, r)
	})
}
