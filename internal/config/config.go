// Package config loads and saves Marginalia's settings and secrets.
//
// Settings come from three layers, later ones winning:
//
//  1. built-in defaults for the run mode (ModeDesktop or ModeServer; the
//     the server defaults keep data in /data and notes in /notes),
//  2. DataDir/settings.json, which the desktop app edits,
//  3. NOTES_* environment variables (the app's documented configuration).
//
// Secrets live next to settings.json in DataDir as 0600 files with the same
// password, session_secret, mcp_token, ai_api_key, ssh/id_ed25519.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Mode selects the built-in defaults.
type Mode int

const (
	// ModeDesktop is the MyGo desktop app (and the `mcp` stdio subcommand):
	// data in DefaultDataDir(), no vault and no AI provider until the user
	// picks them.
	ModeDesktop Mode = iota
	// ModeServer is `marginalia serve`: data in /data, notes in /notes, the
	// embedded web UI and no AI provider until NOTES_AI_* configures one.
	ModeServer
)

// SettingsFileName is the name of the settings file inside DataDir.
const SettingsFileName = "settings.json"

// Themes accepted by Appearance.Theme.
const (
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
)

// Settings is the complete runtime configuration. The JSON form (snake_case)
// is the layout of settings.json; DataDir is never stored in the file.
type Settings struct {
	// NotesDir is the absolute path of the vault; "" means no vault has been
	// chosen yet (desktop first run).
	NotesDir string `json:"notes_dir"`
	// DataDir holds settings.json and the secret files.
	DataDir string `json:"-"`
	// StaticDir overrides the embedded web UI with files on disk ("" = embedded).
	StaticDir string `json:"static_dir"`
	// LoginUser is the user name reported after password login.
	LoginUser string `json:"login_user"`
	// AllowedOrigins are extra origins allowed to make cookie-authenticated
	// unsafe requests (the Python "guard" middleware).
	AllowedOrigins []string `json:"allowed_origins"`
	// CookieSecure marks the session cookie Secure.
	CookieSecure bool `json:"cookie_secure"`
	// SessionHours is the session cookie lifetime.
	SessionHours int `json:"session_hours"`
	// AssetsDir is the vault folder uploads go to by default.
	AssetsDir string `json:"assets_dir"`
	// MaxUploadBytes limits a single upload.
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	// ForwardedAllowIPs lists the reverse proxies whose X-Forwarded-For and
	// X-Forwarded-Proto headers the TCP API believes (login rate limiting,
	// the origin check): comma-separated IP addresses or CIDR ranges, "*" for
	// any peer, "" for none, like uvicorn's --forwarded-allow-ips. Defaults:
	// the loopback addresses in server mode, none in the desktop app.
	ForwardedAllowIPs string `json:"forwarded_allow_ips"`

	AI         AISettings         `json:"ai"`
	Git        GitSettings        `json:"git"`
	API        APISettings        `json:"api"`
	Appearance AppearanceSettings `json:"appearance"`

	envOverrides []string
}

// AISettings configures the OpenAI-compatible chat endpoint.
type AISettings struct {
	// BaseURL is the API root without a trailing slash, e.g.
	// "http://127.0.0.1:3425/v1"; "" disables AI.
	BaseURL string `json:"base_url"`
	// DefaultModel is used when a request names no model.
	DefaultModel string `json:"default_model"`
	// VaultCharBudget caps how much vault text goes into one prompt.
	VaultCharBudget int `json:"vault_char_budget"`
	// APIKeyFile overrides the key file location (default DataDir/ai_api_key).
	APIKeyFile string `json:"api_key_file"`
}

// GitSettings configures automatic commits and sync of the vault.
type GitSettings struct {
	Autocommit         bool    `json:"autocommit"`
	CommitDelaySeconds float64 `json:"commit_delay_seconds"`
	AutosyncMinutes    float64 `json:"autosync_minutes"` // 0 disables periodic sync
	AuthorName         string  `json:"author_name"`
	AuthorEmail        string  `json:"author_email"`
}

// APISettings configures the loopback TCP listener (REST + MCP) of the
// desktop app.
type APISettings struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	CORSOrigins []string `json:"cors_origins"`
}

// AppearanceSettings holds UI preferences.
type AppearanceSettings struct {
	Theme string `json:"theme"` // ThemeSystem, ThemeLight or ThemeDark
}

// Defaults returns the built-in settings for mode.
func Defaults(mode Mode) Settings {
	s := Settings{
		LoginUser:      "cliecy",
		AllowedOrigins: []string{},
		SessionHours:   720,
		AssetsDir:      "_assets",
		MaxUploadBytes: 50 * 1024 * 1024,
		AI:             AISettings{VaultCharBudget: 200000},
		Git: GitSettings{
			Autocommit:         true,
			CommitDelaySeconds: 60,
			AuthorName:         "Marginalia Web",
			AuthorEmail:        "marginalia@localhost",
		},
		API:        APISettings{Enabled: true, Host: "127.0.0.1", Port: 8190, CORSOrigins: []string{}},
		Appearance: AppearanceSettings{Theme: ThemeSystem},
	}
	switch mode {
	case ModeServer:
		s.ForwardedAllowIPs = "127.0.0.1,::1"
		s.DataDir = "/data"
		s.NotesDir = "/notes"
	default:
		s.DataDir = DefaultDataDir()
	}
	return s
}

// DefaultDataDir is the per-user data directory of the desktop app:
// os.UserConfigDir()/Marginalia (on macOS
// ~/Library/Application Support/Marginalia). The NOTES_DATA_DIR
// environment variable is applied by Load, not here.
func DefaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		current := filepath.Join(dir, "Marginalia")
		migrateDataDir(filepath.Join(dir, "MarkdownNote"), current)
		return current
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".marginalia")
	}
	return filepath.Join(os.TempDir(), "Marginalia")
}

// migrateDataDir moves the data directory of the app's former name
// (MarkdownNote) to its current one, once: only when the old one exists and
// the new one does not, so settings, secrets and the deploy key carry over.
// A failed move leaves both untouched; the app then starts fresh.
func migrateDataDir(old, current string) {
	if _, err := os.Stat(current); !errors.Is(err, fs.ErrNotExist) {
		return
	}
	if info, err := os.Stat(old); err != nil || !info.IsDir() {
		return
	}
	_ = os.Rename(old, current)
}

// Load builds the settings for mode from defaults, settings.json and the
// process environment. The data directory itself is NOTES_DATA_DIR when set.
// A missing settings.json is not an error; a malformed one or an unparsable
// numeric environment variable is.
func Load(mode Mode) (Settings, error) {
	return LoadEnv(mode, os.LookupEnv)
}

// LoadEnv is Load with an explicit environment lookup (for tests and for
// callers that want to ignore the process environment).
func LoadEnv(mode Mode, lookup func(string) (string, bool)) (Settings, error) {
	s := Defaults(mode)
	if dir, ok := lookup("NOTES_DATA_DIR"); ok && strings.TrimSpace(dir) != "" {
		s.DataDir = strings.TrimSpace(dir)
	}
	if abs, err := filepath.Abs(s.DataDir); err == nil {
		s.DataDir = abs
	}
	if err := s.readFile(); err != nil {
		return s, err
	}
	if err := s.applyEnv(lookup); err != nil {
		return s, err
	}
	s.Normalize()
	return s, nil
}

// SettingsPath is DataDir/settings.json.
func (s Settings) SettingsPath() string { return filepath.Join(s.DataDir, SettingsFileName) }

// readFile overlays DataDir/settings.json onto s.
func (s *Settings) readFile() error {
	data, err := os.ReadFile(s.SettingsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config: read %s: %w", s.SettingsPath(), err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	dataDir := s.DataDir
	if err := json.Unmarshal(data, s); err != nil {
		return fmt.Errorf("config: parse %s: %w", s.SettingsPath(), err)
	}
	s.DataDir = dataDir
	return nil
}

// EnvOverrides lists the NOTES_* variables that overrode file or default
// values during Load, so a settings UI can show them as locked.
func (s Settings) EnvOverrides() []string { return slices.Clone(s.envOverrides) }

// applyEnv applies the NOTES_* variables. As in Python, a
// variable that is set but empty overrides string values with "" (except the
// directory variables, where empty means unset), booleans treat empty as
// unset and accept 1/true/yes/on, and numbers must parse.
func (s *Settings) applyEnv(lookup func(string) (string, bool)) error {
	get := func(name string) (string, bool) {
		value, ok := lookup(name)
		if ok {
			s.envOverrides = append(s.envOverrides, name)
		}
		return strings.TrimSpace(value), ok
	}
	str := func(name string, dst *string) {
		if value, ok := get(name); ok {
			*dst = value
		}
	}
	boolean := func(name string, dst *bool) {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			s.envOverrides = append(s.envOverrides, name)
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "1", "true", "yes", "on":
				*dst = true
			default:
				*dst = false
			}
		}
	}
	var errs []error
	integer := func(name string, dst *int) {
		if value, ok := get(name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				errs = append(errs, fmt.Errorf("config: %s=%q is not an integer", name, value))
				return
			}
			*dst = n
		}
	}
	float := func(name string, dst *float64) {
		if value, ok := get(name); ok {
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				errs = append(errs, fmt.Errorf("config: %s=%q is not a number", name, value))
				return
			}
			*dst = f
		}
	}

	if value, ok := lookup("NOTES_DATA_DIR"); ok && strings.TrimSpace(value) != "" {
		s.envOverrides = append(s.envOverrides, "NOTES_DATA_DIR")
	}
	if value, ok := lookup("NOTES_DIR"); ok && strings.TrimSpace(value) != "" {
		s.envOverrides = append(s.envOverrides, "NOTES_DIR")
		s.NotesDir = strings.TrimSpace(value)
	}
	if value, ok := lookup("NOTES_STATIC_DIR"); ok && strings.TrimSpace(value) != "" {
		s.envOverrides = append(s.envOverrides, "NOTES_STATIC_DIR")
		s.StaticDir = strings.TrimSpace(value)
	}
	str("NOTES_LOGIN_USER", &s.LoginUser)
	if value, ok := get("NOTES_ALLOWED_ORIGINS"); ok {
		s.AllowedOrigins = splitList(value)
	}
	boolean("NOTES_COOKIE_SECURE", &s.CookieSecure)
	integer("NOTES_SESSION_HOURS", &s.SessionHours)
	str("NOTES_AI_BASE_URL", &s.AI.BaseURL)
	if value, ok := lookup("NOTES_AI_API_KEY_FILE"); ok && strings.TrimSpace(value) != "" {
		s.envOverrides = append(s.envOverrides, "NOTES_AI_API_KEY_FILE")
		s.AI.APIKeyFile = strings.TrimSpace(value)
	}
	str("NOTES_AI_DEFAULT_MODEL", &s.AI.DefaultModel)
	integer("NOTES_AI_VAULT_CHAR_BUDGET", &s.AI.VaultCharBudget)
	boolean("NOTES_GIT_AUTOCOMMIT", &s.Git.Autocommit)
	float("NOTES_GIT_COMMIT_DELAY_SECONDS", &s.Git.CommitDelaySeconds)
	str("NOTES_GIT_AUTHOR_NAME", &s.Git.AuthorName)
	str("NOTES_GIT_AUTHOR_EMAIL", &s.Git.AuthorEmail)
	str("NOTES_ASSETS_DIR", &s.AssetsDir)
	if value, ok := get("NOTES_MAX_UPLOAD_MB"); ok {
		mb, err := strconv.Atoi(value)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: NOTES_MAX_UPLOAD_MB=%q is not an integer", value))
		} else {
			s.MaxUploadBytes = int64(mb) * 1024 * 1024
		}
	}
	float("NOTES_GIT_AUTOSYNC_MINUTES", &s.Git.AutosyncMinutes)
	str("NOTES_FORWARDED_ALLOW_IPS", &s.ForwardedAllowIPs)
	return errors.Join(errs...)
}

// splitList splits a comma-separated list, trimming items and dropping
// empty ones.
func splitList(value string) []string {
	out := []string{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Normalize canonicalises values in place: the AI base URL loses trailing
// slashes, NotesDir becomes absolute with symlinks resolved (like Python's
// Path.resolve; a leading "~/" is expanded), nil lists become empty, an
// unknown theme becomes "system", and an empty API host, an out-of-range API
// port or an empty assets folder fall back to their defaults.
func (s *Settings) Normalize() {
	s.AI.BaseURL = strings.TrimRight(strings.TrimSpace(s.AI.BaseURL), "/")
	if s.NotesDir != "" {
		s.NotesDir = resolveDir(s.NotesDir)
	}
	if s.AllowedOrigins == nil {
		s.AllowedOrigins = []string{}
	}
	if s.API.CORSOrigins == nil {
		s.API.CORSOrigins = []string{}
	}
	switch s.Appearance.Theme {
	case ThemeSystem, ThemeLight, ThemeDark:
	default:
		s.Appearance.Theme = ThemeSystem
	}
	if s.API.Host == "" {
		s.API.Host = "127.0.0.1"
	}
	if s.API.Port <= 0 || s.API.Port > 65535 {
		s.API.Port = 8190
	}
	if s.AssetsDir == "" {
		s.AssetsDir = "_assets"
	}
}

// resolveDir makes dir absolute and resolves symlinks when it exists.
func resolveDir(dir string) string {
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[2:])
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// Clone returns a deep copy of s.
func (s Settings) Clone() Settings {
	c := s
	c.AllowedOrigins = slices.Clone(s.AllowedOrigins)
	c.API.CORSOrigins = slices.Clone(s.API.CORSOrigins)
	c.envOverrides = slices.Clone(s.envOverrides)
	return c
}

// Save writes s to DataDir/settings.json atomically with mode 0600, creating
// DataDir (0700) when needed. Keys in an existing file that this version does
// not know are preserved.
func (s Settings) Save() error {
	if s.DataDir == "" {
		return errors.New("config: no data directory")
	}
	if err := os.MkdirAll(s.DataDir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	fresh, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(fresh, &merged); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if old, err := os.ReadFile(s.SettingsPath()); err == nil {
		var existing map[string]any
		if json.Unmarshal(old, &existing) == nil && existing != nil {
			merged = mergeJSON(existing, merged)
		}
	}
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return writeFileAtomic(s.SettingsPath(), append(out, '\n'), 0o600)
}

// SaveChanges writes to DataDir/settings.json only the values that differ
// between prev and s, leaving every other key of the file as it is. Use it
// for edits of loaded settings: unlike Save it never copies values that came
// from NOTES_* variables, command-line flags or built-in defaults into the
// file, where they would outlive the variable and shadow later defaults.
// Values are compared leaf by leaf in their JSON form (lists as a whole); a
// value changed to "" is written, so it clears an earlier one. When nothing
// changed the file is not touched.
func (s Settings) SaveChanges(prev Settings) error {
	if s.DataDir == "" {
		return errors.New("config: no data directory")
	}
	before, err := jsonObject(prev)
	if err != nil {
		return err
	}
	after, err := jsonObject(s)
	if err != nil {
		return err
	}
	doc := map[string]any{}
	if old, err := os.ReadFile(s.SettingsPath()); err == nil {
		var existing map[string]any
		if json.Unmarshal(old, &existing) == nil && existing != nil {
			doc = existing
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("config: read %s: %w", s.SettingsPath(), err)
	}
	if !applyChanges(doc, before, after) {
		return nil
	}
	if err := os.MkdirAll(s.DataDir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return writeFileAtomic(s.SettingsPath(), append(out, '\n'), 0o600)
}

// jsonObject returns the settings.json form of s as a generic object.
func jsonObject(s Settings) (map[string]any, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return obj, nil
}

// applyChanges sets in doc every leaf that differs between before and after
// (objects recurse; a key missing from after is deleted) and reports whether
// it changed anything.
func applyChanges(doc, before, after map[string]any) bool {
	changed := false
	keys := slices.Collect(maps.Keys(after))
	for key := range before {
		if _, ok := after[key]; !ok {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		newValue, inAfter := after[key]
		oldValue, inBefore := before[key]
		newObj, newIsObj := newValue.(map[string]any)
		oldObj, oldIsObj := oldValue.(map[string]any)
		switch {
		case !inAfter:
			delete(doc, key)
			changed = true
		case newIsObj && oldIsObj:
			sub, ok := doc[key].(map[string]any)
			if !ok {
				sub = map[string]any{}
			}
			if applyChanges(sub, oldObj, newObj) {
				doc[key] = sub
				changed = true
			}
		case !inBefore || !jsonEqual(oldValue, newValue):
			doc[key] = newValue
			changed = true
		}
	}
	return changed
}

// jsonEqual compares two decoded JSON values.
func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// mergeJSON overlays fresh onto existing recursively: objects merge key by
// key, any other value in fresh replaces the existing one.
func mergeJSON(existing, fresh map[string]any) map[string]any {
	out := maps.Clone(existing)
	for key, value := range fresh {
		if sub, ok := value.(map[string]any); ok {
			if old, ok := out[key].(map[string]any); ok {
				out[key] = mergeJSON(old, sub)
				continue
			}
		}
		out[key] = value
	}
	return out
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, fsyncs it, sets perm and renames it into place.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	name := tmp.Name()
	cleanup := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("config: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return cleanup(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("config: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// CheckNotesDir validates a vault folder chosen by the user: it must be an
// absolute path to an existing directory. It returns the cleaned path with
// symlinks resolved.
func CheckNotesDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("笔记库路径不能为空")
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("笔记库路径必须是绝对路径")
	}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("笔记库文件夹不存在")
		}
		return "", fmt.Errorf("无法访问笔记库文件夹：%w", err)
	}
	if !info.IsDir() {
		return "", errors.New("笔记库路径不是文件夹")
	}
	return resolveDir(filepath.Clean(dir)), nil
}
