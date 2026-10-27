// Package desktop is the MyGo shell of Marginalia: the desktop app's
// window, menus, deep links and the native services its page calls.
//
// The whole UI is the web frontend, served together with the API by one
// http.Handler (server.Server.AppHandler) on the app's own mygo:// scheme,
// so the page and the API share an origin and need no port or login. The
// same server also runs the optional loopback listener (NetHandler) through
// which other programs and `marginalia mcp` reach the vault.
//
// Run is the entry point. It binds the Native service and declares the
// typed events (MenuEvent, OpenEvent) from which `go tool mygo generate`
// writes frontend/src/lib/mygo.ts, the client src/lib/native.js loads.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"marginalia/internal/config"
	"marginalia/internal/server"
)

// Window geometry and colours. backgroundColor is the page's own --bg
// (frontend/src/styles/tokens.css), which the window shows until the page
// paints; with the theme forced through mygo.Theme, light-dark() follows it.
const (
	windowWidth     = 1280
	windowHeight    = 820
	windowMinWidth  = 960
	windowMinHeight = 600
	backgroundColor = "light-dark(#f4f4f6, #1a1a1e)"
	windowStateKey  = "main"
)

// shutdownTimeout bounds the drain of the API listener when the app quits.
const shutdownTimeout = 5 * time.Second

// flushTimeout bounds how long closing the window or quitting waits for the
// page to save its unsaved edits.
const flushTimeout = 3 * time.Second

// Title bar geometry (Magpie's 46 px top bar). On macOS the window controls
// sit trafficLightInset from the window's top-left corner, which makes MyGo
// report a 46 px title bar (14 px buttons + 2 × 16); titleBarHeight is the
// same height on Windows and Linux. (TitleBarHiddenInset would add a
// toolbar and make the title bar taller.)
const (
	titleBarHeight    = 46
	trafficLightInset = 16
)

// Options configures Run.
type Options struct {
	// Name is the app's name (mygo.json "name"). Packaged apps take theirs
	// from the bundle; plain `go build` binaries are given this one.
	Name string
	// Version is the app version, reported by Native.Info, the API and the
	// MCP server.
	Version string
	// Logger receives the app's logs. nil = stderr, plus a log file in the
	// system's log folder for packaged apps, whose stderr nobody reads.
	Logger *slog.Logger
}

// Run runs the desktop app until it quits: one instance per user, the main
// window on the frontend, the loopback API when the settings enable it.
// It must be called from the main goroutine (MyGo's event loop runs there).
//
// A second launch hands its command line (deep links included) to the
// running instance and returns nil. When the app cannot start, for example
// because settings.json is malformed, Run shows the error in a dialog and
// returns it.
func Run(opts Options) error {
	if opts.Name == "" {
		opts.Name = "Marginalia"
	}
	native := newNative(nil, opts.Version)
	mygo.Bind(native)
	if os.Getenv("MYGO_GENERATE") != "" {
		// `mygo generate`: App.Run writes the TypeScript client of the
		// bindings and events and returns. Nothing else may happen: no
		// settings, no secrets, no listener.
		return mygo.App.Run()
	}

	if !mygo.App.IsPackaged() {
		mygo.App.SetName(opts.Name)
		mygo.App.SetVersion(opts.Version)
	}
	log, closeLog := opts.logger()
	defer closeLog()
	slog.SetDefault(log)

	if !mygo.App.RequestSingleInstanceLock() {
		log.Info("Marginalia is already running; handed the launch over to it")
		return nil
	}

	a, err := newApp(opts, native, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		return showFatal(err)
	}
	if err := a.install(); err != nil {
		_ = a.shutdown()
		return showFatal(err)
	}
	runErr := mygo.App.Run()
	return errors.Join(runErr, a.shutdown())
}

// app is the running desktop app.
type app struct {
	name    string
	log     *slog.Logger
	core    *server.Core
	srv     *server.Server
	native  *Native
	origins []string // origins of the app's own pages

	mu      sync.Mutex
	win     *mygo.Window  // the main window; nil while closed
	pending []OpenRequest // deep links that arrived before the app was ready

	// quitSaving is set while the page saves before a quit, quitSaved once
	// it is done (or gave up), so the repeated quit goes through.
	quitSaving, quitSaved atomic.Bool
}

// newApp loads the settings and builds the core and the server.
func newApp(opts Options, native *Native, log *slog.Logger) (*app, error) {
	settings, err := config.Load(config.ModeDesktop)
	if err != nil {
		return nil, fmt.Errorf("无法读取设置：%w", err)
	}
	core, err := server.NewCore(settings, server.CoreOptions{
		Mode:    config.ModeDesktop,
		Version: opts.Version,
		Logger:  log,
	})
	if err != nil {
		return nil, fmt.Errorf("无法打开数据文件夹 %s：%w", settings.DataDir, err)
	}
	origins := server.AppOrigins(devURL())
	srv := server.New(core, server.Options{AppOrigins: origins, Logger: log})
	native.srv = srv
	return &app{name: opts.Name, log: log, core: core, srv: srv, native: native, origins: origins}, nil
}

// devURL is the dev server `mygo dev` points the app at ("" otherwise),
// whose origin the page then has.
func devURL() string {
	if !mygo.IsDev() {
		return ""
	}
	return os.Getenv("MYGO_DEV_URL")
}

// install sets the app up before MyGo's event loop starts.
func (a *app) install() error {
	// The page and the API on the app's own scheme, before any window
	// exists; this replaces MyGo's built-in frontend server.
	if err := mygo.Protocol.Handle("mygo", a.srv.AppHandler()); err != nil {
		return err
	}

	a.applyTheme()
	a.core.OnChange(func(prev, next config.Settings) {
		if prev.Appearance.Theme != next.Appearance.Theme {
			a.applyTheme()
		}
	})

	mygo.App.SetMenu(mygo.NewMenu(a.menuTemplate()))
	mygo.App.OnOpenURL(a.openURL)
	mygo.App.OnSecondInstance(a.secondInstance)
	mygo.App.OnWindowAllClosed(func() {
		// macOS apps keep running without windows; the Dock icon
		// reopens the window (OnActivate).
		if runtime.GOOS != "darwin" {
			mygo.App.Quit()
		}
	})
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			a.showMainWindow()
		}
	})
	mygo.App.OnBeforeQuit(a.beforeQuit)
	mygo.App.WhenReady(a.ready)

	// The loopback API for other programs. A failure (no free port) is
	// logged and shown in the settings page; the app works without it.
	_ = a.srv.StartAPI()
	return nil
}

// ready opens the main window once MyGo has launched.
func (a *app) ready() {
	a.showMainWindow()
	if !mygo.App.IsPackaged() {
		// A plain `go build` binary declares no URL scheme, so MyGo does
		// not pick deep links out of the command line: do it here.
		for _, link := range deepLinkArgs(os.Args[1:]) {
			a.openURL(link)
		}
	}
	a.mu.Lock()
	pending := a.pending
	a.pending = nil
	a.mu.Unlock()
	for _, req := range pending {
		a.deliver(req)
	}
}

// shutdown stops the API listener and closes the core, which commits the
// pending git edits. It runs once the event loop has stopped.
func (a *app) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return a.srv.Shutdown(ctx)
}

// applyTheme makes the windows follow Settings.Appearance.Theme.
func (a *app) applyTheme() {
	mygo.Theme.SetSource(themeSource(a.core.Settings().Appearance.Theme))
}

// themeSource maps a settings theme to MyGo's appearance.
func themeSource(theme string) mygo.ThemeSource {
	switch theme {
	case config.ThemeLight:
		return mygo.ThemeLight
	case config.ThemeDark:
		return mygo.ThemeDark
	default:
		return mygo.ThemeSystem
	}
}

// ------------------------------------------------------------ the window

// mainWindow returns the main window, or nil while it is closed.
func (a *app) mainWindow() *mygo.Window {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.win != nil && a.win.IsDestroyed() {
		a.win = nil
	}
	return a.win
}

// showMainWindow brings the main window to the front, opening it when it
// is closed, and returns it. It runs on the main thread.
func (a *app) showMainWindow() *mygo.Window {
	win := a.mainWindow()
	if win == nil {
		win = a.openMainWindow()
	}
	if win.IsMinimized() {
		win.Restore()
	}
	win.Show()
	win.Focus()
	mygo.App.Focus()
	return win
}

// openMainWindow creates the main window on the frontend.
func (a *app) openMainWindow() *mygo.Window {
	win := mygo.NewWindow(mainWindowOptions(a.name))
	win.Page().OnWillNavigate(a.guardNavigation)
	// The page autosaves a moment after typing stops, and a closing window
	// gives it no beforeunload: have it save first, then close for real.
	var closing atomic.Int32 // 0 open, 1 saving, 2 saved
	win.OnClose(func(e *mygo.CloseEvent) {
		if closing.Load() == 2 || a.quitSaved.Load() {
			return
		}
		e.PreventDefault()
		if closing.CompareAndSwap(0, 1) {
			go func() {
				a.flushPage(win, FlushClose)
				closing.Store(2)
				win.Close()
			}()
		}
	})
	win.OnClosed(func() {
		a.mu.Lock()
		if a.win == win {
			a.win = nil
		}
		a.mu.Unlock()
	})
	a.mu.Lock()
	a.win = win
	a.mu.Unlock()
	return win
}

// mainWindowOptions are the options of the main window.
func mainWindowOptions(title string) mygo.WindowOptions {
	return mygo.WindowOptions{
		Title:                title,
		URL:                  "/",
		Width:                windowWidth,
		Height:               windowHeight,
		MinWidth:             windowMinWidth,
		MinHeight:            windowMinHeight,
		TitleBarStyle:        mygo.TitleBarHidden,
		TrafficLightPosition: &mygo.Point{X: trafficLightInset, Y: trafficLightInset},
		TitleBarHeight:       titleBarHeight,
		BackgroundColor:      backgroundColor,
		StateKey:             windowStateKey,
	}
}

// beforeQuit has the page save its unsaved edits before the app quits. It
// runs on the main thread and waits right there (flushPage keeps the event
// loop running), instead of cancelling the quit and quitting again later:
// a cancelled quit would make a system logout or shutdown fail.
func (a *app) beforeQuit(e *mygo.QuitEvent) {
	if a.quitSaved.Load() {
		return
	}
	win := a.mainWindow()
	if win == nil {
		return // the window saved when it closed
	}
	if !a.quitSaving.CompareAndSwap(false, true) {
		e.PreventDefault() // a second quit while the first one waits for the page
		return
	}
	defer a.quitSaving.Store(false)
	a.flushPage(win, FlushQuit)
	a.quitSaved.Store(true)
}

// Scripts flushPage evaluates in the page.
const (
	// flushHookScript calls the page's direct save hook, when it offers one
	// instead of answering FlushEvent; Eval awaits the promise it returns.
	flushHookScript = `typeof window.__marginaliaFlush === "function" ? Promise.resolve(window.__marginaliaFlush()).then(() => true, () => true) : false`
	// flushTickScript waits a moment; Eval keeps the event loop running
	// while it waits on the main thread.
	flushTickScript = `new Promise((resolve) => setTimeout(resolve, 15))`
)

// flushPage has the page of win save its unsaved edits and waits until it
// has, at most flushTimeout. It may run on any goroutine: on the main thread
// it waits through Eval, which keeps processing events, so the page can
// save (its requests are served through the main thread) and answer.
func (a *app) flushPage(win *mygo.Window, reason FlushReason) {
	if a.core.Vault() == nil {
		return // without a vault there is nothing to save
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	page := win.Page()
	if done, err := page.EvalContext(ctx, flushHookScript); err == nil && done == true {
		return
	}
	err := a.native.awaitFlush(ctx,
		func() error { return FlushEvent.Emit(win, reason) },
		func() error { _, err := page.EvalContext(ctx, flushTickScript); return err })
	if err != nil {
		a.log.Warn("could not have the page save before closing", "reason", reason, "err", err)
	}
}

// guardNavigation keeps the window on the app: a link to another site
// opens in the default browser instead of replacing the app.
func (a *app) guardNavigation(e *mygo.NavigateEvent) {
	if a.isAppURL(e.URL) {
		return
	}
	e.PreventDefault()
	if !e.UserInitiated {
		a.log.Debug("blocked a navigation away from the app", "url", e.URL)
		return
	}
	if target, err := externalURL(e.URL); err == nil {
		go func() {
			if err := a.native.shell.OpenExternal(target); err != nil {
				a.log.Warn("cannot open a link", "url", target, "err", err)
			}
		}()
	}
}

// isAppURL reports whether raw is a page of the app itself.
func (a *app) isAppURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "about" {
		return true // about:blank, about:srcdoc
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	return slices.ContainsFunc(a.origins, func(o string) bool { return strings.EqualFold(o, origin) })
}

// ------------------------------------------------------------ menu, deep links

// command shows the main window and has the page carry out a menu command.
func (a *app) command(c MenuCommand) {
	win := a.showMainWindow()
	if err := MenuEvent.Emit(win, c); err != nil {
		a.log.Warn("cannot send a menu command to the page", "command", c, "err", err)
	}
}

// openURL handles a marginalia:// deep link.
func (a *app) openURL(raw string) {
	req, err := ParseDeepLink(raw)
	if err != nil {
		a.log.Warn("ignored a link", "url", raw, "err", err)
		return
	}
	if req.Path != "" {
		if req.Path, err = vaultRelative(a.core.Vault(), req.Path); err != nil {
			a.log.Warn("ignored a link", "url", raw, "err", err)
			return
		}
	}
	if !mygo.App.IsReady() {
		a.mu.Lock()
		a.pending = append(a.pending, req)
		a.mu.Unlock()
		return
	}
	a.deliver(req)
}

// deliver shows the main window and sends it an OpenRequest. Events sent
// before the page is ready reach it once it is.
func (a *app) deliver(req OpenRequest) {
	win := a.showMainWindow()
	if err := OpenEvent.Emit(win, req); err != nil {
		a.log.Warn("cannot send a link to the page", "err", err)
	}
}

// secondInstance runs when Marginalia is launched again: the running
// instance comes to the front. MyGo passes deep links among the arguments
// to OnOpenURL in packaged apps, which declare the scheme.
//
// MyGo accepts second launches as soon as the single-instance lock is held,
// which can be before the app is ready, when no window may be created yet:
// such a launch is handled once the app is ready (ready opens the window).
func (a *app) secondInstance(args []string, wd string) {
	if !mygo.App.IsReady() {
		mygo.App.WhenReady(func() { a.secondInstance(args, wd) })
		return
	}
	a.showMainWindow()
	if !mygo.App.IsPackaged() {
		for _, link := range deepLinkArgs(args) {
			a.openURL(link)
		}
	}
}

// revealDataDir shows the data folder (Help menu).
func (a *app) revealDataDir() {
	if err := a.native.RevealDataDir(); err != nil {
		mygo.Dialog.Error("无法显示数据文件夹", err.Error())
	}
}

// revealLogs shows the log folder (Help menu).
func (a *app) revealLogs() {
	dir, err := mygo.App.Path(mygo.PathLogs)
	if err == nil {
		err = a.native.shell.OpenPath(dir)
	}
	if err != nil {
		mygo.Dialog.Error("无法显示日志文件夹", err.Error())
	}
}

// ------------------------------------------------------------ errors and logs

// showFatal shows why the app cannot start, then quits, and returns err.
func showFatal(err error) error {
	mygo.App.WhenReady(func() {
		go func() {
			mygo.Dialog.Error("Marginalia 无法启动", err.Error())
			mygo.App.Quit()
		}()
	})
	return errors.Join(err, mygo.App.Run())
}

// logFileName is the log of packaged apps, in mygo.PathLogs.
const logFileName = "marginalia.log"

// maxLogBytes is the size at which the log file starts over (the previous
// one is kept as marginalia.log.1).
const maxLogBytes = 4 << 20

// logger returns opts.Logger, or the default one, and a function that
// closes the log file.
func (opts Options) logger() (*slog.Logger, func()) {
	if opts.Logger != nil {
		return opts.Logger, func() {}
	}
	var out io.Writer = os.Stderr
	closeLog := func() {}
	if mygo.App.IsPackaged() {
		if f, err := openLogFile(); err == nil {
			out = io.MultiWriter(os.Stderr, f)
			closeLog = func() { _ = f.Close() }
		}
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo})), closeLog
}

// openLogFile opens the log file for appending, starting a new one when it
// has grown past maxLogBytes.
func openLogFile() (*os.File, error) {
	dir, err := mygo.App.Path(mygo.PathLogs)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, logFileName)
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
}
