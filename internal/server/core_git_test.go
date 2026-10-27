package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marginalia/internal/config"
)

// gitFixture is a fixture whose vault is a real git repository with
// auto-commit on (one hour delay).
func gitFixture(t *testing.T) *fixture {
	t.Helper()
	needGit(t)
	f := newFixture(t, func(o *fixtureOptions) {
		o.mode = modeDesktop
		o.settings = func(s *settingsT) { s.Git.Autocommit, s.Git.CommitDelaySeconds = true, 3600 }
	})
	if err := os.RemoveAll(filepath.Join(f.notes, ".git")); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, f.notes, "init", "-q")
	if err := f.core.Apply(f.core.Settings()); err != nil {
		t.Fatal(err)
	}
	if !f.core.Git().Enabled() {
		t.Fatal("the repository was not enabled")
	}
	return f
}

// Changing git settings must not open a second repository on the same work
// tree (two repositories would commit and sync over each other), and must
// still take effect and restart periodic sync.
func TestGitSettingsChangeReconfiguresTheRepository(t *testing.T) {
	f := gitFixture(t)
	repo := f.core.Git()
	writeFile(t, f.notes, "A.md", "edited\n")
	repo.Record([]string{"A.md"}, "web: edit A.md", false)

	if _, err := f.core.Update(func(s *config.Settings) error {
		s.Git.AuthorName, s.Git.AuthorEmail = "Someone Else", "else@x"
		s.Git.AutosyncMinutes = 30
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if f.core.Git() != repo || !repo.Enabled() {
		t.Fatal("a git settings change replaced the repository")
	}
	if repo.Pending() != 1 {
		t.Errorf("pending = %d, want the queued edit kept", repo.Pending())
	}
	f.core.mu.Lock()
	syncing := f.core.stopSync != nil
	f.core.mu.Unlock()
	if !syncing {
		t.Error("periodic sync was not started for the new interval")
	}
	repo.Flush()
	if author := gitCmd(t, f.notes, "log", "-1", "--format=%an"); author != "Someone Else\n" {
		t.Errorf("author = %q", author)
	}

	// Switching auto-commit off disables the same repository and stops sync.
	if _, err := f.core.Update(func(s *config.Settings) error {
		s.Git.Autocommit = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.core.mu.Lock()
	syncing = f.core.stopSync != nil
	f.core.mu.Unlock()
	if f.core.Git() != repo || repo.Enabled() || syncing {
		t.Errorf("same repo = %v, enabled = %v, periodic sync = %v", f.core.Git() == repo, repo.Enabled(), syncing)
	}
}

// A sync that is still running must neither block settings changes nor
// overlap with commits of another repository object.
func TestSettingsChangesDoNotWaitForASync(t *testing.T) {
	f := gitFixture(t)
	writeFile(t, f.notes, "A.md", "first\n")
	f.core.Git().Record([]string{"A.md"}, "web: edit A.md", true)
	remote := filepath.Join(t.TempDir(), "remote.git")
	gitCmd(t, f.notes, "init", "-q", "--bare", remote)
	gitCmd(t, f.notes, "remote", "add", "origin", remote)
	gitCmd(t, f.notes, "push", "-q", "origin", "HEAD")
	// Every fetch now takes a while.
	gitCmd(t, f.notes, "config", "remote.origin.uploadpack", "sleep 1.5; git-upload-pack")

	done := make(chan error, 1)
	go func() {
		_, err := f.core.Git().Sync()
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // the sync is fetching

	start := time.Now()
	if _, err := f.core.Update(func(s *config.Settings) error {
		s.Appearance.Theme = config.ThemeDark
		s.Git.CommitDelaySeconds = 0.05
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 700*time.Millisecond {
		t.Errorf("a settings change waited %v for the running sync", took)
	}
	select {
	case err := <-done:
		t.Fatalf("the sync ended too early (%v); the test needs a slow fetch", err)
	default:
	}
	if err := <-done; err != nil {
		t.Fatalf("sync: %v", err)
	}
	if log := gitCmd(t, f.notes, "log", "--format=%s"); strings.Count(log, "\n") != 1 {
		t.Errorf("log = %q", log)
	}
}
