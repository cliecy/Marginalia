package server

import (
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"marginalia/internal/config"
)

// ------------------------------------------------------------- settings

// settingsView is GET /api/app/settings.
type settingsView struct {
	NotesDir     string                    `json:"notes_dir"`
	DataDir      string                    `json:"data_dir"`
	AI           aiSettingsView            `json:"ai"`
	API          apiSettingsView           `json:"api"`
	Git          config.GitSettings        `json:"git"`
	Appearance   config.AppearanceSettings `json:"appearance"`
	EnvOverrides []string                  `json:"env_overrides"` // NOTES_* variables that win over these values
}

type aiSettingsView struct {
	BaseURL      string `json:"base_url"`
	HasKey       bool   `json:"has_key"`
	DefaultModel string `json:"default_model"`
	Provider     string `json:"provider"` // "magpie" | "custom"
}

type apiSettingsView struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	CORSOrigins []string `json:"cors_origins"`
	Running     bool     `json:"running"`
	URL         string   `json:"url"`
	Error       string   `json:"error,omitempty"`
}

// settingsResponse renders the current settings with the listener state.
func (s *Server) settingsResponse() settingsView {
	set := s.core.Settings()
	status := s.APIStatus()
	return settingsView{
		NotesDir: set.NotesDir,
		DataDir:  set.DataDir,
		AI: aiSettingsView{
			BaseURL:      set.AI.BaseURL,
			HasKey:       set.AIAPIKey() != "",
			DefaultModel: set.AI.DefaultModel,
			Provider:     providerOf(set.AI.BaseURL),
		},
		API: apiSettingsView{
			Enabled:     set.API.Enabled,
			Host:        set.API.Host,
			Port:        set.API.Port,
			CORSOrigins: orEmpty(set.API.CORSOrigins),
			Running:     status.Running,
			URL:         status.URL,
			Error:       status.Error,
		},
		Git:          set.Git,
		Appearance:   set.Appearance,
		EnvOverrides: orEmpty(set.EnvOverrides()),
	}
}

// GET /api/app/settings
func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request, _ *state) error {
	writeJSON(w, http.StatusOK, s.settingsResponse())
	return nil
}

// settingsPatch is the body of PUT /api/app/settings: the GET shape, every
// field optional, plus the write-only ai.api_key. Read-only fields
// (data_dir, has_key, provider, running, url…) are ignored.
type settingsPatch struct {
	NotesDir *string `json:"notes_dir"`
	AI       *struct {
		BaseURL      *string `json:"base_url"`
		APIKey       *string `json:"api_key"`
		DefaultModel *string `json:"default_model"`
	} `json:"ai"`
	API *struct {
		Enabled     *bool     `json:"enabled"`
		Host        *string   `json:"host"`
		Port        *int      `json:"port"`
		CORSOrigins *[]string `json:"cors_origins"`
	} `json:"api"`
	Git *struct {
		Autocommit         *bool    `json:"autocommit"`
		CommitDelaySeconds *float64 `json:"commit_delay_seconds"`
		AutosyncMinutes    *float64 `json:"autosync_minutes"`
		AuthorName         *string  `json:"author_name"`
		AuthorEmail        *string  `json:"author_email"`
	} `json:"git"`
	Appearance *struct {
		Theme *string `json:"theme"`
	} `json:"appearance"`
}

// apply validates the patch and writes it into s. The AI key is returned
// separately (it lives in its own file).
func (p *settingsPatch) apply(s *config.Settings) (key *string, err error) {
	bad := func(format string, args ...any) error { return errorf(http.StatusBadRequest, format, args...) }
	if p.NotesDir != nil {
		dir, err := config.CheckNotesDir(*p.NotesDir)
		if err != nil {
			return nil, bad("%s", err.Error())
		}
		s.NotesDir = dir
	}
	if a := p.AI; a != nil {
		if a.BaseURL != nil {
			base := strings.TrimRight(strings.TrimSpace(*a.BaseURL), "/")
			if base != "" {
				if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return nil, bad("AI 服务地址需要以 http:// 或 https:// 开头")
				}
			}
			s.AI.BaseURL = base
		}
		if a.DefaultModel != nil {
			s.AI.DefaultModel = strings.TrimSpace(*a.DefaultModel)
		}
		key = a.APIKey
	}
	if a := p.API; a != nil {
		if a.Enabled != nil {
			s.API.Enabled = *a.Enabled
		}
		if a.Host != nil {
			host := strings.Trim(strings.TrimSpace(*a.Host), "[]")
			if host != "localhost" && net.ParseIP(host) == nil {
				return nil, bad("监听地址需要是 IP 地址（例如 127.0.0.1）或 localhost")
			}
			s.API.Host = host
		}
		if a.Port != nil {
			if *a.Port < 1 || *a.Port > 65535 {
				return nil, bad("端口需要在 1 到 65535 之间")
			}
			s.API.Port = *a.Port
		}
		if a.CORSOrigins != nil {
			origins := []string{}
			for _, o := range *a.CORSOrigins {
				o = strings.TrimRight(strings.TrimSpace(o), "/")
				if o == "" {
					continue
				}
				if o != "*" && originOf(o) != strings.ToLower(o) {
					return nil, bad("CORS 来源格式不对：%s（应为 https://example.com 这样的来源，或 *）", o)
				}
				if !slices.Contains(origins, o) {
					origins = append(origins, o)
				}
			}
			s.API.CORSOrigins = origins
		}
	}
	if g := p.Git; g != nil {
		if g.Autocommit != nil {
			s.Git.Autocommit = *g.Autocommit
		}
		if g.CommitDelaySeconds != nil {
			if *g.CommitDelaySeconds < 0 {
				return nil, bad("提交延迟不能是负数")
			}
			s.Git.CommitDelaySeconds = *g.CommitDelaySeconds
		}
		if g.AutosyncMinutes != nil {
			if *g.AutosyncMinutes < 0 {
				return nil, bad("自动同步间隔不能是负数")
			}
			s.Git.AutosyncMinutes = *g.AutosyncMinutes
		}
		if g.AuthorName != nil {
			if strings.TrimSpace(*g.AuthorName) == "" {
				return nil, bad("提交作者不能为空")
			}
			s.Git.AuthorName = strings.TrimSpace(*g.AuthorName)
		}
		if g.AuthorEmail != nil {
			if strings.TrimSpace(*g.AuthorEmail) == "" {
				return nil, bad("提交邮箱不能为空")
			}
			s.Git.AuthorEmail = strings.TrimSpace(*g.AuthorEmail)
		}
	}
	if a := p.Appearance; a != nil && a.Theme != nil {
		switch *a.Theme {
		case config.ThemeSystem, config.ThemeLight, config.ThemeDark:
			s.Appearance.Theme = *a.Theme
		default:
			return nil, bad("外观只能是 system、light 或 dark")
		}
	}
	return key, nil
}

// PUT /api/app/settings: partial update. Saves settings.json, reloads the
// core when the vault, AI or git settings change and restarts the API
// listener when its address or switch changes. Returns the new settings
// (a listener that could not start is reported in api.error).
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, _ *state) error {
	var patch settingsPatch
	if err := decodeJSON(w, r, &patch); err != nil {
		return err
	}
	restart := false
	if _, err := s.core.Update(func(next *config.Settings) error {
		before := next.API
		key, err := patch.apply(next)
		if err != nil {
			return err
		}
		if key != nil {
			if err := next.SetAIAPIKey(strings.TrimSpace(*key)); err != nil {
				return err
			}
		}
		restart = listenerChanged(before, next.API)
		return nil
	}); err != nil {
		return err
	}
	if restart {
		_ = s.RestartAPI() // a failure is reported in api.error
	}
	writeJSON(w, http.StatusOK, s.settingsResponse())
	return nil
}

// listenerChanged reports whether the listener must be restarted (CORS
// origins apply per request and need no restart).
func listenerChanged(a, b config.APISettings) bool {
	return a.Enabled != b.Enabled || a.Host != b.Host || a.Port != b.Port
}
