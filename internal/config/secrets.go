package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Secret lengths in random bytes, as in the Python backend.
const (
	PasswordBytes      = 18
	SessionSecretBytes = 48
	MCPTokenBytes      = 32
)

// PasswordFile is DataDir/password: the web login password.
func (s Settings) PasswordFile() string { return filepath.Join(s.DataDir, "password") }

// SessionSecretFile is DataDir/session_secret: the session cookie HMAC key.
func (s Settings) SessionSecretFile() string { return filepath.Join(s.DataDir, "session_secret") }

// MCPTokenFile is DataDir/mcp_token: the Bearer token of the REST and MCP API.
func (s Settings) MCPTokenFile() string { return filepath.Join(s.DataDir, "mcp_token") }

// SSHKeyFile is DataDir/ssh/id_ed25519: the git deploy key.
func (s Settings) SSHKeyFile() string { return filepath.Join(s.DataDir, "ssh", "id_ed25519") }

// AIAPIKeyFile is the AI key file: AI.APIKeyFile when set (NOTES_AI_API_KEY_FILE),
// else DataDir/ai_api_key.
func (s Settings) AIAPIKeyFile() string {
	if s.AI.APIKeyFile != "" {
		return s.AI.APIKeyFile
	}
	return filepath.Join(s.DataDir, "ai_api_key")
}

// AIAPIKey reads the AI key on every call, so it can be added or rotated
// without a restart. A missing or unreadable file means no key.
func (s Settings) AIAPIKey() string {
	key, err := ReadOptional(s.AIAPIKeyFile())
	if err != nil {
		slog.Warn("cannot read AI API key", "file", s.AIAPIKeyFile(), "err", err)
		return ""
	}
	return key
}

// SetAIAPIKey stores key (0600) in AIAPIKeyFile; an empty key deletes the
// file.
func (s Settings) SetAIAPIKey(key string) error {
	key = strings.TrimSpace(key)
	path := s.AIAPIKeyFile()
	if key == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return writeFileAtomic(path, []byte(key+"\n"), 0o600)
}

// AIConfigured reports whether AI requests can be attempted: a base URL is
// set and either a key is stored or the endpoint is on the loopback interface
// (local gateways such as Magpie accept requests without a key; callers send
// no Authorization header when the key is empty).
func (s Settings) AIConfigured() bool {
	if s.AI.BaseURL == "" {
		return false
	}
	return s.AIAPIKey() != "" || IsLoopbackURL(s.AI.BaseURL)
}

// IsLoopbackURL reports whether raw is an http(s) URL whose host is
// localhost, a *.localhost name or a loopback IP address.
func IsLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Secrets are the generated credentials of an installation.
type Secrets struct {
	Password      string // web login password
	SessionSecret string // session cookie signing key
	MCPToken      string // Bearer token for REST and MCP
}

// LoadSecrets reads the password, session secret and API token, generating
// any that are missing (see ReadOrCreateSecret). DataDir is created first.
func (s Settings) LoadSecrets() (Secrets, error) {
	if err := os.MkdirAll(s.DataDir, 0o700); err != nil {
		return Secrets{}, fmt.Errorf("config: %w", err)
	}
	var out Secrets
	var err error
	if out.Password, err = ReadOrCreateSecret(s.PasswordFile(), PasswordBytes); err != nil {
		return out, err
	}
	if out.SessionSecret, err = ReadOrCreateSecret(s.SessionSecretFile(), SessionSecretBytes); err != nil {
		return out, err
	}
	if out.MCPToken, err = ReadOrCreateSecret(s.MCPTokenFile(), MCPTokenBytes); err != nil {
		return out, err
	}
	return out, nil
}

// RotateMCPToken replaces the API token with a fresh random one and returns
// it. Clients using the old token are locked out immediately.
func (s Settings) RotateMCPToken() (string, error) {
	token, err := TokenURLSafe(MCPTokenBytes)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.DataDir, 0o700); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	if err := writeFileAtomic(s.MCPTokenFile(), []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// ReadOrCreateSecret returns the trimmed content of path. When the file is
// missing or blank it writes a new TokenURLSafe(n) value followed by a
// newline (creating parent folders; new files get mode 0600) and returns it.
func ReadOrCreateSecret(path string, n int) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			return value, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("config: read secret: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	value, err := TokenURLSafe(n)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("config: write secret: %w", err)
	}
	if _, err := f.WriteString(value + "\n"); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("config: write secret: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("config: write secret: %w", err)
	}
	return value, nil
}

// ReadOptional returns the trimmed content of path, or "" when it does not
// exist.
func ReadOptional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// TokenURLSafe is Python's secrets.token_urlsafe(n): n random bytes encoded
// as unpadded base64url.
func TokenURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("config: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
