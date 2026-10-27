package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"marginalia/internal/vault/pystr"
)

// Node is one entry of the file tree returned by Tree.
//
// Folders encode as {"type":"folder","name","path","children"} and files as
// {"type":"note"|"file","name","path","size","mtime"}, exactly like the
// Python API.
type Node struct {
	Type     string  // "folder", "note" (Markdown) or "file"
	Name     string  // base name
	Path     string  // vault-relative path
	Children []*Node // folders only; never nil for folders
	Size     int64   // files only, in bytes
	Mtime    float64 // files only, seconds since the Unix epoch
}

// MarshalJSON emits the folder or file shape described on Node.
func (n *Node) MarshalJSON() ([]byte, error) {
	if n.Type == "folder" {
		children := n.Children
		if children == nil {
			children = []*Node{}
		}
		return json.Marshal(struct {
			Type     string  `json:"type"`
			Name     string  `json:"name"`
			Path     string  `json:"path"`
			Children []*Node `json:"children"`
		}{n.Type, n.Name, n.Path, children})
	}
	return json.Marshal(struct {
		Type  string  `json:"type"`
		Name  string  `json:"name"`
		Path  string  `json:"path"`
		Size  int64   `json:"size"`
		Mtime float64 `json:"mtime"`
	}{n.Type, n.Name, n.Path, n.Size, n.Mtime})
}

// hidden reports whether a directory entry is invisible to the app.
func hidden(name string) bool { return strings.HasPrefix(name, ".") }

// mtimeOf converts a modification time to Python's float st_mtime.
func mtimeOf(info os.FileInfo) float64 {
	return float64(info.ModTime().UnixNano()) / 1e9
}

// Tree lists the vault recursively: in every folder, sub-folders first, then
// files, each group in natural order ("Note 2" before "Note 10",
// case-insensitive). Hidden entries and symlinks are omitted, and unreadable
// folders appear empty.
func (v *Vault) Tree() []*Node {
	return v.walkTree(v.root)
}

func (v *Vault) walkTree(dir string) []*Node {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []*Node{}
	}
	var folders, files []*Node
	for _, entry := range entries {
		if hidden(entry.Name()) {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		rel := v.mustRelLexical(full)
		switch mode := entry.Type(); {
		case mode.IsDir():
			folders = append(folders, &Node{Type: "folder", Name: entry.Name(), Path: rel, Children: v.walkTree(full)})
		case mode.IsRegular():
			info, err := entry.Info()
			if err != nil {
				continue
			}
			kind := "file"
			if IsNote(entry.Name()) {
				kind = "note"
			}
			files = append(files, &Node{Type: kind, Name: entry.Name(), Path: rel, Size: info.Size(), Mtime: mtimeOf(info)})
		}
	}
	byName := func(list []*Node) {
		sort.SliceStable(list, func(i, j int) bool { return pystr.NaturalLess(list[i].Name, list[j].Name) })
	}
	byName(folders)
	byName(files)
	return append(append(make([]*Node, 0, len(folders)+len(files)), folders...), files...)
}

// Files lists the vault-relative paths of every visible file in the order
// Python's os.walk produced them: a folder's files (sorted by name) before its
// sub-folders (sorted by name), recursively. Hidden files and folders are
// skipped, symlinked folders are not descended into, and with notesOnly only
// Markdown notes are returned.
//
// Symlinked files are listed when they point to a regular file inside the
// vault. Unlike the Python backend, symlinks that dangle or lead outside the
// vault (for example ones pulled in through git sync) are skipped, so their
// targets can never reach search results or AI context.
func (v *Vault) Files(notesOnly bool) []string {
	var out []string
	v.walkFiles(v.root, notesOnly, &out)
	return out
}

func (v *Vault) walkFiles(dir string, notesOnly bool, out *[]string) {
	entries, err := os.ReadDir(dir) // sorted by name, like sorted() in Python
	if err != nil {
		return
	}
	var subdirs []string
	for _, entry := range entries {
		name := entry.Name()
		if hidden(name) {
			continue
		}
		full := filepath.Join(dir, name)
		mode := entry.Type()
		switch {
		case mode.IsDir():
			subdirs = append(subdirs, full)
			continue
		case mode&os.ModeSymlink != 0:
			if !v.listableLink(full) {
				continue
			}
		case !mode.IsRegular():
			continue
		}
		if notesOnly && !IsNote(name) {
			continue
		}
		*out = append(*out, v.mustRelLexical(full))
	}
	for _, sub := range subdirs {
		v.walkFiles(sub, notesOnly, out)
	}
}

// listableLink reports whether the symlink at full resolves to a regular file
// inside the vault that is not itself hidden (a link to .git/config must not
// reach search results, the index or AI context).
func (v *Vault) listableLink(full string) bool {
	target := realpath(full)
	return v.contains(target) && !v.hiddenBelow(target) && isFile(target)
}

// mustRelLexical returns the vault-relative path of full without resolving
// symlinks (a symlinked note is listed under its own name).
func (v *Vault) mustRelLexical(full string) string {
	rel, err := filepath.Rel(v.root, full)
	if err != nil {
		return filepath.ToSlash(full)
	}
	return filepath.ToSlash(rel)
}
