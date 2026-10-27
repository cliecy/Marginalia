package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"marginalia/internal/ai"
	"marginalia/internal/config"
	"marginalia/internal/gitrepo"
	"marginalia/internal/mcp"
	"marginalia/internal/vault"
)

// CoreOptions configures NewCore.
type CoreOptions struct {
	// Mode is the run mode: config.ModeDesktop (the MyGo app) or
	// config.ModeServer (`marginalia serve`). It is reported by
	// GET /api/auth/me.
	Mode config.Mode
	// Version is the app version (GET /api/auth/me, MCP serverInfo, OpenAPI).
	Version string
	// DeployKey makes git use the deploy key DataDir/ssh/id_ed25519, created
	// on start when missing, like the Python server did. Use it in server
	// mode; the desktop app leaves it off so git uses the user's own SSH
	// setup.
	DeployKey bool
	// Logger receives the core's logs (nil = slog.Default()).
	Logger *slog.Logger
}

// Core owns the runtime state of an installation: the settings, the
// secrets, and the live vault, git repository, AI client and MCP server
// built from them. Apply and Update swap them atomically; every request
// works on one consistent snapshot (see state), so a settings change never
// tears a request in half.
//
// A Core is safe for concurrent use.
type Core struct {
	mode      config.Mode
	version   string
	deployKey string // ssh key path for git ("" = user's ssh setup)
	log       *slog.Logger

	password      string
	sessionSecret []byte
	token         atomic.Pointer[string]

	cur atomic.Pointer[state]

	mu        sync.Mutex // serialises Apply, Update, RotateToken and Close
	stopSync  context.CancelFunc
	closed    bool
	closeOnce sync.Once

	hooksMu  sync.Mutex // guards hooks and nextHook
	hooks    []changeHook
	nextHook uint64
}

// changeHook is a listener registered with OnChange.
type changeHook struct {
	id uint64
	fn func(prev, next config.Settings)
}

// state is one immutable snapshot of the runtime. vault, git and mcp are
// nil-safe: vault and git are nil when no vault is configured.
type state struct {
	settings config.Settings
	vault    *vault.Vault
	git      *gitrepo.Repo
	ai       *ai.Client
	mcp      *mcp.Server
}

// record queues a commit when the vault has a repository.
func (st *state) record(paths []string, message string, immediate bool) {
	if st.git != nil && len(paths) > 0 {
		st.git.Record(paths, message, immediate)
	}
}

// flush commits pending edits now (before moves, deletes and AI changes).
func (st *state) flush() {
	if st.git != nil {
		st.git.Flush()
	}
}

// NewCore loads (or creates) the secrets in s.DataDir and builds the runtime
// for s. s is used as given (callers pass the result of config.Load, which is
// already normalised). With opts.DeployKey the SSH deploy key is created
// when missing (failures are logged, as in Python). Periodic git sync starts
// when s.Git.AutosyncMinutes > 0.
func NewCore(s config.Settings, opts CoreOptions) (*Core, error) {
	if s.DataDir == "" {
		return nil, errors.New("server: settings have no data directory")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	secrets, err := s.LoadSecrets()
	if err != nil {
		return nil, err
	}
	c := &Core{
		mode:          opts.Mode,
		version:       opts.Version,
		log:           logger,
		password:      secrets.Password,
		sessionSecret: []byte(secrets.SessionSecret),
	}
	if c.version == "" {
		c.version = mcp.DefaultVersion
	}
	c.token.Store(&secrets.MCPToken)
	if opts.DeployKey {
		key, err := gitrepo.EnsureSSHKey(s.SSHKeyFile())
		if err != nil {
			logger.Warn("could not create SSH deploy key", "err", err)
		}
		c.deployKey = key
	}
	st := c.build(s.Clone(), nil)
	c.cur.Store(st)
	c.startAutosync(st)
	return c, nil
}

// Mode returns the run mode.
func (c *Core) Mode() config.Mode { return c.mode }

// Version returns the app version.
func (c *Core) Version() string { return c.version }

// Settings returns a copy of the current settings.
func (c *Core) Settings() config.Settings { return c.state().settings.Clone() }

// Vault returns the current vault, or nil when none is configured.
func (c *Core) Vault() *vault.Vault { return c.state().vault }

// Git returns the current vault's repository, or nil when no vault is
// configured. A repository may be disabled (see gitrepo.Repo.Enabled).
func (c *Core) Git() *gitrepo.Repo { return c.state().git }

// AI returns the current AI client.
func (c *Core) AI() *ai.Client { return c.state().ai }

// MCP returns the current MCP server (it answers tool calls with
// "未选择笔记库" while no vault is configured).
func (c *Core) MCP() *mcp.Server { return c.state().mcp }

// Token returns the API token (Bearer token of the REST and MCP API).
func (c *Core) Token() string { return *c.token.Load() }

// checkToken compares a presented token with the API token in constant time.
func (c *Core) checkToken(presented string) bool {
	return presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(c.Token())) == 1
}

// checkPassword compares a login password in constant time.
func (c *Core) checkPassword(presented string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(c.password)) == 1
}

// RotateToken replaces the API token (in DataDir/mcp_token and in memory)
// and returns the new one. Clients using the old token are locked out at
// once.
func (c *Core) RotateToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	token, err := c.state().settings.RotateMCPToken()
	if err != nil {
		return "", err
	}
	c.token.Store(&token)
	return token, nil
}

// state returns the current snapshot.
func (c *Core) state() *state { return c.cur.Load() }

// Apply switches the runtime to next. The vault, repository and MCP server
// are rebuilt only when the vault folder changes; the old repository is then
// closed, which commits its pending edits (Apply returns once that is done).
// Git settings changes reconfigure the open repository in place. Apply does
// not write settings.json (see Update).
func (c *Core) Apply(next config.Settings) error {
	next = next.Clone()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("server: core is closed")
	}
	prev := c.state().settings
	retired := c.applyLocked(next)
	c.mu.Unlock()
	closeRepo(retired)
	c.notify(prev, next)
	return nil
}

// Update edits a copy of the current settings with fn, saves the result to
// settings.json and applies it, all under the core's lock so concurrent
// updates do not lose each other's changes. When fn or saving fails nothing
// changes. It returns the applied settings.
func (c *Core) Update(fn func(*config.Settings) error) (config.Settings, error) {
	prev, next, err := c.update(fn)
	if err != nil {
		return config.Settings{}, err
	}
	c.notify(prev, next)
	return next.Clone(), nil
}

// update is Update without the change notification: it returns the
// settings before and after the change. Only the values fn changed are
// written to settings.json, so values that came from NOTES_* variables,
// `serve --static` or built-in defaults never end up in the file.
func (c *Core) update(fn func(*config.Settings) error) (prev, next config.Settings, err error) {
	var retired *gitrepo.Repo
	defer func() { closeRepo(retired) }() // after c.mu is released (deferred first, runs last)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return prev, next, errors.New("server: core is closed")
	}
	prev = c.state().settings
	next = prev.Clone()
	if err := fn(&next); err != nil {
		return prev, next, err
	}
	if err := next.SaveChanges(prev); err != nil {
		return prev, next, err
	}
	retired = c.applyLocked(next)
	return prev, next, nil
}

// closeRepo closes a repository that applyLocked replaced. It runs without
// the core's lock: Close waits for a running sync, which can take minutes,
// and settings changes must not wait for it.
func closeRepo(r *gitrepo.Repo) {
	if r != nil {
		r.Close()
	}
}

// OnChange registers fn to run after each settings change made by Apply or
// Update (the HTTP settings routes, vault initialisation, the Magpie
// integration…), with copies of the settings before and after it. The
// desktop app uses it to follow Appearance.Theme.
//
// fn runs synchronously on the goroutine that made the change, once the new
// settings are live and the core's lock is released, so it may read the
// core and even change settings again; keep it short. Changes made
// concurrently may be reported out of order: read Settings for the latest
// values rather than trusting next to be current. It returns a function
// that removes the listener.
func (c *Core) OnChange(fn func(prev, next config.Settings)) (off func()) {
	c.hooksMu.Lock()
	c.nextHook++
	id := c.nextHook
	c.hooks = append(c.hooks, changeHook{id: id, fn: fn})
	c.hooksMu.Unlock()
	return func() {
		c.hooksMu.Lock()
		defer c.hooksMu.Unlock()
		c.hooks = slices.DeleteFunc(slices.Clone(c.hooks), func(h changeHook) bool { return h.id == id })
	}
}

// notify calls the OnChange listeners, in registration order.
func (c *Core) notify(prev, next config.Settings) {
	c.hooksMu.Lock()
	hooks := c.hooks
	c.hooksMu.Unlock()
	for _, h := range hooks {
		h.fn(prev.Clone(), next.Clone())
	}
}

// applyLocked publishes the runtime for next and (re)starts periodic sync
// when the repository, its enabled state or the interval changed. When the
// repository was replaced it returns the old one, which the caller must
// close with closeRepo once c.mu is released.
func (c *Core) applyLocked(next config.Settings) (retired *gitrepo.Repo) {
	old := c.state()
	wasEnabled := old.git != nil && old.git.Enabled() // build may reconfigure old.git
	st := c.build(next, old)
	c.cur.Store(st)
	enabled := st.git != nil && st.git.Enabled()
	if st.git != old.git || enabled != wasEnabled || next.Git.AutosyncMinutes != old.settings.Git.AutosyncMinutes {
		if c.stopSync != nil {
			c.stopSync()
			c.stopSync = nil
		}
		c.startAutosync(st)
	}
	if st.git != old.git {
		return old.git
	}
	return nil
}

// build makes the runtime for s. While the vault folder stays the same,
// prev's vault, repository and MCP server are kept: a change of the git
// settings, or a vault that became (or stopped being) a git repository,
// reconfigures the repository in place, because two repositories on one
// work tree would commit and sync over each other.
func (c *Core) build(s config.Settings, prev *state) *state {
	st := &state{settings: s, ai: ai.NewFromSettings(s)}
	if prev != nil && prev.settings.NotesDir == s.NotesDir && (prev.git != nil || prev.settings.Git == s.Git) {
		st.vault, st.git, st.mcp = prev.vault, prev.git, prev.mcp
		if prev.git != nil && (prev.settings.Git != s.Git || repoChanged(prev)) {
			prev.git.Reconfigure(c.gitOptions(s, prev.vault))
		}
		return st
	}
	if s.NotesDir != "" {
		if info, err := os.Stat(s.NotesDir); err != nil || !info.IsDir() {
			c.log.Warn("the vault folder is not available; no vault is open", "dir", s.NotesDir, "err", err)
		} else if v, err := vault.New(s.NotesDir); err != nil {
			c.log.Warn("cannot open the vault", "dir", s.NotesDir, "err", err)
		} else {
			st.vault = v
			st.git = gitrepo.New(c.gitOptions(s, v))
		}
	}
	var git mcp.Git
	if st.git != nil {
		git = st.git
	}
	st.mcp = mcp.New(st.vault, git, mcp.Options{Version: c.version, Logger: c.log})
	return st
}

// gitOptions are the repository options for vault v under settings s.
func (c *Core) gitOptions(s config.Settings, v *vault.Vault) gitrepo.Options {
	return gitrepo.Options{
		Root:        v.Root(),
		AuthorName:  s.Git.AuthorName,
		AuthorEmail: s.Git.AuthorEmail,
		Enabled:     s.Git.Autocommit,
		Delay:       time.Duration(s.Git.CommitDelaySeconds * float64(time.Second)),
		SSHKey:      c.deployKey,
	}
}

// repoChanged reports whether st's vault gained or lost its .git folder
// since its repository was (re)configured (gitrepo checks it in New and
// Reconfigure).
func repoChanged(st *state) bool {
	if st.vault == nil || st.git == nil || !st.settings.Git.Autocommit {
		return false
	}
	_, err := os.Stat(filepath.Join(st.vault.Root(), ".git"))
	return (err == nil) != st.git.Enabled()
}

// startAutosync runs periodic git sync for st when configured.
func (c *Core) startAutosync(st *state) {
	minutes := st.settings.Git.AutosyncMinutes
	if st.git == nil || !st.git.Enabled() || minutes <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.stopSync = cancel
	go st.git.RunAutosync(ctx, time.Duration(minutes*float64(time.Minute)))
}

// Close stops periodic sync and closes the repository, committing pending
// edits and waiting for running commits. Call it on shutdown, after the HTTP
// listeners stopped. The core must not be used afterwards.
func (c *Core) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.closed = true
		if c.stopSync != nil {
			c.stopSync()
			c.stopSync = nil
		}
		if git := c.state().git; git != nil {
			git.Close()
		}
	})
}
