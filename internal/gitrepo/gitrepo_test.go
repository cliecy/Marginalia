package gitrepo

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// isolateGit keeps the developer's git configuration (signing, hooks,
// default branch) out of the tests. Repo copies the environment in New.
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	// No detached background maintenance racing with TempDir cleanup.
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	t.Setenv("GIT_CONFIG_VALUE_0", "0")
	t.Setenv("GIT_CONFIG_KEY_1", "maintenance.auto")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	root := t.TempDir()
	sh(t, root, "git", "init", "-q", "-b", "master")
	repo := New(Options{Root: root, AuthorName: "t", AuthorEmail: "t@x", Enabled: true, Delay: time.Hour})
	t.Cleanup(repo.Close)
	return repo, root
}

// Autocommit after an edit.
func TestAutocommit(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	write(t, filepath.Join(root, "n.md"), "one")
	repo.Record([]string{"n.md"}, "web: edit n.md", false)
	repo.Flush()
	write(t, filepath.Join(root, "n.md"), "two")
	repo.Record([]string{"n.md"}, "web: edit n.md", true)
	history := repo.History("n.md", DefaultHistoryLimit)
	if len(history) != 2 {
		t.Fatalf("history = %+v", history)
	}
	got, err := repo.Show(history[1].Rev, "n.md")
	if err != nil || got != "one" {
		t.Errorf("Show = %q, %v", got, err)
	}
	if history[0].Subject != "web: edit n.md" || history[0].Author != "t" ||
		!regexp.MustCompile(`^\d{4}-\d\d-\d\dT`).MatchString(history[0].Date) || len(history[0].Rev) != 40 {
		t.Errorf("entry = %+v", history[0])
	}
}

func TestCommitMessageAndBody(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	write(t, filepath.Join(root, "b.md"), "b")
	write(t, filepath.Join(root, "a.md"), "a")
	repo.Record([]string{"b.md"}, "web: edit b.md", false)
	repo.Record([]string{"a.md"}, "web: create a.md", false)
	if repo.Pending() != 2 {
		t.Errorf("pending = %d", repo.Pending())
	}
	repo.Flush()
	msg := sh(t, root, "git", "log", "-1", "--format=%B")
	if msg != "web: update 2 files\n\n- a.md\n- b.md\n\n" {
		t.Errorf("message = %q", msg)
	}
	// Nothing staged: no empty commit.
	repo.Record([]string{"a.md"}, "web: edit a.md", true)
	if n := strings.TrimSpace(sh(t, root, "git", "rev-list", "--count", "HEAD")); n != "1" {
		t.Errorf("commits = %s, want 1", n)
	}
	// Deletions are committed too.
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	repo.Record([]string{"a.md"}, "web: delete a.md", true)
	if msg := sh(t, root, "git", "log", "-1", "--format=%s"); msg != "web: delete a.md\n" {
		t.Errorf("delete commit = %q", msg)
	}
}

// A batch holding a path git cannot add (an untracked note that was moved
// away, a deleted untracked folder, an ignored file) must not keep the rest
// of the batch out of history.
func TestCommitSkipsUnaddablePaths(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	write(t, filepath.Join(root, ".gitignore"), "*.pdf\n")
	write(t, filepath.Join(root, "linker.md"), "see [[ext]]")
	repo.Record([]string{".gitignore", "linker.md"}, "web: init", true)

	// ext.md was created outside the app, so git never tracked it; moving it
	// rewrites linker.md. The batch is [ext.md (gone), moved.md, linker.md].
	write(t, filepath.Join(root, "moved.md"), "ext")
	write(t, filepath.Join(root, "linker.md"), "see [[moved]]")
	write(t, filepath.Join(root, "scan.pdf"), "%PDF")
	repo.Record([]string{"ext.md", "moved.md", "linker.md", "Empty", "scan.pdf"}, "web: move ext.md → moved.md", true)
	if n := strings.TrimSpace(sh(t, root, "git", "rev-list", "--count", "HEAD")); n != "2" {
		t.Fatalf("commits = %s, want 2", n)
	}
	files := strings.Fields(sh(t, root, "git", "show", "--name-only", "--format=", "HEAD"))
	if !slices.Equal(files, []string{"linker.md", "moved.md"}) {
		t.Errorf("last commit files = %q", files)
	}
	if msg := sh(t, root, "git", "log", "-1", "--format=%B"); !strings.Contains(msg, "- ext.md\n") || !strings.Contains(msg, "- scan.pdf") {
		t.Errorf("body should still list every path: %q", msg)
	}
	if status := sh(t, root, "git", "status", "--porcelain"); status != "" {
		t.Errorf("status = %q", status)
	}
	if repo.Pending() != 0 {
		t.Errorf("pending = %d", repo.Pending())
	}

	// Deleting a tracked folder stages the deletion of its files.
	write(t, filepath.Join(root, "Dir", "x.md"), "x")
	repo.Record([]string{"Dir/x.md"}, "web: create Dir/x.md", true)
	if err := os.RemoveAll(filepath.Join(root, "Dir")); err != nil {
		t.Fatal(err)
	}
	repo.Record([]string{"Dir"}, "web: delete Dir", true)
	if out := sh(t, root, "git", "ls-files", "Dir"); out != "" {
		t.Errorf("Dir still tracked: %q", out)
	}
	// A batch of nothing addable commits nothing and does not fail.
	repo.Record([]string{"nowhere.md"}, "web: delete nowhere.md", true)
	if n := strings.TrimSpace(sh(t, root, "git", "rev-list", "--count", "HEAD")); n != "4" {
		t.Errorf("commits = %s, want 4", n)
	}
}

// A failed commit puts its paths back in the queue once; a second failure
// drops them so a permanent error cannot loop.
func TestFailedCommitIsRetriedOnce(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	lock := filepath.Join(root, ".git", "index.lock")
	write(t, lock, "")
	write(t, filepath.Join(root, "a.md"), "a")
	repo.Record([]string{"a.md"}, "web: create a.md", true)
	repo.mu.Lock()
	retry := repo.timer != nil
	repo.mu.Unlock()
	if repo.Pending() != 1 || !retry {
		t.Fatalf("pending = %d, retry scheduled = %v", repo.Pending(), retry)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	repo.Flush()
	if repo.Pending() != 0 || len(repo.History("a.md", 5)) != 1 {
		t.Fatalf("retry did not commit: pending = %d", repo.Pending())
	}

	write(t, lock, "")
	write(t, filepath.Join(root, "a.md"), "b")
	repo.Record([]string{"a.md"}, "web: edit a.md", true)
	repo.Flush()
	if repo.Pending() != 0 {
		t.Errorf("second failure kept %d paths queued", repo.Pending())
	}
	repo.mu.Lock()
	timer := repo.timer
	repo.mu.Unlock()
	if timer != nil {
		t.Error("a retry was scheduled after the second failure")
	}
	// A new edit of the same path gets its retry back.
	repo.Record([]string{"a.md"}, "web: edit a.md", true)
	if repo.Pending() != 1 {
		t.Errorf("pending after a new failed edit = %d, want 1", repo.Pending())
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	repo.Close()
	if repo.Pending() != 0 || len(repo.History("a.md", 5)) != 2 {
		t.Errorf("Close did not commit the retried edit: pending = %d", repo.Pending())
	}
}

func TestReconfigure(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	write(t, filepath.Join(root, "a.md"), "a")
	repo.Record([]string{"a.md"}, "web: create a.md", false)
	repo.Reconfigure(Options{Root: "/ignored", AuthorName: "New Name", AuthorEmail: "new@x", Enabled: true, Delay: 20 * time.Millisecond})
	if repo.Root() != root || !repo.Enabled() {
		t.Fatalf("root = %q, enabled = %v", repo.Root(), repo.Enabled())
	}
	// The queued edit keeps its (one hour) timer; a new edit uses the new delay.
	write(t, filepath.Join(root, "b.md"), "b")
	repo.Record([]string{"b.md"}, "web: create b.md", false)
	deadline := time.Now().Add(5 * time.Second)
	for len(repo.History("b.md", 5)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the new delay was not used")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if author := sh(t, root, "git", "log", "-1", "--format=%an <%ae>"); author != "New Name <new@x>\n" {
		t.Errorf("author = %q", author)
	}
	// Switching auto-commit off disables the repository; switching it on
	// again re-checks .git.
	repo.Reconfigure(Options{Enabled: false})
	if repo.Enabled() {
		t.Error("still enabled")
	}
	repo.Record([]string{"c.md"}, "web: create c.md", true)
	if repo.Pending() != 0 {
		t.Error("disabled repo queued work")
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, "git.off")); err != nil {
		t.Fatal(err)
	}
	repo.Reconfigure(Options{Enabled: true})
	if repo.Enabled() {
		t.Error("enabled without .git")
	}
	if err := os.Rename(filepath.Join(root, "git.off"), filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	repo.Reconfigure(Options{Enabled: true, AuthorName: "t", AuthorEmail: "t@x", Delay: time.Hour})
	if !repo.Enabled() {
		t.Error("not enabled after .git came back")
	}
}

func TestDebounceTimer(t *testing.T) {
	isolateGit(t)
	root := t.TempDir()
	sh(t, root, "git", "init", "-q")
	repo := New(Options{Root: root, AuthorName: "t", AuthorEmail: "t@x", Enabled: true, Delay: 50 * time.Millisecond})
	t.Cleanup(repo.Close)
	write(t, filepath.Join(root, "n.md"), "x")
	repo.Record([]string{"n.md"}, "web: edit n.md", false)
	deadline := time.Now().Add(5 * time.Second)
	for len(repo.History("n.md", 5)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("debounced commit never happened")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if repo.Pending() != 0 {
		t.Errorf("pending = %d", repo.Pending())
	}
}

func TestDisabledRepo(t *testing.T) {
	isolateGit(t)
	root := t.TempDir() // no .git
	repo := New(Options{Root: root, Enabled: true, Delay: time.Second})
	if repo.Enabled() {
		t.Fatal("enabled without .git")
	}
	repo.Record([]string{"x.md"}, "m", true)
	if repo.Pending() != 0 {
		t.Error("disabled repo queued work")
	}
	st, err := repo.Status()
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(st); string(data) != `{"enabled":false}` {
		t.Errorf("status = %s", data)
	}
	if _, err := repo.Sync(); err == nil || err.Error() != "git 未启用" {
		t.Errorf("sync = %v", err)
	}
	if h := repo.History("x.md", 5); h == nil || len(h) != 0 {
		t.Errorf("history = %#v", h)
	}
	// Enabled=false wins over an existing .git.
	_, gitRoot := newRepo(t)
	if New(Options{Root: gitRoot, Enabled: false}).Enabled() {
		t.Error("Enabled=false ignored")
	}
}

func TestShowFollowsRenames(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	write(t, filepath.Join(root, "old.md"), "original\r\ncontent\n")
	repo.Record([]string{"old.md"}, "web: create old.md", true)
	// The app renames on disk (vault.Move) and records both paths.
	if err := os.Rename(filepath.Join(root, "old.md"), filepath.Join(root, "new.md")); err != nil {
		t.Fatal(err)
	}
	repo.Record([]string{"old.md", "new.md"}, "web: move old.md -> new.md", true)
	write(t, filepath.Join(root, "new.md"), "changed\n")
	repo.Record([]string{"new.md"}, "web: edit new.md", true)

	history := repo.History("new.md", DefaultHistoryLimit)
	if len(history) != 3 {
		t.Fatalf("history = %+v", history)
	}
	oldest := history[2].Rev
	got, err := repo.Show(oldest[:7], "new.md")
	// Like Python's text mode, CRLF comes back as LF.
	if err != nil || got != "original\ncontent\n" {
		t.Errorf("Show(old rev) = %q, %v", got, err)
	}
	for _, bad := range []string{"HEAD", "abc", "ABCDEF1", "../../etc", strings.Repeat("a", 41)} {
		if _, err := repo.Show(bad, "new.md"); !errors.Is(err, ErrInvalidRevision) {
			t.Errorf("Show(%q) = %v", bad, err)
		}
	}
	if _, err := repo.Show("0000000", "new.md"); err == nil {
		t.Error("unknown revision accepted")
	}
	if h := repo.History("new.md", 1); len(h) != 1 {
		t.Errorf("limit ignored: %d", len(h))
	}
}

// Sync with a bare remote.
func TestSyncWithBareRemote(t *testing.T) {
	isolateGit(t)
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	a, b := filepath.Join(tmp, "a"), filepath.Join(tmp, "b")
	sh(t, tmp, "git", "init", "-q", "--bare", "-b", "master", remote)
	sh(t, tmp, "git", "clone", "-q", remote, a)
	sh(t, a, "git", "checkout", "-q", "-b", "master")
	write(t, filepath.Join(a, "n.md"), "one\n")
	sh(t, a, "git", "add", ".")
	sh(t, a, "git", "commit", "-qm", "init")
	sh(t, a, "git", "push", "-q", "origin", "master")
	sh(t, tmp, "git", "clone", "-q", remote, b)

	repoA := New(Options{Root: a, AuthorName: "t", AuthorEmail: "t@x", Enabled: true, Delay: time.Hour})
	repoB := New(Options{Root: b, AuthorName: "t", AuthorEmail: "t@x", Enabled: true, Delay: time.Hour})
	t.Cleanup(repoA.Close)
	t.Cleanup(repoB.Close)
	write(t, filepath.Join(b, "other.md"), "from b\n")
	repoB.Record([]string{"other.md"}, "web: b", false)
	res, err := repoB.Sync()
	if err != nil || res.Pushed != 1 {
		t.Fatalf("sync b = %+v, %v", res, err)
	}

	write(t, filepath.Join(a, "n.md"), "two\n")
	repoA.Record([]string{"n.md"}, "web: a", false)
	res, err = repoA.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if res.Pulled != 1 || res.Pushed != 1 || !slices.Contains(res.Changed, "other.md") || !res.OK {
		t.Errorf("sync a = %+v", res)
	}
	st, err := repoA.Status()
	if err != nil || st.Ahead != 0 || st.Behind != 0 || st.Branch != "master" || st.Remote != remote || st.LastSync != res {
		t.Errorf("status = %+v, %v", st, err)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[+-]\d{4}$`).MatchString(res.At) {
		t.Errorf("at = %q", res.At)
	}
	data, _ := json.Marshal(st)
	if !strings.Contains(string(data), `"last_sync":{"at":`) || !strings.Contains(string(data), `"ok":true`) {
		t.Errorf("status json = %s", data)
	}

	// Conflicting edits are rejected without touching the local file.
	write(t, filepath.Join(b, "n.md"), "conflict b\n")
	repoB.Record([]string{"n.md"}, "web: b2", false)
	_, err = repoB.Sync()
	if err == nil || !strings.HasPrefix(err.Error(), "远程与本地修改冲突，已取消同步，本地内容未受影响。\n") {
		t.Fatalf("conflict sync = %v", err)
	}
	if got := read(t, filepath.Join(b, "n.md")); got != "conflict b\n" {
		t.Errorf("local file = %q", got)
	}
	last := repoB.LastSync()
	if last == nil || last.OK || last.Error == "" {
		t.Errorf("last sync = %+v", last)
	}
	data, _ = json.Marshal(last)
	if !regexp.MustCompile(`^\{"at":"[^"]+","ok":false,"error":"`).Match(data) {
		t.Errorf("failure json = %s", data)
	}
	if _, err := os.Stat(filepath.Join(b, ".git", "rebase-merge")); err == nil {
		t.Error("rebase left in progress")
	}
}

func TestSyncWithoutRemote(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	write(t, filepath.Join(root, "n.md"), "x")
	repo.Record([]string{"n.md"}, "web: x", false)
	_, err := repo.Sync()
	if err == nil || err.Error() != "仓库没有配置 origin 远程" {
		t.Errorf("sync = %v", err)
	}
	// The pending edit was committed before the sync failed.
	if len(repo.History("n.md", 5)) != 1 {
		t.Error("pending edit not committed")
	}
	if last := repo.LastSync(); last == nil || last.OK || last.Error != "仓库没有配置 origin 远程" {
		t.Errorf("last sync = %+v", last)
	}
	st, _ := repo.Status()
	if st.Remote != "" || st.Ahead != 0 || st.Uncommitted != 0 {
		t.Errorf("status = %+v", st)
	}
}

func TestFlushWaitsForSync(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	write(t, filepath.Join(root, "n.md"), "x")
	repo.mu.Lock()
	repo.syncing = true
	repo.mu.Unlock()
	repo.Record([]string{"n.md"}, "web: x", true)
	if repo.Pending() != 1 {
		t.Fatal("flush during sync must keep the queue")
	}
	repo.mu.Lock()
	retry := repo.timer != nil
	repo.syncing = false
	repo.mu.Unlock()
	if !retry {
		t.Error("no retry scheduled")
	}
	repo.Close()
	if repo.Pending() != 0 || len(repo.History("n.md", 5)) != 1 {
		t.Error("Close did not commit")
	}
}

func TestConcurrentSyncRefused(t *testing.T) {
	isolateGit(t)
	repo, _ := newRepo(t)
	repo.syncMu.Lock()
	_, err := repo.Sync()
	repo.syncMu.Unlock()
	if err == nil || err.Error() != "正在同步中，请稍候" {
		t.Errorf("sync = %v", err)
	}
}

func TestConcurrentRecords(t *testing.T) {
	isolateGit(t)
	repo, root := newRepo(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a'+i)) + ".md"
			write(t, filepath.Join(root, name), name)
			repo.Record([]string{name}, "web: "+name, i%2 == 0)
		}(i)
	}
	wg.Wait()
	repo.Close()
	out := sh(t, root, "git", "ls-files")
	if got := len(strings.Fields(out)); got != 6 {
		t.Errorf("committed files = %d (%q)", got, out)
	}
}

func TestSSHCommand(t *testing.T) {
	got := SSHCommand("/data/ssh/id_ed25519")
	want := "ssh -i /data/ssh/id_ed25519 -o IdentitiesOnly=yes -o UserKnownHostsFile=/data/ssh/known_hosts " +
		"-o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=15"
	if got != want {
		t.Errorf("SSHCommand = %q", got)
	}
	got = SSHCommand("/Users/x/Library/Application Support/Marginalia/ssh/id_ed25519")
	if !strings.Contains(got, "-i '/Users/x/Library/Application Support/Marginalia/ssh/id_ed25519' ") ||
		!strings.Contains(got, "UserKnownHostsFile='/Users/x/Library/Application Support/Marginalia/ssh/known_hosts' ") {
		t.Errorf("quoted SSHCommand = %q", got)
	}
	if q := shellQuote("it's"); q != `'it'"'"'s'` {
		t.Errorf("shellQuote = %s", q)
	}
}

func TestEnsureSSHKeyAndInit(t *testing.T) {
	isolateGit(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := filepath.Join(t.TempDir(), "data dir")
	key := filepath.Join(dir, "ssh", "id_ed25519")
	got, err := EnsureSSHKey(key)
	if err != nil || got != key {
		t.Fatalf("EnsureSSHKey = %q, %v", got, err)
	}
	if info, _ := os.Stat(filepath.Dir(key)); info.Mode().Perm() != 0o700 {
		t.Errorf("ssh dir mode = %v", info.Mode().Perm())
	}
	pub := PublicKey(key)
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " marginalia") {
		t.Errorf("public key = %q", pub)
	}
	before := read(t, key)
	if again, err := EnsureSSHKey(key); err != nil || again != key || read(t, key) != before {
		t.Error("existing key must be kept")
	}
	if PublicKey(filepath.Join(dir, "missing")) != "" {
		t.Error("missing public key")
	}

	vaultDir := filepath.Join(t.TempDir(), "new vault")
	if err := Init(vaultDir); err != nil {
		t.Fatal(err)
	}
	repo := New(Options{Root: vaultDir, AuthorName: "t", AuthorEmail: "t@x", Enabled: true, Delay: time.Hour,
		SSHKey: key})
	if !repo.Enabled() {
		t.Fatal("initialised repo not enabled")
	}
	write(t, filepath.Join(vaultDir, "AGENTS.md"), "# rules\n")
	repo.Record([]string{"AGENTS.md"}, "init: 初始化知识库", true)
	if h := repo.History("AGENTS.md", 5); len(h) != 1 || h[0].Subject != "init: 初始化知识库" {
		t.Errorf("history = %+v", h)
	}
}
