package magpie

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CLI timeouts.
const (
	// DefaultCLITimeout bounds `magpie library mcp add|rm`.
	DefaultCLITimeout = 30 * time.Second
	// DefaultStatusTimeout bounds `magpie library` (a status read).
	DefaultStatusTimeout = 5 * time.Second
	// maxOutput caps the command output kept for the API.
	maxOutput = 64 << 10
)

// ErrNoCLI is returned when the magpie command-line tool cannot be found.
var ErrNoCLI = errors.New("没有找到 magpie 命令行工具")

// Result is the outcome of a CLI command, as the register/unregister
// endpoints return it.
type Result struct {
	// OK is true when the command exited with status 0.
	OK bool `json:"ok"`
	// Output is the combined stdout and stderr, trimmed (and capped).
	Output string `json:"output"`
	// Command is the command line that ran, shell-quoted for display.
	Command string `json:"command"`
}

// AppCLIPaths lists where the magpie executable lives inside an installed
// Magpie.app, in the order FindCLI tries them after $PATH.
func AppCLIPaths() []string {
	paths := []string{"/Applications/magpie.app/Contents/MacOS/magpie"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, "Applications", "magpie.app", "Contents", "MacOS", "magpie"))
	}
	return paths
}

// FindCLI returns the absolute path of the magpie executable: `magpie` on
// $PATH first, then the copies inside Magpie.app (AppCLIPaths). It returns ""
// when Magpie is not installed.
func FindCLI() string {
	if p, err := exec.LookPath("magpie"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	for _, p := range AppCLIPaths() {
		if isExecutable(p) {
			return p
		}
	}
	return ""
}

func isExecutable(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// CLI returns the magpie executable this client uses, or "" when there is
// none.
func (c *Client) CLI() string {
	if c.CLIPath != "" {
		if isExecutable(c.CLIPath) {
			return c.CLIPath
		}
		return ""
	}
	return FindCLI()
}

// RegisterMCP adds an MCP server called name to Magpie's library, which
// hands it to every agent Magpie manages:
//
//	magpie library mcp add <name> <url>            (Streamable HTTP)
//	magpie library mcp add <name> <command> <args…> (stdio)
//
// It returns ErrNoCLI when Magpie is not installed; otherwise the Result
// tells whether the command succeeded and what it printed.
func (c *Client) RegisterMCP(ctx context.Context, name string, target ...string) (Result, error) {
	args := append([]string{"library", "mcp", "add", name}, target...)
	return c.run(ctx, c.cliTimeout(), args...)
}

// UnregisterMCP removes the MCP server called name from Magpie's library
// (`magpie library mcp rm <name>`).
func (c *Client) UnregisterMCP(ctx context.Context, name string) (Result, error) {
	return c.run(ctx, c.cliTimeout(), "library", "mcp", "rm", name)
}

// Library runs `magpie library`, which prints what the library gives each
// agent. It changes nothing.
func (c *Client) Library(ctx context.Context) (Result, error) {
	timeout := c.StatusTimeout
	if timeout <= 0 {
		timeout = DefaultStatusTimeout
	}
	return c.run(ctx, timeout, "library")
}

// MCPRegistered reports, best effort, whether the library lists an MCP
// server called name: true or false when `magpie library` succeeded, nil
// when it could not tell (no CLI, Magpie not running, timeout).
func (c *Client) MCPRegistered(ctx context.Context, name string) *bool {
	res, err := c.Library(ctx)
	if err != nil || !res.OK {
		return nil
	}
	found := regexp.MustCompile(`(^|[^\w-])` + regexp.QuoteMeta(name) + `($|[^\w-])`).MatchString(res.Output)
	return &found
}

func (c *Client) cliTimeout() time.Duration {
	if c.CLITimeout > 0 {
		return c.CLITimeout
	}
	return DefaultCLITimeout
}

// run executes the CLI with args, bounded by timeout.
func (c *Client) run(ctx context.Context, timeout time.Duration, args ...string) (Result, error) {
	path := c.CLI()
	if path == "" {
		return Result{Command: QuoteCommand(append([]string{"magpie"}, args...))}, ErrNoCLI
	}
	res := Result{Command: QuoteCommand(append([]string{path}, args...))}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "TERM=dumb")
	cmd.Stdin = nil
	cmd.WaitDelay = time.Second // a child keeping the pipes open cannot hang us
	var out bytes.Buffer
	cmd.Stdout = &limitedBuffer{buf: &out, max: maxOutput}
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	res.Output = strings.TrimSpace(out.String())
	switch {
	case err == nil:
		res.OK = true
	case ctx.Err() != nil:
		res.Output = strings.TrimSpace(res.Output + "\n（magpie 命令超时）")
	case res.Output == "":
		res.Output = err.Error()
	}
	return res, nil
}

// limitedBuffer keeps the first max bytes written to it and drops the rest.
type limitedBuffer struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// QuoteCommand renders argv as a POSIX shell command line, quoting the
// arguments that need it.
func QuoteCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
