package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo"

	"marginalia/internal/server"
)

// AppInfo describes the running app.
type AppInfo struct {
	// Version is the app version, e.g. "0.2.0".
	Version string `json:"version"`
	// OS is runtime.GOOS: "darwin", "windows" or "linux".
	OS string `json:"os"`
	// Arch is runtime.GOARCH, e.g. "arm64".
	Arch string `json:"arch"`
	// DataDir holds settings.json and the secrets.
	DataDir string `json:"dataDir"`
	// APIURL is the base URL of the local API listener while it runs, else "".
	APIURL string `json:"apiURL"`
}

// Native gives the page what only the desktop can do: native dialogs and
// the file manager. It is bound for the app's own pages only (MyGo refuses
// calls from other origins), and every path it receives is checked.
type Native struct {
	srv     *server.Server
	version string
	// shell and pickFolders are the system integrations, replaced in tests.
	shell       shell
	pickFolders func(mygo.OpenDialogOptions) ([]string, error)
	// flushed receives the page's answers to FlushEvent (see Flushed).
	flushed chan struct{}
}

// shell is the part of mygo.Shell that Native uses.
type shell interface {
	OpenExternal(url string) error
	OpenPath(path string) error
	ShowItemInFolder(path string)
}

// newNative returns the service for srv. srv may be nil while the client
// is generated (`mygo generate`), when no method is called.
func newNative(srv *server.Server, version string) *Native {
	return &Native{srv: srv, version: version, shell: mygo.Shell, pickFolders: mygo.Dialog.Open, flushed: make(chan struct{}, 1)}
}

// Flushed tells the app that the page has saved its unsaved edits, in
// answer to a FlushEvent. The app waits for it before it closes the window
// or quits.
func (n *Native) Flushed() {
	select {
	case n.flushed <- struct{}{}:
	default: // an answer is already waiting
	}
}

// awaitFlush drops answers left from earlier requests, calls ask (which
// sends the FlushEvent) and waits for the page's Flushed or for ctx to end,
// calling tick between checks. tick must wait a little; on the main thread
// it must keep the event loop running meanwhile, or the page could never
// answer. An error from tick ends the wait.
func (n *Native) awaitFlush(ctx context.Context, ask, tick func() error) error {
	select {
	case <-n.flushed:
	default:
	}
	if err := ask(); err != nil {
		return err
	}
	for {
		select {
		case <-n.flushed:
			return nil
		default:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tick(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

// PickFolder shows the system folder picker; "" when cancelled.
func (n *Native) PickFolder(ctx context.Context, title string) (string, error) {
	opts := mygo.OpenDialogOptions{
		Parent:            mygo.CallerWindow(ctx),
		Title:             title,
		Message:           title,
		ButtonLabel:       "选择",
		Directory:         true,
		CreateDirectories: true,
	}
	if dir := n.srv.Core().Settings().NotesDir; dir != "" {
		opts.DefaultPath = filepath.Dir(dir)
	}
	paths, err := n.pickFolders(opts)
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// Reveal shows a vault path ("" = the vault root) in Finder / Explorer.
func (n *Native) Reveal(path string) error {
	v := n.srv.Core().Vault()
	if v == nil {
		return errors.New("还没有打开笔记库")
	}
	if strings.TrimSpace(path) == "" {
		return n.shell.OpenPath(v.Root())
	}
	abs, err := v.Resolve(path)
	if err != nil {
		return fmt.Errorf("无法显示 %s：路径无效", path)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("找不到 %s", path)
	}
	n.shell.ShowItemInFolder(abs)
	return nil
}

// RevealDataDir shows the app's data folder.
func (n *Native) RevealDataDir() error {
	dir := n.srv.Core().Settings().DataDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return n.shell.OpenPath(dir)
}

// OpenExternal opens an http(s) or mailto URL in the default app.
func (n *Native) OpenExternal(url string) error {
	target, err := externalURL(url)
	if err != nil {
		return err
	}
	return n.shell.OpenExternal(target)
}

// Info describes the running app.
func (n *Native) Info() AppInfo {
	info := AppInfo{
		Version: n.version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		DataDir: n.srv.Core().Settings().DataDir,
	}
	if st := n.srv.APIStatus(); st.Running {
		info.APIURL = st.URL
	}
	return info
}

// externalURL validates a URL the page wants opened outside the app: only
// http and https URLs with a host, and mailto URLs, are allowed, so a page
// cannot launch arbitrary handlers (file:, custom schemes…).
func externalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("链接格式不对：%s", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return "", fmt.Errorf("链接缺少主机名：%s", raw)
		}
	case "mailto":
		if u.Opaque == "" && u.Path == "" {
			return "", fmt.Errorf("邮件链接缺少地址：%s", raw)
		}
	default:
		return "", fmt.Errorf("只能在外部打开 http、https 或 mailto 链接：%s", raw)
	}
	return u.String(), nil
}
