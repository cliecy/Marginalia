// Package gitrepo keeps the vault's git history: debounced auto-commits of
// edited files, per-file history and old versions, and sync with a remote
// (fetch, rebase, push). It is a behaviour-identical port of the Python
// backend's gitrepo.py and drives the git command line.
package gitrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"marginalia/internal/vault/pystr"
)

// revRE matches the revisions Show accepts (abbreviated or full SHA-1).
var revRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ErrInvalidRevision is returned by Show for revisions that are not hex
// commit ids (the API answers 404 with the message "invalid revision").
var ErrInvalidRevision = errors.New("invalid revision")

// Command timeouts.
const (
	defaultTimeout = 30 * time.Second
	networkTimeout = 120 * time.Second
	// syncRetryDelay is how long a flush waits when a sync is running.
	syncRetryDelay = 5 * time.Second
)

// Options configures a Repo.
type Options struct {
	Root        string        // the vault folder (the work tree)
	AuthorName  string        // author and committer name of auto-commits
	AuthorEmail string        // author and committer e-mail
	Enabled     bool          // auto-commit and sync; also requires Root/.git
	Delay       time.Duration // debounce delay of Record
	SSHKey      string        // private key for ssh remotes ("" = ssh defaults)
}

// Repo is the vault's git repository. All methods are safe for concurrent
// use.
//
// There must be at most one Repo per work tree: commits and syncs exclude
// each other only within one Repo. To change the author, the debounce delay
// or whether auto-commit is on, call Reconfigure instead of opening another.
type Repo struct {
	root string
	cfg  atomic.Pointer[repoConfig]

	mu       sync.Mutex        // guards pending, retried, timer, syncing, lastSync
	pending  map[string]string // path -> commit message
	retried  map[string]bool   // paths re-queued once after a failed commit
	timer    *time.Timer
	syncing  bool
	lastSync *SyncResult

	syncMu sync.Mutex // held for the whole of Sync
	workMu sync.Mutex // serialises commits and the sync's git operations
}

// repoConfig is the part of a Repo that Reconfigure replaces.
type repoConfig struct {
	enabled bool
	delay   time.Duration
	env     []string
}

// New returns the repository at opts.Root. It is enabled only when
// opts.Enabled is set and Root/.git exists; a disabled Repo ignores Record,
// reports {"enabled": false} and refuses Sync.
func New(opts Options) *Repo {
	r := &Repo{root: opts.Root, pending: map[string]string{}, retried: map[string]bool{}}
	r.cfg.Store(newConfig(opts.Root, opts))
	return r
}

// newConfig derives a Repo's settings from opts for the work tree root.
func newConfig(root string, opts Options) *repoConfig {
	_, err := os.Stat(filepath.Join(root, ".git"))
	env := os.Environ()
	env = append(env,
		"GIT_AUTHOR_NAME="+opts.AuthorName,
		"GIT_AUTHOR_EMAIL="+opts.AuthorEmail,
		"GIT_COMMITTER_NAME="+opts.AuthorName,
		"GIT_COMMITTER_EMAIL="+opts.AuthorEmail,
		"GIT_TERMINAL_PROMPT=0",
	)
	if opts.SSHKey != "" {
		env = append(env, "GIT_SSH_COMMAND="+SSHCommand(opts.SSHKey))
	}
	return &repoConfig{enabled: opts.Enabled && err == nil, delay: opts.Delay, env: env}
}

// Reconfigure applies new options (opts.Root is ignored) to the running
// repository, re-checking whether the work tree has a .git folder. Edits that
// are already queued keep their timer and are committed with the new author;
// they are committed even when auto-commit was just switched off. A sync in
// progress finishes with the environment it started with.
func (r *Repo) Reconfigure(opts Options) {
	r.cfg.Store(newConfig(r.root, opts))
}

// SSHCommand is the GIT_SSH_COMMAND used with a deploy key: the key only,
// known hosts next to the key, new host keys accepted, never prompting.
// Paths are shell-quoted when needed (the macOS data folder contains a
// space); simple paths produce exactly the Python backend's string.
func SSHCommand(key string) string {
	knownHosts := filepath.Join(filepath.Dir(key), "known_hosts")
	return fmt.Sprintf("ssh -i %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s "+
		"-o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=15",
		shellQuote(key), shellQuote(knownHosts))
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

// shellQuote is Python's shlex.quote.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Root returns the work tree folder.
func (r *Repo) Root() string { return r.root }

// Enabled reports whether auto-commit and sync are active.
func (r *Repo) Enabled() bool { return r.cfg.Load().enabled }

// gitError is a failed git command; its message is git's stderr.
type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }

// run executes git with the backend's fixed options in the work tree and
// returns stdout (decoded like Python's text mode: invalid UTF-8 replaced,
// universal newlines). With check, a non-zero exit becomes an error carrying
// stderr (or "git <cmd> failed").
//
// core.fsmonitor is forced off: it names a command git runs from the
// repository's own config, and that config is not something the app should
// execute behind the user's back on every auto-commit.
func (r *Repo) run(check bool, timeout time.Duration, args ...string) (string, error) {
	return r.runInput("", check, timeout, args...)
}

// runInput is run with stdin.
func (r *Repo) runInput(stdin string, check bool, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := append([]string{"-c", "safe.directory=*", "-c", "core.quotepath=false", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = r.root
	cmd.Env = r.cfg.Load().env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("Command '%s' timed out after %s seconds", strings.Join(append([]string{"git"}, full...), " "),
			strconv.FormatFloat(timeout.Seconds(), 'f', -1, 64))
	}
	out := pystr.ReadText([]byte(stdout.String()))
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return out, err // git missing, cannot start, …
		}
		if check {
			msg := pystr.Strip(pystr.ReadText([]byte(stderr.String())))
			if msg == "" {
				msg = fmt.Sprintf("git %s failed", args[0])
			}
			return out, &gitError{msg}
		}
	}
	return out, nil
}

// ------------------------------------------------------------- committing

// Record queues paths for commit with message. Edits are debounced: every
// call restarts the Delay timer, and when it fires all queued paths are
// committed together. With immediate the queue is committed right away.
// Record does nothing when the repository is disabled.
func (r *Repo) Record(paths []string, message string, immediate bool) {
	cfg := r.cfg.Load()
	if !cfg.enabled {
		return
	}
	r.mu.Lock()
	for _, p := range paths {
		r.pending[p] = message
		delete(r.retried, p) // a new edit earns a new retry
	}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if !immediate {
		r.timer = time.AfterFunc(cfg.delay, r.Flush)
	}
	r.mu.Unlock()
	if immediate {
		r.Flush()
	}
}

// Flush commits the queued paths now. The subject is the shared message, or
// "web: update N files" when they differ; the body lists the paths. While a
// sync is rebasing, the flush is postponed by five seconds instead.
// Failures are logged and never lose the files themselves; the paths of a
// failed commit are queued again once and retried five seconds later.
func (r *Repo) Flush() {
	r.mu.Lock()
	if r.syncing {
		if len(r.pending) > 0 && r.timer == nil {
			r.timer = time.AfterFunc(syncRetryDelay, r.Flush)
		}
		r.mu.Unlock()
		return
	}
	pending := r.pending
	r.pending = map[string]string{}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	r.workMu.Lock()
	err := r.commit(pending)
	r.workMu.Unlock()
	r.settle(pending, err)
}

// settle updates the retry bookkeeping after a commit of batch: on success
// its paths may be retried again later; on failure every path that has not
// been retried yet, and was not queued again meanwhile, goes back into the
// queue and a retry is scheduled. A path that fails twice is dropped (and
// logged), so a permanent failure cannot loop.
func (r *Repo) settle(batch map[string]string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	requeued := false
	for p, msg := range batch {
		switch {
		case err == nil || r.retried[p]:
			delete(r.retried, p)
		default:
			if _, newer := r.pending[p]; !newer {
				r.pending[p] = msg
			}
			r.retried[p] = true
			requeued = true
		}
	}
	if requeued && r.timer == nil {
		r.timer = time.AfterFunc(syncRetryDelay, r.Flush)
	}
}

// commit runs `git add` and `git commit` for the given path -> message map.
// Paths that no longer exist and that git does not track (an untracked note
// that was moved or deleted, an empty folder) and paths git ignores are left
// out of the pathspecs: git would refuse the whole batch for any one of them.
// The commit body still lists every path of the batch.
func (r *Repo) commit(pending map[string]string) error {
	paths := make([]string, 0, len(pending))
	messageSet := map[string]bool{}
	for p, msg := range pending {
		paths = append(paths, p)
		messageSet[msg] = true
	}
	sort.Strings(paths)
	var subject string
	if len(messageSet) == 1 {
		for msg := range messageSet {
			subject = msg
		}
	} else {
		subject = fmt.Sprintf("web: update %d files", len(paths))
	}
	lines := make([]string, len(paths))
	for i, p := range paths {
		lines[i] = "- " + p
	}
	body := strings.Join(lines, "\n")
	err := func() error {
		keep, err := r.committable(paths)
		if err != nil || len(keep) == 0 {
			return err
		}
		if _, err := r.run(true, defaultTimeout, append([]string{"add", "-A", "--"}, keep...)...); err != nil {
			return err
		}
		staged, err := r.run(true, defaultTimeout, append([]string{"diff", "--cached", "--name-only", "--"}, keep...)...)
		if err != nil {
			return err
		}
		if pystr.Strip(staged) == "" {
			return nil
		}
		_, err = r.run(true, defaultTimeout, append([]string{"commit", "--no-verify", "-m", subject, "-m", body, "--"}, keep...)...)
		return err
	}()
	if err != nil {
		slog.Error("auto-commit failed", "paths", paths, "err", err)
	}
	return err
}

// committable returns the paths `git add -A` can be given: those that exist
// in the work tree and are not ignored, plus missing ones that git tracks
// (deletions, including deleted folders that held tracked files).
func (r *Repo) committable(paths []string) ([]string, error) {
	var present, missing []string
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(r.root, filepath.FromSlash(p))); err == nil {
			present = append(present, p)
		} else {
			missing = append(missing, p)
		}
	}
	keep := present
	if len(present) > 0 {
		// Exit status 1 means "nothing is ignored"; real failures exit 128.
		out, err := r.runInput(strings.Join(present, "\x00")+"\x00", false, defaultTimeout, "check-ignore", "-z", "--stdin")
		if err != nil {
			return nil, err
		}
		if ignored := splitNUL(out); len(ignored) > 0 {
			keep = slices.DeleteFunc(slices.Clone(present), func(p string) bool { return slices.Contains(ignored, p) })
		}
	}
	if len(missing) > 0 {
		out, err := r.run(true, defaultTimeout, append([]string{"ls-files", "-z", "--"}, missing...)...)
		if err != nil {
			return nil, err
		}
		tracked := splitNUL(out)
		for _, p := range missing {
			if slices.ContainsFunc(tracked, func(t string) bool { return t == p || strings.HasPrefix(t, p+"/") }) {
				keep = append(keep, p)
			}
		}
	}
	return keep, nil
}

// splitNUL splits git's -z output into its entries.
func splitNUL(out string) []string {
	out = strings.TrimRight(out, "\x00")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\x00")
}

// Pending returns the number of paths waiting to be committed.
func (r *Repo) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// Close waits for a running sync, stops the debounce timer, commits
// whatever is pending (retrying a failed commit once at once rather than in
// five seconds) and waits for commits that are already running, so the work
// tree is quiescent when it returns. Call it before swapping vaults or
// exiting. Syncs started while Close runs are refused.
func (r *Repo) Close() {
	// A running sync postpones flushes, so wait for it to end first.
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	for range 2 {
		r.mu.Lock()
		if r.timer != nil {
			r.timer.Stop()
			r.timer = nil
		}
		r.mu.Unlock()
		r.Flush()
		// Wait for commits started by a timer or another goroutine.
		r.workMu.Lock()
		r.workMu.Unlock()
		if r.Pending() == 0 {
			break
		}
	}
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.mu.Unlock()
}

// ------------------------------------------------------------------ sync

// SyncResult is the outcome of the last Sync (also part of Status).
type SyncResult struct {
	At      string   // local time, e.g. "2025-10-26T21:30:00+0900"
	OK      bool     // false when the sync failed
	Pulled  int      // remote commits rebased onto (OK only)
	Pushed  int      // local commits pushed (OK only)
	Changed []string // files changed by the pull (OK only)
	Error   string   // failure message, at most 500 characters (failures only)
}

// MarshalJSON emits {"at","pulled","pushed","changed","ok"} for a success
// and {"at","ok","error"} for a failure, like the Python dicts.
func (s SyncResult) MarshalJSON() ([]byte, error) {
	if !s.OK {
		return json.Marshal(struct {
			At    string `json:"at"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}{s.At, s.OK, s.Error})
	}
	changed := s.Changed
	if changed == nil {
		changed = []string{}
	}
	return json.Marshal(struct {
		At      string   `json:"at"`
		Pulled  int      `json:"pulled"`
		Pushed  int      `json:"pushed"`
		Changed []string `json:"changed"`
		OK      bool     `json:"ok"`
	}{s.At, s.Pulled, s.Pushed, changed, s.OK})
}

// Status describes the repository for GET /api/git/status.
type Status struct {
	Enabled     bool
	Remote      string      // URL of origin ("" when none)
	Branch      string      // current branch
	Ahead       int         // local commits not on origin/<branch>
	Behind      int         // remote commits not merged locally
	Pending     int         // paths queued for the next auto-commit
	Uncommitted int         // entries in `git status --porcelain`
	LastSync    *SyncResult // nil before the first sync
}

// MarshalJSON emits {"enabled": false} for a disabled repository and the
// full object otherwise.
func (s Status) MarshalJSON() ([]byte, error) {
	if !s.Enabled {
		return []byte(`{"enabled":false}`), nil
	}
	return json.Marshal(struct {
		Enabled     bool        `json:"enabled"`
		Remote      string      `json:"remote"`
		Branch      string      `json:"branch"`
		Ahead       int         `json:"ahead"`
		Behind      int         `json:"behind"`
		Pending     int         `json:"pending"`
		Uncommitted int         `json:"uncommitted"`
		LastSync    *SyncResult `json:"last_sync"`
	}{s.Enabled, s.Remote, s.Branch, s.Ahead, s.Behind, s.Pending, s.Uncommitted, s.LastSync})
}

// counts returns (behind, ahead) relative to upstream, (0, 0) when unknown.
func (r *Repo) counts(upstream string) (int, int, error) {
	out, err := r.run(false, defaultTimeout, "rev-list", "--left-right", "--count", upstream+"...HEAD")
	if err != nil {
		return 0, 0, err
	}
	fields := pystr.Fields(out)
	if len(fields) != 2 {
		return 0, 0, nil
	}
	behind, err1 := strconv.Atoi(fields[0])
	ahead, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, nil
	}
	return behind, ahead, nil
}

// Status reports the branch, remote, divergence and pending work.
func (r *Repo) Status() (Status, error) {
	if !r.Enabled() {
		return Status{}, nil
	}
	remoteOut, err := r.run(false, defaultTimeout, "remote", "get-url", "origin")
	if err != nil {
		return Status{}, err
	}
	branchOut, err := r.run(false, defaultTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Status{}, err
	}
	remote, branch := pystr.Strip(remoteOut), pystr.Strip(branchOut)
	behind, ahead := 0, 0
	if remote != "" {
		if behind, ahead, err = r.counts("origin/" + branch); err != nil {
			return Status{}, err
		}
	}
	porcelain, err := r.run(false, defaultTimeout, "status", "--porcelain")
	if err != nil {
		return Status{}, err
	}
	dirty := 0
	for _, line := range pystr.SplitLines(porcelain) {
		if pystr.Strip(line) != "" {
			dirty++
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{
		Enabled: true, Remote: remote, Branch: branch, Ahead: ahead, Behind: behind,
		Pending: len(r.pending), Uncommitted: dirty, LastSync: r.lastSync,
	}, nil
}

// LastSync returns the result of the most recent Sync (nil before the first).
func (r *Repo) LastSync() *SyncResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSync
}

// Sync commits pending edits, rebases local commits onto origin/<branch>
// (autostashing uncommitted changes) and pushes. A conflicting rebase is
// aborted, leaving the work tree untouched, and reported as
// "远程与本地修改冲突，已取消同步，本地内容未受影响。\n<git output>".
// The API answers 409 with the error text for every Sync error.
func (r *Repo) Sync() (*SyncResult, error) {
	if !r.Enabled() {
		return nil, errors.New("git 未启用")
	}
	if !r.syncMu.TryLock() {
		return nil, errors.New("正在同步中，请稍候")
	}
	defer r.syncMu.Unlock()

	r.Flush()
	r.mu.Lock()
	r.syncing = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.syncing = false
		r.mu.Unlock()
	}()

	r.workMu.Lock()
	result, err := r.doSync()
	r.workMu.Unlock()

	at := time.Now().Format("2006-01-02T15:04:05-0700")
	if err != nil {
		msg := err.Error()
		if pystr.Len(msg) > 500 {
			msg = pystr.Head(msg, 500)
		}
		failed := &SyncResult{At: at, OK: false, Error: msg}
		r.mu.Lock()
		r.lastSync = failed
		r.mu.Unlock()
		return nil, err
	}
	result.At = at
	r.mu.Lock()
	r.lastSync = result
	r.mu.Unlock()
	return result, nil
}

func (r *Repo) doSync() (*SyncResult, error) {
	remote, err := r.run(false, defaultTimeout, "remote", "get-url", "origin")
	if err != nil {
		return nil, err
	}
	if pystr.Strip(remote) == "" {
		return nil, errors.New("仓库没有配置 origin 远程")
	}
	branchOut, err := r.run(true, defaultTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	branch := pystr.Strip(branchOut)
	upstream := "origin/" + branch
	if _, err := r.run(true, networkTimeout, "fetch", "origin", branch); err != nil {
		return nil, err
	}
	behind, _, err := r.counts(upstream)
	if err != nil {
		return nil, err
	}
	changed := []string{}
	if behind > 0 {
		beforeOut, err := r.run(true, defaultTimeout, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		before := pystr.Strip(beforeOut)
		if _, err := r.run(true, networkTimeout, "rebase", "--autostash", upstream); err != nil {
			_, _ = r.run(false, defaultTimeout, "rebase", "--abort")
			return nil, fmt.Errorf("远程与本地修改冲突，已取消同步，本地内容未受影响。\n%w", err)
		}
		diff, err := r.run(true, defaultTimeout, "diff", "--name-only", before, "HEAD")
		if err != nil {
			return nil, err
		}
		for _, p := range pystr.SplitLines(diff) {
			if p != "" {
				changed = append(changed, p)
			}
		}
	}
	_, aheadAfter, err := r.counts(upstream)
	if err != nil {
		return nil, err
	}
	if aheadAfter > 0 {
		if _, err := r.run(true, networkTimeout, "push", "origin", "HEAD:"+branch); err != nil {
			return nil, err
		}
	}
	return &SyncResult{OK: true, Pulled: behind, Pushed: aheadAfter, Changed: changed}, nil
}

// RunAutosync calls Sync every interval until ctx is cancelled, logging
// failures. It returns immediately when interval is not positive.
func (r *Repo) RunAutosync(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.Sync(); err != nil {
				slog.Warn("auto-sync failed", "err", err)
			}
		}
	}
}

// --------------------------------------------------------------- history

// Commit is one entry of a file's history.
type Commit struct {
	Rev     string `json:"rev"`     // full SHA-1
	Author  string `json:"author"`  // author name
	Date    string `json:"date"`    // strict ISO 8601 author date
	Subject string `json:"subject"` // first line of the message
}

// DefaultHistoryLimit is the number of commits GET /api/history returns.
const DefaultHistoryLimit = 50

// History lists the newest commits touching path (following renames). It is
// empty for a disabled repository or when git fails.
func (r *Repo) History(path string, limit int) []Commit {
	entries := []Commit{}
	if !r.Enabled() {
		return entries
	}
	out, err := r.run(false, defaultTimeout, "log", "--follow", fmt.Sprintf("-n%d", limit),
		"--format=%H%x1f%an%x1f%aI%x1f%s", "--", path)
	if err != nil {
		return entries
	}
	for _, line := range pystr.SplitLines(out) {
		parts := strings.Split(line, "\x1f")
		if len(parts) == 4 {
			entries = append(entries, Commit{Rev: parts[0], Author: parts[1], Date: parts[2], Subject: parts[3]})
		}
	}
	return entries
}

// Show returns the content of path at rev. Because history follows renames,
// the file may have had another name in that revision; Show looks it up.
// Revisions that are not 7–40 lowercase hex digits yield ErrInvalidRevision;
// other failures carry git's message. Both map to 404 in the API.
func (r *Repo) Show(rev, path string) (string, error) {
	if !revRE.MatchString(rev) {
		return "", ErrInvalidRevision
	}
	names, err := r.run(false, defaultTimeout, "log", "--follow", "--name-only", "--format=%H", "--", path)
	if err != nil {
		return "", err
	}
	currentRev, lookup := "", path
	for _, line := range pystr.SplitLines(names) {
		line = pystr.Strip(line)
		if revRE.MatchString(line) && len(line) == 40 {
			currentRev = line
		} else if line != "" && currentRev != "" && strings.HasPrefix(currentRev, rev) {
			lookup = line
			break
		}
	}
	return r.run(true, defaultTimeout, "show", rev+":"+lookup)
}

// ----------------------------------------------------------------- setup

// Init runs `git init` in dir (creating it when needed).
func Init(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	cmd := exec.Command("git", "-c", "safe.directory=*", "init", "-q")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("git init: %s", msg)
		}
		return fmt.Errorf("git init: %w", err)
	}
	return nil
}

// EnsureSSHKey creates the ed25519 deploy key used to push the vault
// (`ssh-keygen -q -t ed25519 -N "" -C marginalia -f path`) unless it
// already exists, and returns its path. The key folder is created with mode
// 0700. On failure it returns "" and the error; callers continue without a
// key, as the Python backend did.
func EnsureSSHKey(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("could not create SSH deploy key: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "marginalia", "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return "", fmt.Errorf("could not create SSH deploy key: %s", msg)
		}
		return "", fmt.Errorf("could not create SSH deploy key: %w", err)
	}
	return path, nil
}

// PublicKey returns the trimmed content of keyPath + ".pub", or "" when it
// does not exist.
func PublicKey(keyPath string) string {
	data, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
