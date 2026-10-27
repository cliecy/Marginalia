package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// env returns a lookup function over a fixed environment.
func env(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	server := Defaults(ModeServer)
	if server.DataDir != "/data" || server.NotesDir != "/notes" || server.StaticDir != "" ||
		server.AI.BaseURL != "" || server.AI.DefaultModel != "" {
		t.Errorf("server defaults = %+v", server)
	}
	desktop := Defaults(ModeDesktop)
	if desktop.NotesDir != "" || desktop.AI.BaseURL != "" || desktop.DataDir != DefaultDataDir() {
		t.Errorf("desktop defaults = %+v", desktop)
	}
	for _, s := range []Settings{server, desktop} {
		if s.LoginUser != "cliecy" || s.SessionHours != 720 || s.AI.VaultCharBudget != 200000 ||
			!s.Git.Autocommit || s.Git.CommitDelaySeconds != 60 || s.Git.AutosyncMinutes != 0 ||
			s.Git.AuthorName != "Marginalia Web" || s.Git.AuthorEmail != "marginalia@localhost" ||
			s.AssetsDir != "_assets" || s.MaxUploadBytes != 50<<20 || !s.API.Enabled || s.API.Host != "127.0.0.1" ||
			s.API.Port != 8190 || s.Appearance.Theme != ThemeSystem || s.CookieSecure {
			t.Errorf("common defaults = %+v", s)
		}
	}
	if !strings.HasSuffix(DefaultDataDir(), string(filepath.Separator)+"Marginalia") {
		t.Errorf("DefaultDataDir = %q", DefaultDataDir())
	}
}

func TestEnvOverridesMatchPython(t *testing.T) {
	data := t.TempDir()
	notes := t.TempDir()
	s, err := LoadEnv(ModeServer, env(map[string]string{
		"NOTES_DATA_DIR":                 data,
		"NOTES_DIR":                      notes,
		"NOTES_LOGIN_USER":               " alice ",
		"NOTES_ALLOWED_ORIGINS":          "https://a.example, ,https://b.example ",
		"NOTES_COOKIE_SECURE":            "Yes",
		"NOTES_SESSION_HOURS":            "24",
		"NOTES_AI_BASE_URL":              "http://127.0.0.1:3425/v1//",
		"NOTES_AI_API_KEY_FILE":          "/run/secrets/key",
		"NOTES_AI_DEFAULT_MODEL":         "group/default",
		"NOTES_AI_VAULT_CHAR_BUDGET":     "1000",
		"NOTES_GIT_AUTOCOMMIT":           "off",
		"NOTES_GIT_COMMIT_DELAY_SECONDS": "2.5",
		"NOTES_GIT_AUTHOR_NAME":          "Bot",
		"NOTES_GIT_AUTHOR_EMAIL":         "bot@x",
		"NOTES_ASSETS_DIR":               "files",
		"NOTES_MAX_UPLOAD_MB":            "3",
		"NOTES_GIT_AUTOSYNC_MINUTES":     "0.5",
		"NOTES_STATIC_DIR":               "/srv/static",
	}))
	if err != nil {
		t.Fatal(err)
	}
	realNotes, _ := filepath.EvalSymlinks(notes)
	checks := []struct {
		name      string
		got, want any
	}{
		{"DataDir", s.DataDir, data},
		{"NotesDir", s.NotesDir, realNotes},
		{"LoginUser", s.LoginUser, "alice"},
		{"AllowedOrigins", strings.Join(s.AllowedOrigins, "|"), "https://a.example|https://b.example"},
		{"CookieSecure", s.CookieSecure, true},
		{"SessionHours", s.SessionHours, 24},
		{"BaseURL", s.AI.BaseURL, "http://127.0.0.1:3425/v1"},
		{"APIKeyFile", s.AIAPIKeyFile(), "/run/secrets/key"},
		{"DefaultModel", s.AI.DefaultModel, "group/default"},
		{"VaultCharBudget", s.AI.VaultCharBudget, 1000},
		{"Autocommit", s.Git.Autocommit, false},
		{"CommitDelay", s.Git.CommitDelaySeconds, 2.5},
		{"AuthorName", s.Git.AuthorName, "Bot"},
		{"AuthorEmail", s.Git.AuthorEmail, "bot@x"},
		{"AssetsDir", s.AssetsDir, "files"},
		{"MaxUpload", s.MaxUploadBytes, int64(3 << 20)},
		{"Autosync", s.Git.AutosyncMinutes, 0.5},
		{"StaticDir", s.StaticDir, "/srv/static"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !slices.Contains(s.EnvOverrides(), "NOTES_AI_BASE_URL") || !slices.Contains(s.EnvOverrides(), "NOTES_DIR") {
		t.Errorf("EnvOverrides = %v", s.EnvOverrides())
	}
}

func TestEnvEdgeCases(t *testing.T) {
	data := t.TempDir()
	// Empty booleans keep the default; set-but-empty strings override (Python
	// _env semantics); empty directory variables are ignored.
	s, err := LoadEnv(ModeServer, env(map[string]string{
		"NOTES_DATA_DIR":         data,
		"NOTES_COOKIE_SECURE":    "",
		"NOTES_GIT_AUTOCOMMIT":   " ",
		"NOTES_AI_DEFAULT_MODEL": "",
		"NOTES_DIR":              "",
		"NOTES_ALLOWED_ORIGINS":  "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if s.CookieSecure || !s.Git.Autocommit || s.AI.DefaultModel != "" || s.NotesDir != "/notes" || len(s.AllowedOrigins) != 0 {
		t.Errorf("settings = %+v", s)
	}
	_, err = LoadEnv(ModeServer, env(map[string]string{"NOTES_DATA_DIR": data, "NOTES_SESSION_HOURS": "abc", "NOTES_MAX_UPLOAD_MB": "1.5"}))
	if err == nil || !strings.Contains(err.Error(), "NOTES_SESSION_HOURS") || !strings.Contains(err.Error(), "NOTES_MAX_UPLOAD_MB") {
		t.Errorf("bad numbers = %v", err)
	}
}

func TestSettingsFileRoundTrip(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	lookup := env(map[string]string{"NOTES_DATA_DIR": data})
	s, err := LoadEnv(ModeDesktop, lookup)
	if err != nil {
		t.Fatal(err)
	}
	notes := t.TempDir()
	s.NotesDir = notes
	s.AI.BaseURL = "http://127.0.0.1:3425/v1/"
	s.API.Port = 9000
	s.API.CORSOrigins = []string{"http://localhost:5173"}
	s.Appearance.Theme = ThemeDark
	s.Git.AutosyncMinutes = 15
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.SettingsPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("settings.json mode = %v, %v", info.Mode().Perm(), err)
	}
	if dirInfo, _ := os.Stat(data); dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("data dir mode = %v", dirInfo.Mode().Perm())
	}
	// Unknown keys written by a newer version survive a save.
	raw, _ := os.ReadFile(s.SettingsPath())
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["data_dir"]; ok {
		t.Error("data_dir must not be stored")
	}
	doc["future"] = "keep me"
	doc["api"].(map[string]any)["future_api"] = 1.5
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(s.SettingsPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadEnv(ModeDesktop, lookup)
	if err != nil {
		t.Fatal(err)
	}
	realNotes, _ := filepath.EvalSymlinks(notes)
	if loaded.NotesDir != realNotes || loaded.AI.BaseURL != "http://127.0.0.1:3425/v1" || loaded.API.Port != 9000 ||
		loaded.Appearance.Theme != ThemeDark || loaded.Git.AutosyncMinutes != 15 ||
		!slices.Equal(loaded.API.CORSOrigins, []string{"http://localhost:5173"}) || loaded.DataDir != data {
		t.Errorf("loaded = %+v", loaded)
	}
	loaded.Appearance.Theme = ThemeLight
	if err := loaded.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(s.SettingsPath())
	_ = json.Unmarshal(raw, &doc)
	if doc["future"] != "keep me" || doc["api"].(map[string]any)["future_api"] != 1.5 ||
		doc["appearance"].(map[string]any)["theme"] != ThemeLight {
		t.Errorf("merged file = %s", raw)
	}
	// Environment variables still win over the file.
	withEnv, err := LoadEnv(ModeDesktop, env(map[string]string{"NOTES_DATA_DIR": data, "NOTES_AI_BASE_URL": "http://x/v1"}))
	if err != nil || withEnv.AI.BaseURL != "http://x/v1" {
		t.Errorf("env over file = %q, %v", withEnv.AI.BaseURL, err)
	}
}

// SaveChanges persists only what an edit changed: values from NOTES_*
// variables, flags and defaults stay out of settings.json, and a value
// changed to "" clears the key.
func TestSaveChangesWritesOnlyTheEdit(t *testing.T) {
	data := t.TempDir()
	saved, envVault := t.TempDir(), t.TempDir()
	plain := env(map[string]string{"NOTES_DATA_DIR": data})
	s, err := LoadEnv(ModeDesktop, plain)
	if err != nil {
		t.Fatal(err)
	}
	prev := s.Clone()
	s.NotesDir = saved
	s.AI.APIKeyFile = "/keys/ai"
	if err := s.SaveChanges(prev); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.SettingsPath())
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 2 || doc["notes_dir"] != saved || len(doc["ai"].(map[string]any)) != 1 {
		t.Errorf("first save wrote more than the edit: %s", raw)
	}
	doc["future"] = "keep me"
	raw, _ = json.Marshal(doc)
	if err := os.WriteFile(s.SettingsPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// A theme change while NOTES_DIR, NOTES_AI_BASE_URL and a --static style
	// override are in effect writes the theme only.
	withEnv, err := LoadEnv(ModeDesktop, env(map[string]string{
		"NOTES_DATA_DIR": data, "NOTES_DIR": envVault, "NOTES_AI_BASE_URL": "http://example.test/v1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	withEnv.StaticDir = "/some/dir"
	prev = withEnv.Clone()
	withEnv.Appearance.Theme = ThemeDark
	if err := withEnv.SaveChanges(prev); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEnv(ModeDesktop, plain)
	if err != nil {
		t.Fatal(err)
	}
	realSaved, _ := filepath.EvalSymlinks(saved)
	if loaded.NotesDir != realSaved || loaded.AI.BaseURL != "" || loaded.StaticDir != "" ||
		loaded.Appearance.Theme != ThemeDark || loaded.AI.APIKeyFile != "/keys/ai" {
		t.Errorf("loaded = %+v", loaded)
	}
	raw, _ = os.ReadFile(s.SettingsPath())
	if !strings.Contains(string(raw), `"future": "keep me"`) || strings.Contains(string(raw), "commit_delay_seconds") {
		t.Errorf("file = %s", raw)
	}

	// Values changed to "" are written, which clears them.
	prev = loaded.Clone()
	loaded.StaticDir = "/srv/ui"
	if err := loaded.SaveChanges(prev); err != nil {
		t.Fatal(err)
	}
	prev = loaded.Clone()
	loaded.StaticDir, loaded.AI.APIKeyFile = "", ""
	if err := loaded.SaveChanges(prev); err != nil {
		t.Fatal(err)
	}
	again, err := LoadEnv(ModeDesktop, plain)
	if err != nil {
		t.Fatal(err)
	}
	if again.StaticDir != "" || again.AI.APIKeyFile != "" {
		t.Errorf("cleared values came back: static_dir = %q, api_key_file = %q", again.StaticDir, again.AI.APIKeyFile)
	}

	// No change, no write.
	info, _ := os.Stat(s.SettingsPath())
	if err := os.Chmod(s.SettingsPath(), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := again.SaveChanges(again.Clone()); err != nil {
		t.Errorf("unchanged save = %v", err)
	}
	if after, _ := os.Stat(s.SettingsPath()); !after.ModTime().Equal(info.ModTime()) || after.Mode().Perm() != 0o400 {
		t.Error("an unchanged save rewrote the file")
	}
}

func TestBrokenSettingsFile(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, SettingsFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnv(ModeDesktop, env(map[string]string{"NOTES_DATA_DIR": data})); err == nil {
		t.Error("malformed settings.json accepted")
	}
	if err := os.WriteFile(filepath.Join(data, SettingsFileName), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnv(ModeDesktop, env(map[string]string{"NOTES_DATA_DIR": data})); err != nil {
		t.Errorf("blank settings.json: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	s := Settings{AI: AISettings{BaseURL: " http://h/v1/ "}, Appearance: AppearanceSettings{Theme: "neon"}, API: APISettings{Port: 70000}}
	s.Normalize()
	if s.AI.BaseURL != "http://h/v1" || s.Appearance.Theme != ThemeSystem || s.API.Port != 8190 || s.API.Host != "127.0.0.1" ||
		s.AllowedOrigins == nil || s.API.CORSOrigins == nil || s.AssetsDir != "_assets" {
		t.Errorf("normalized = %+v", s)
	}
	c := s.Clone()
	c.AllowedOrigins = append(c.AllowedOrigins, "x")
	if len(s.AllowedOrigins) != 0 {
		t.Error("Clone shares slices")
	}
}

func TestSecrets(t *testing.T) {
	s := Defaults(ModeDesktop)
	s.DataDir = filepath.Join(t.TempDir(), "data")
	secrets, err := s.LoadSecrets()
	if err != nil {
		t.Fatal(err)
	}
	urlsafe := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	for name, c := range map[string]struct {
		value string
		n     int
		file  string
	}{
		"password": {secrets.Password, 24, s.PasswordFile()},
		"session":  {secrets.SessionSecret, 64, s.SessionSecretFile()},
		"token":    {secrets.MCPToken, 43, s.MCPTokenFile()},
	} {
		if len(c.value) != c.n || !urlsafe.MatchString(c.value) {
			t.Errorf("%s = %q (len %d), want %d url-safe chars", name, c.value, len(c.value), c.n)
		}
		info, err := os.Stat(c.file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s file mode = %v, %v", name, info.Mode().Perm(), err)
		}
		raw, _ := os.ReadFile(c.file)
		if string(raw) != c.value+"\n" {
			t.Errorf("%s file = %q", name, raw)
		}
	}
	again, err := s.LoadSecrets()
	if err != nil || again != secrets {
		t.Errorf("secrets not stable: %v", err)
	}
	// A blank file is regenerated; surrounding whitespace is ignored.
	if err := os.WriteFile(s.PasswordFile(), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.SessionSecretFile(), []byte("  kept \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := s.LoadSecrets()
	if err != nil || third.Password == "" || third.Password == secrets.Password || third.SessionSecret != "kept" {
		t.Errorf("third = %+v, %v", third, err)
	}
	rotated, err := s.RotateMCPToken()
	if err != nil || rotated == secrets.MCPToken || len(rotated) != 43 {
		t.Errorf("rotate = %q, %v", rotated, err)
	}
	if latest, _ := s.LoadSecrets(); latest.MCPToken != rotated {
		t.Error("rotated token not persisted")
	}
}

func TestAIKeyAndConfigured(t *testing.T) {
	s := Defaults(ModeDesktop)
	s.DataDir = t.TempDir()
	if s.AIAPIKey() != "" || s.AIConfigured() {
		t.Error("fresh settings must not be configured")
	}
	s.AI.BaseURL = "https://api.example.com/v1"
	if s.AIConfigured() {
		t.Error("remote endpoint without key must not be configured")
	}
	if err := s.SetAIAPIKey("  sk-test \n"); err != nil {
		t.Fatal(err)
	}
	if s.AIAPIKey() != "sk-test" || !s.AIConfigured() {
		t.Errorf("key = %q", s.AIAPIKey())
	}
	if info, _ := os.Stat(s.AIAPIKeyFile()); info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", info.Mode().Perm())
	}
	if err := s.SetAIAPIKey(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.AIAPIKeyFile()); !os.IsNotExist(err) {
		t.Error("empty key must delete the file")
	}
	if err := s.SetAIAPIKey(""); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
	for url, want := range map[string]bool{
		"http://127.0.0.1:3425/v1": true, "http://localhost:8080": true, "http://[::1]:1/v1": true,
		"http://app.localhost/v1": true, "http://127.8.9.10": true, "https://api.example.com": false,
		"http://10.0.0.1:3425": false, "not a url": false, "": false,
	} {
		if got := IsLoopbackURL(url); got != want {
			t.Errorf("IsLoopbackURL(%q) = %v", url, got)
		}
	}
	s.AI.BaseURL = "http://127.0.0.1:3425/v1"
	if !s.AIConfigured() {
		t.Error("loopback endpoint without key must be configured")
	}
}

func TestCheckNotesDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.md")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if got, err := CheckNotesDir(" " + dir + "/ "); err != nil || got != real {
		t.Errorf("CheckNotesDir = %q, %v", got, err)
	}
	for _, bad := range []string{"", "relative/path", file, filepath.Join(dir, "missing")} {
		if _, err := CheckNotesDir(bad); err == nil {
			t.Errorf("CheckNotesDir(%q) accepted", bad)
		}
	}
}
