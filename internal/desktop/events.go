package desktop

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"

	"marginalia/internal/vault"
)

// URLScheme is the scheme of Marginalia's deep links (urlSchemes in
// mygo.json).
const URLScheme = "marginalia"

// MenuCommand names a command of the native menu that the page carries out.
type MenuCommand string

// The menu commands (the ids src/lib/native.js documents).
const (
	CommandNewNote       MenuCommand = "new-note"
	CommandOpenVault     MenuCommand = "open-vault"
	CommandSettings      MenuCommand = "settings"
	CommandToggleSidebar MenuCommand = "toggle-sidebar"
	CommandRevealVault   MenuCommand = "reveal-vault"
	CommandSearch        MenuCommand = "search"
)

// OpenRequest is what a marginalia:// deep link asks the page to show:
// a note (Path) or a search (Q). Exactly one of them is set.
type OpenRequest struct {
	// Path is the vault-relative path of the note to open.
	Path string `json:"path,omitzero"`
	// Q is the search to run; an empty query only opens the search.
	Q *string `json:"q,omitzero"`
}

// MenuEvent carries the id of a menu command: "new-note", "open-vault",
// "settings", "toggle-sidebar", "reveal-vault", "search".
var MenuEvent = mygo.NewEvent[MenuCommand]("menu")

// OpenEvent carries a marginalia:// deep link: open a note or search.
var OpenEvent = mygo.NewEvent[OpenRequest]("open")

// FlushReason says why the app asks the page to save: "close" (the window
// closes) or "quit" (the app quits).
type FlushReason string

// The reasons of a FlushEvent.
const (
	FlushClose FlushReason = "close"
	FlushQuit  FlushReason = "quit"
)

// FlushEvent asks the page to save its unsaved edits now, because the
// window is about to close (the page gets no beforeunload). The page answers
// with Native.Flushed once they are written.
var FlushEvent = mygo.NewEvent[FlushReason]("flush")

// errNotADeepLink is returned by ParseDeepLink for URLs it does not know.
var errNotADeepLink = errors.New("not a Marginalia link")

// ParseDeepLink parses a Marginalia deep link:
//
//	marginalia://open?path=Knowledge/RL/PPO.md
//	marginalia://search?q=transformer
//
// The action may also be written as a path (marginalia:///open?…) or
// opaquely (marginalia:open?…). The note path is returned as given apart
// from surrounding whitespace and slashes; an absolute path is kept
// absolute so the caller can map it into the vault (see vaultRelative).
func ParseDeepLink(raw string) (OpenRequest, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return OpenRequest{}, fmt.Errorf("%w: %v", errNotADeepLink, err)
	}
	if !strings.EqualFold(u.Scheme, URLScheme) {
		return OpenRequest{}, fmt.Errorf("%w: scheme %q", errNotADeepLink, u.Scheme)
	}
	action := u.Host
	if action == "" {
		action = u.Opaque
		if action == "" {
			action = u.Path
		}
	}
	action = strings.ToLower(strings.Trim(action, "/"))
	query := u.Query()
	switch action {
	case "open":
		p := strings.TrimSpace(query.Get("path"))
		if filepath.IsAbs(p) {
			p = filepath.Clean(p)
			if p == filepath.Dir(p) {
				p = "" // the root of the file system is no note
			}
		} else {
			p = strings.Trim(p, "/")
		}
		if p == "" || strings.ContainsRune(p, 0) {
			return OpenRequest{}, fmt.Errorf("%w: open needs a path", errNotADeepLink)
		}
		return OpenRequest{Path: p}, nil
	case "search":
		q := strings.TrimSpace(query.Get("q"))
		return OpenRequest{Q: &q}, nil
	default:
		return OpenRequest{}, fmt.Errorf("%w: unknown action %q", errNotADeepLink, action)
	}
}

// vaultRelative turns the path of an OpenRequest into the vault-relative
// path the page expects. Relative paths are checked with v.Clean; absolute
// ones must lie inside the vault. Without a vault, relative paths pass
// through unchanged (the page then asks for a vault first).
func vaultRelative(v *vault.Vault, p string) (string, error) {
	if v == nil {
		if filepath.IsAbs(p) {
			return "", errors.New("no vault is open")
		}
		return p, nil
	}
	if filepath.IsAbs(p) {
		rel, err := v.Rel(p)
		if err != nil || rel == "." {
			return "", fmt.Errorf("%s is not a note of the vault", p)
		}
		p = rel
	}
	return v.Clean(p)
}

// deepLinkArgs returns the arguments that are Marginalia deep links.
func deepLinkArgs(args []string) []string {
	var links []string
	for _, a := range args {
		if scheme, _, ok := strings.Cut(a, ":"); ok && strings.EqualFold(scheme, URLScheme) {
			links = append(links, a)
		}
	}
	return links
}
