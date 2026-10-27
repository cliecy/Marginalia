package vault

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"marginalia/internal/vault/pystr"
)

// Note is a text file as returned by ReadNote and GET /api/note.
type Note struct {
	Path    string  `json:"path"`    // vault-relative path (symlinks resolved)
	Content string  `json:"content"` // UTF-8 text; invalid bytes become U+FFFD, line endings untouched
	ETag    string  `json:"etag"`    // see ETag
	Mtime   float64 `json:"mtime"`   // seconds since the Unix epoch
}

// WriteResult describes a saved note ({path, etag, mtime}) or a created
// folder ({path} only).
type WriteResult struct {
	Path  string  `json:"path"`
	ETag  string  `json:"etag,omitempty"`
	Mtime float64 `json:"mtime,omitempty"`
}

// ReadBytes returns the absolute path and raw content of a vault file.
// Missing files and folders yield NotFound("file not found").
func (v *Vault) ReadBytes(rel string) (string, []byte, error) {
	p, err := v.Resolve(rel)
	if err != nil {
		return "", nil, err
	}
	if !isFile(p) {
		return "", nil, NotFound("file not found")
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, NotFound("file not found")
	}
	if err != nil {
		return "", nil, err
	}
	return p, data, nil
}

// ReadNote reads a text file (see IsText); other files are refused with
// Invalid("not a text file").
func (v *Vault) ReadNote(rel string) (*Note, error) {
	p, data, err := v.ReadBytes(rel)
	if err != nil {
		return nil, err
	}
	if !IsText(filepath.Base(p)) {
		return nil, Invalid("not a text file")
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	return &Note{Path: v.mustRel(p), Content: pystr.Decode(data), ETag: ETag(data), Mtime: mtimeOf(info)}, nil
}

// ReadText reads a vault file the way the Python backend's iteration code
// did (Path.read_text): invalid UTF-8 becomes U+FFFD and "\r\n"/"\r" become
// "\n". rel must be an exact path as returned by Files or Index (no
// normalisation is applied, so names with surrounding spaces work); it is
// still validated and confined to the vault. Use ReadNote for paths that come
// from clients.
func (v *Vault) ReadText(rel string) (string, error) {
	p, err := v.resolveExact(rel)
	if err != nil {
		return "", err
	}
	if !isFile(p) {
		return "", NotFound("file not found")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return pystr.ReadText(data), nil
}

// WriteNote saves content to a text file atomically.
//
// baseETag implements optimistic locking: nil overwrites unconditionally; a
// value must equal the file's current ETag, otherwise the write fails with
// Conflict("the note changed on disk since it was opened"). A non-empty
// baseETag for a file that no longer exists fails with Conflict("the note was
// deleted or moved since it was opened"); an empty one creates the file.
func (v *Vault) WriteNote(rel, content string, baseETag *string) (*WriteResult, error) {
	p, err := v.Resolve(rel)
	if err != nil {
		return nil, err
	}
	if !IsText(filepath.Base(p)) {
		return nil, Invalid("only text files can be edited")
	}
	if exists(p) {
		if !isFile(p) {
			return nil, Invalid("not a file")
		}
		current, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if baseETag != nil && *baseETag != ETag(current) {
			return nil, Conflict("the note changed on disk since it was opened")
		}
	} else if baseETag != nil && *baseETag != "" {
		return nil, Conflict("the note was deleted or moved since it was opened")
	}
	data := []byte(content)
	if err := atomicWrite(p, data); err != nil {
		return nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	return &WriteResult{Path: v.mustRel(p), ETag: ETag(data), Mtime: mtimeOf(info)}, nil
}

// Create makes a new note (with content) or, with folder, a new folder
// (including missing parents). Existing paths fail with Conflict("a file or
// folder with that name already exists"); notes must have a text extension
// (Invalid("new notes must end in .md")).
func (v *Vault) Create(rel, content string, folder bool) (*WriteResult, error) {
	p, err := v.Resolve(rel)
	if err != nil {
		return nil, err
	}
	if exists(p) {
		return nil, Conflict("a file or folder with that name already exists")
	}
	if folder {
		if err := os.MkdirAll(p, 0o777); err != nil {
			return nil, err
		}
		return &WriteResult{Path: v.mustRel(p)}, nil
	}
	if !IsText(filepath.Base(p)) {
		return nil, Invalid("new notes must end in .md")
	}
	return v.WriteNote(rel, content, nil)
}

// finderJunk lists metadata files macOS Finder drops into folders. They are
// hidden from the tree, so a folder holding only these counts as empty.
var finderJunk = map[string]bool{".DS_Store": true}

// Delete removes a file or an empty folder. Folders that still contain
// anything are refused with Invalid("only empty folders can be deleted");
// missing paths yield NotFound("file not found") and the root itself
// Invalid("cannot delete the vault root").
//
// Deliberate difference from the Python backend: a folder whose only content
// is Finder's .DS_Store counts as empty, because the app never shows it.
func (v *Vault) Delete(rel string) error {
	p, err := v.Resolve(rel)
	if err != nil {
		return err
	}
	if p == v.root {
		return Invalid("cannot delete the vault root")
	}
	info, err := os.Stat(p)
	switch {
	case err == nil && info.IsDir():
		entries, err := os.ReadDir(p)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !finderJunk[entry.Name()] {
				return Invalid("only empty folders can be deleted")
			}
		}
		for _, entry := range entries {
			if err := os.Remove(filepath.Join(p, entry.Name())); err != nil {
				return err
			}
		}
		return os.Remove(p)
	case err == nil && info.Mode().IsRegular():
		return os.Remove(p)
	default:
		return NotFound("file not found")
	}
}

var (
	uploadStemUnsafe   = regexp.MustCompile(`[\x00-\x1f/\\:*?"<>|]`)
	uploadSuffixUnsafe = regexp.MustCompile(`[^A-Za-z0-9.]`)
)

// Upload stores data under folder (created if needed) using a sanitised form
// of the client's filename, appending "-<6 hex>" to the stem until the name is
// free. It returns the new file's vault-relative path.
func (v *Vault) Upload(folder, filename string, data []byte) (string, error) {
	base := Name(strings.ReplaceAll(filename, `\`, "/"))
	stem, suffix := splitExt(base)
	stem = uploadStemUnsafe.ReplaceAllString(stem, "_")
	stem = strings.Trim(stem, " .")
	if stem == "" {
		stem = "file"
	}
	suffix = uploadSuffixUnsafe.ReplaceAllString(suffix, "")
	if len(suffix) > 12 {
		suffix = suffix[:12] // ASCII only after the replacement above
	}
	suffix = strings.ToLower(suffix)
	dir, err := v.Resolve(folder)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	candidate := filepath.Join(dir, stem+suffix)
	for exists(candidate) {
		token := make([]byte, 3)
		if _, err := rand.Read(token); err != nil {
			return "", err
		}
		candidate = filepath.Join(dir, stem+"-"+hex.EncodeToString(token)+suffix)
	}
	if err := atomicWrite(candidate, data); err != nil {
		return "", err
	}
	return v.mustRel(candidate), nil
}

// splitExt is posixpath.splitext for a bare file name: the extension starts
// at the last dot unless every character before it is a dot.
func splitExt(name string) (string, string) {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 {
		return name, ""
	}
	if strings.Trim(name[:dot], ".") == "" {
		return name, ""
	}
	return name[:dot], name[dot:]
}
