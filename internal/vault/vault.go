// Package vault gives safe filesystem access to a Markdown note vault (an
// Obsidian-compatible folder). It is a behaviour-identical port of the Python
// backend's vault.py.
//
// Every path that reaches this package may come from a browser, an external
// program or an AI agent, so all of them go through Vault.Resolve, which keeps
// access inside the vault (symlinks included) and away from hidden entries
// such as .git, .obsidian and the app's own .webapp folder.
//
// Paths exchanged with callers are vault-relative, slash-separated strings
// ("Knowledge/RL/PPO.md"); absolute OS paths only appear where noted.
package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"marginalia/internal/vault/pystr"
)

// noteSuffixes are the extensions of Markdown notes.
var noteSuffixes = map[string]bool{".md": true, ".markdown": true}

// textSuffixes are the extensions the API may read and write as text.
var textSuffixes = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".csv": true, ".json": true, ".yaml": true,
	".yml": true, ".toml": true, ".py": true, ".sh": true, ".tex": true, ".bib": true,
}

// IsNote reports whether p names a Markdown note (.md or .markdown, any case).
func IsNote(p string) bool { return noteSuffixes[pystr.Lower(Suffix(p))] }

// IsText reports whether p names a file the API treats as editable text.
func IsText(p string) bool { return textSuffixes[pystr.Lower(Suffix(p))] }

// ETag returns the version tag of a file's content: the first 20 hex digits
// of its SHA-256.
func ETag(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:20]
}

// Name is PurePosixPath(p).name: the last non-empty, non-"." component.
func Name(p string) string {
	parts := strings.Split(p, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" && parts[i] != "." {
			return parts[i]
		}
	}
	return ""
}

// Suffix is PurePosixPath(p).suffix: the final ".ext" of the name, or "" for
// names without one, dot-files and names ending in a dot.
func Suffix(p string) string {
	name := Name(p)
	i := strings.LastIndexByte(name, '.')
	if 0 < i && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// Stem is PurePosixPath(p).stem: the name without its Suffix.
func Stem(p string) string {
	name := Name(p)
	i := strings.LastIndexByte(name, '.')
	if 0 < i && i < len(name)-1 {
		return name[:i]
	}
	return name
}

// Vault is a note folder on disk. It holds no other state, so a single value
// is safe for concurrent use.
type Vault struct {
	root string // absolute, symlinks resolved
}

// New opens the vault rooted at dir. The directory does not need to exist yet
// (Python's Path.resolve was non-strict too); symlinks in dir are resolved so
// that containment checks compare real paths.
func New(dir string) (*Vault, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("vault: no notes directory configured")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	return &Vault{root: realpath(abs)}, nil
}

// Root returns the absolute, symlink-free path of the vault folder.
func (v *Vault) Root() string { return v.root }

// Clean normalises a client-supplied relative path, rejecting anything unsafe:
// backslashes become slashes, surrounding whitespace and slashes are trimmed,
// and empty, ".", ".." or dot-prefixed components, NUL bytes and paths longer
// than 1024 characters are refused with Invalid("invalid path").
func (v *Vault) Clean(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, `\`, "/")
	rel = strings.Trim(pystr.Strip(rel), "/")
	if rel == "" || strings.ContainsRune(rel, 0) || utf8.RuneCountInString(rel) > 1024 {
		return "", Invalid("invalid path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", Invalid("invalid path")
		}
	}
	return rel, nil
}

// Resolve cleans rel and returns the absolute path it designates, following
// symlinks. Paths that end up outside the vault are refused with
// Invalid("path escapes the vault"), and paths that a symlink leads into a
// hidden entry (Inbox/g -> ../.git) with Invalid("invalid path"), exactly as
// if the hidden path had been named directly. The target does not need to
// exist.
func (v *Vault) Resolve(rel string) (string, error) {
	cleaned, err := v.Clean(rel)
	if err != nil {
		return "", err
	}
	return v.confine(realpath(filepath.Join(v.root, filepath.FromSlash(cleaned))))
}

// resolveExact resolves a path exactly as Files lists it: unlike Resolve it
// does not trim whitespace or rewrite backslashes (both are legal in file
// names), but it still refuses absolute paths, ".", "..", empty and hidden
// components, NUL bytes and anything that resolves outside the vault.
func (v *Vault) resolveExact(rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", Invalid("invalid path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", Invalid("invalid path")
		}
	}
	return v.confine(realpath(filepath.Join(v.root, filepath.FromSlash(rel))))
}

// confine checks a symlink-resolved target: it must lie inside the vault and
// must not be, or be inside, a hidden entry. Checking the client's path text
// alone is not enough, because a symlink planted in the vault (for example
// one pulled in through git sync) can lead into .git, whose config and hooks
// git executes.
func (v *Vault) confine(target string) (string, error) {
	if !v.contains(target) {
		return "", Invalid("path escapes the vault")
	}
	if v.hiddenBelow(target) {
		return "", Invalid("invalid path")
	}
	return target, nil
}

// hiddenBelow reports whether abs, a path inside the vault, has a
// dot-prefixed component relative to the vault root.
func (v *Vault) hiddenBelow(abs string) bool {
	rel, err := filepath.Rel(v.root, abs)
	if err != nil || rel == "." {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if hidden(part) {
			return true
		}
	}
	return false
}

// contains reports whether abs is the vault root or inside it.
func (v *Vault) contains(abs string) bool {
	if abs == v.root {
		return true
	}
	prefix := v.root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(abs, prefix)
}

// Rel converts an absolute path inside the vault back to its vault-relative,
// slash-separated form (symlinks resolved). The root itself is ".".
func (v *Vault) Rel(abs string) (string, error) {
	resolved := realpath(abs)
	if !v.contains(resolved) {
		return "", Invalid("path escapes the vault")
	}
	rel, err := filepath.Rel(v.root, resolved)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// mustRel is Rel for paths already known to be inside the vault.
func (v *Vault) mustRel(abs string) string {
	rel, err := v.Rel(abs)
	if err != nil {
		return filepath.ToSlash(strings.TrimPrefix(abs, v.root+string(filepath.Separator)))
	}
	return rel
}

// realpath is Python's os.path.realpath(path, strict=False) for an absolute
// path: symlinks are resolved component by component, missing components are
// appended unchanged, and a symlink loop stops resolution at the loop.
func realpath(p string) string {
	resolved, _ := joinRealpath(string(filepath.Separator), p, map[string]*string{})
	return resolved
}

func joinRealpath(base, rest string, seen map[string]*string) (string, bool) {
	sep := string(filepath.Separator)
	if filepath.IsAbs(rest) {
		base = sep
		rest = strings.TrimLeft(rest, sep)
	}
	for rest != "" {
		var name string
		name, rest, _ = strings.Cut(rest, sep)
		switch name {
		case "", ".":
			continue
		case "..":
			if base != sep {
				base = filepath.Dir(base)
			}
			continue
		}
		next := filepath.Join(base, name)
		info, err := os.Lstat(next)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			base = next
			continue
		}
		if done, ok := seen[next]; ok {
			if done != nil {
				base = *done
				continue
			}
			// A symlink loop: return what was resolved plus the rest verbatim.
			return filepath.Join(next, rest), false
		}
		seen[next] = nil
		target, err := os.Readlink(next)
		if err != nil {
			base = next
			continue
		}
		var ok bool
		base, ok = joinRealpath(base, target, seen)
		if !ok {
			return filepath.Join(base, rest), false
		}
		resolved := base
		seen[next] = &resolved
	}
	return base, true
}

// atomicWrite replaces path with data without ever exposing a partial file:
// the bytes go to a hidden temporary file in the same directory, are fsynced,
// get the previous file's permissions (0644 for new files) and are renamed
// into place. Missing parent directories are created.
func atomicWrite(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// isFile reports whether p exists and is a regular file (following symlinks).
func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// exists reports whether p exists (following symlinks).
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
