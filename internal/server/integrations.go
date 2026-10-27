package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"marginalia/internal/ai"
	"marginalia/internal/config"
	"marginalia/internal/magpie"
)

// MCPName is the name Marginalia's MCP server gets in Magpie's library.
const MCPName = "marginalia"

const (
	// registrationTTL is how long the answer of `magpie library` is reused
	// by GET /api/app/integrations (the page polls it every minute).
	registrationTTL = 30 * time.Second
	// registrationWait bounds how long GET /api/app/integrations waits for
	// a `magpie library` refresh before answering with the previous value;
	// the refresh still completes in the background.
	registrationWait = 600 * time.Millisecond
	// magpieModelsTimeout bounds the model lookup of POST /api/app/magpie/use.
	magpieModelsTimeout = 5 * time.Second
)

// ------------------------------------------------------------ integrations

// integrationsView is GET /api/app/integrations.
type integrationsView struct {
	API    apiIntegration    `json:"api"`
	Magpie magpieIntegration `json:"magpie"`
}

type apiIntegration struct {
	Enabled    bool   `json:"enabled"`
	Running    bool   `json:"running"`
	URL        string `json:"url"`
	MCPURL     string `json:"mcp_url"`
	OpenAPIURL string `json:"openapi_url"`
	Token      string `json:"token,omitempty"` // app and session only
	Command    string `json:"command"`         // the stdio MCP server is `<command> mcp`
	Error      string `json:"error,omitempty"`
}

type magpieIntegration struct {
	Gateway       magpie.Gateway `json:"gateway"`
	CLI           string         `json:"cli"`            // "" when Magpie is not installed
	MCPRegistered *bool          `json:"mcp_registered"` // null when unknown
	InstallURL    string         `json:"install_url"`
}

// GET /api/app/integrations: the API listener and Magpie (gateway, CLI,
// library registration). It answers within about DetectTimeout: the gateway
// probe is bounded and slow `magpie library` reads are cached.
func (s *Server) integrations(w http.ResponseWriter, r *http.Request, _ *state) error {
	status := s.APIStatus()
	view := integrationsView{
		API: apiIntegration{
			Enabled:    status.Enabled,
			Running:    status.Running,
			URL:        status.URL,
			MCPURL:     status.URL + "/mcp",
			OpenAPIURL: status.URL + "/api/openapi.json",
			Command:    s.executablePath(),
			Error:      status.Error,
		},
		Magpie: magpieIntegration{InstallURL: magpie.InstallURL},
	}
	if kind := authOf(r).kind; kind == authApp || kind == authSession {
		view.API.Token = s.core.Token()
	}
	var wg sync.WaitGroup
	wg.Go(func() { view.Magpie.Gateway = s.magpie.Gateway(r.Context()) })
	wg.Go(func() {
		if view.Magpie.CLI = s.magpie.CLI(); view.Magpie.CLI != "" {
			view.Magpie.MCPRegistered = s.registration.get(s)
		}
	})
	wg.Wait()
	writeJSON(w, http.StatusOK, view)
	return nil
}

// registrationCache remembers whether Marginalia is in Magpie's library.
// `magpie library` may be slow, so it is refreshed in the background and a
// caller waits for a refresh at most registrationWait, getting the previous
// answer otherwise.
type registrationCache struct {
	mu      sync.Mutex
	at      time.Time     // when value was learnt; zero = never
	value   *bool         // nil = unknown
	running chan struct{} // closed when the running refresh ends; nil = idle
	gen     int           // bumped by set: older refreshes are discarded
}

// get returns the registration state, refreshing it when stale.
func (c *registrationCache) get(s *Server) *bool {
	c.mu.Lock()
	if !c.at.IsZero() && s.now().Sub(c.at) < registrationTTL {
		defer c.mu.Unlock()
		return c.value
	}
	done := c.running
	if done == nil {
		done = make(chan struct{})
		c.running = done
		gen := c.gen
		go func() {
			defer close(done)
			value := s.magpie.MCPRegistered(context.Background(), MCPName)
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.gen == gen {
				c.value, c.at = value, s.now()
			}
			if c.running == done {
				c.running = nil
			}
		}()
	}
	stale := c.value
	c.mu.Unlock()

	timer := time.NewTimer(registrationWait)
	defer timer.Stop()
	select {
	case <-done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.value
	case <-timer.C:
		return stale
	}
}

// set records a state learnt from a register/unregister command (nil =
// unknown, refreshed by the next get).
func (c *registrationCache) set(value *bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.running = nil
	c.value = value
	c.at = time.Time{}
	if value != nil {
		c.at = now
	}
}

// POST /api/app/token/rotate: a new API token; clients using the old one
// are locked out at once.
func (s *Server) rotateToken(w http.ResponseWriter, _ *http.Request, _ *state) error {
	token, err := s.core.RotateToken()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
	return nil
}

// ------------------------------------------------------------------ magpie

// POST /api/app/magpie/use {model?}: use Magpie's gateway as the AI
// provider (base URL, key "magpie") with model, else Magpie's default
// routing group when offered, else the first model it offers. Answers the
// new settings.
func (s *Server) magpieUse(w http.ResponseWriter, r *http.Request, st *state) error {
	var body struct {
		Model string `json:"model"`
	}
	if err := decodeOptionalJSON(w, r, &body); err != nil {
		return err
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = s.magpieDefaultModel(r.Context(), st)
	}
	if _, err := s.core.Update(func(next *config.Settings) error {
		next.AI.BaseURL = magpie.BaseURL
		next.AI.DefaultModel = model
		return next.SetAIAPIKey(magpie.APIKey)
	}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.settingsResponse())
	return nil
}

// magpieDefaultModel picks the model when switching to Magpie: its default
// routing group when offered, else its first model; when the gateway cannot
// be asked, the current model if it already is a Magpie one, else
// magpie.DefaultModel.
func (s *Server) magpieDefaultModel(ctx context.Context, st *state) string {
	ctx, cancel := context.WithTimeout(ctx, magpieModelsTimeout)
	defer cancel()
	client := ai.NewClient(ai.Options{BaseURL: s.magpie.OpenAIBaseURL(), APIKey: func() string { return magpie.APIKey }})
	if models, err := client.Models(ctx); err == nil && len(models) > 0 {
		ids := ai.ModelIDs(models)
		if slices.Contains(ids, magpie.DefaultModel) {
			return magpie.DefaultModel
		}
		return ids[0]
	}
	if providerOf(st.settings.AI.BaseURL) == "magpie" && st.settings.AI.DefaultModel != "" {
		return st.settings.AI.DefaultModel
	}
	return magpie.DefaultModel
}

// POST /api/app/magpie/register-mcp {transport: "stdio"|"http"}: adds
// Marginalia to Magpie's library, which configures it in every agent.
// stdio (the default) runs `<this executable> mcp`, which works whether or
// not the app runs; http points at the loopback API with the token in the
// URL. Answers {ok, output, command}.
func (s *Server) magpieRegister(w http.ResponseWriter, r *http.Request, _ *state) error {
	var body struct {
		Transport string `json:"transport"`
	}
	if err := decodeOptionalJSON(w, r, &body); err != nil {
		return err
	}
	var target []string
	switch strings.ToLower(strings.TrimSpace(body.Transport)) {
	case "", "stdio":
		target = []string{s.executablePath(), "mcp"}
	case "http":
		status := s.APIStatus()
		if !status.Enabled {
			return &apiError{Status: http.StatusConflict, Code: "api_disabled",
				Detail: "本地 API 没有启用：请先在设置里打开它，或改用 stdio 方式"}
		}
		target = []string{status.URL + "/mcp?token=" + url.QueryEscape(s.core.Token())}
	default:
		return errorf(http.StatusUnprocessableEntity, "transport 只能是 stdio 或 http")
	}
	return s.runMagpie(w, true, func() (magpie.Result, error) {
		return s.magpie.RegisterMCP(r.Context(), MCPName, target...)
	})
}

// POST /api/app/magpie/unregister-mcp: removes Marginalia from Magpie's
// library. Answers {ok, output, command}.
func (s *Server) magpieUnregister(w http.ResponseWriter, r *http.Request, _ *state) error {
	if err := decodeOptionalJSON(w, r, &struct{}{}); err != nil {
		return err
	}
	return s.runMagpie(w, false, func() (magpie.Result, error) {
		return s.magpie.UnregisterMCP(r.Context(), MCPName)
	})
}

// runMagpie runs a register (add) or unregister CLI command, records the
// resulting library state and answers {ok, output, command}. A missing CLI
// is a 409 with code "magpie_cli_missing".
func (s *Server) runMagpie(w http.ResponseWriter, add bool, run func() (magpie.Result, error)) error {
	res, err := run()
	if errors.Is(err, magpie.ErrNoCLI) {
		s.registration.set(nil, s.now())
		return &apiError{Status: http.StatusConflict, Code: "magpie_cli_missing",
			Detail: "没有找到 magpie 命令行工具：请先安装 Magpie（" + magpie.InstallURL + "）",
			Extra:  map[string]any{"command": res.Command, "output": ""}}
	}
	if err != nil {
		return err
	}
	if res.OK {
		s.registration.set(&add, s.now())
	} else {
		s.registration.set(nil, s.now())
		token := s.core.Token()
		s.log.Warn("magpie command failed", "command", redact(res.Command, token), "output", redact(res.Output, token))
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// redact hides secret in text (for logs).
func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}
