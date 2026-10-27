package server

import (
	"context"
	"io"
	"log/slog"
	"time"

	"marginalia/internal/config"
	"marginalia/internal/mcp"
)

// DefaultMCPRecheck is how often `marginalia mcp` serving in-process (or
// waiting for an app with the API turned off) looks for the app again.
const DefaultMCPRecheck = 2 * time.Second

// apiOffMessage answers MCP requests while the app runs with its local API
// turned off: serving them in-process would make a second writer of the
// vault and its git repository.
const apiOffMessage = "Marginalia 正在运行，但本地 API 已关闭，请在「设置 → API」中开启后重试"

// StdioOptions configures ServeMCPStdio.
type StdioOptions struct {
	// Load returns the current settings (config.Load(config.ModeDesktop)).
	// It is called again whenever the router looks for the app, so a vault
	// or port chosen in the app meanwhile is followed.
	Load func() (config.Settings, error)
	// Version is reported by the in-process MCP server.
	Version string
	// Logger receives the logs (stderr; stdout carries the protocol).
	Logger *slog.Logger
	// Recheck is how often in-process mode looks for a running app
	// (0 = DefaultMCPRecheck).
	Recheck time.Duration
}

// ServeMCPStdio is `marginalia mcp`: MCP over stdio for the vault of the
// desktop app's data folder. The app stays the only writer of the vault:
//
//   - While the app runs with its API on, each message is relayed to it
//     with the API token, after the app has proved (mcp.ProbeApp) that it
//     holds the token, so the token never goes to another program that
//     took the port. The token is read again whenever the router looks for
//     the app, so a regenerated token is picked up.
//   - Without the app, messages are served in-process against the vault of
//     the settings (pending edits are committed; periodic sync is left to
//     the app). Every Recheck the router looks for the app again and hands
//     over to it once it runs.
//   - While the app runs with its API off, requests are answered with an
//     error saying so.
//
// When the app quits mid-session, or refuses the token, the router looks
// again before giving up; a message is only re-sent when it certainly did
// not reach the app. Messages are handled one at a time, in order.
func ServeMCPStdio(ctx context.Context, in io.Reader, out io.Writer, opts StdioOptions) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Recheck <= 0 {
		opts.Recheck = DefaultMCPRecheck
	}
	r := &mcpRouter{opts: opts, log: opts.Logger}
	defer r.close()
	return mcp.RunStdioFunc(ctx, in, out, r.handle)
}

// routeKind is where mcpRouter sends messages.
type routeKind int

const (
	routeNone  routeKind = iota // not decided yet
	routeApp                    // relayed to the running app
	routeLocal                  // served in-process
	routeOff                    // the app runs with its API off: refused
)

// mcpRouter sends each stdio message to the app or serves it in-process.
// It is used by one goroutine at a time (RunStdioFunc handles messages in
// order), so it needs no lock.
type mcpRouter struct {
	opts StdioOptions
	log  *slog.Logger

	kind    routeKind
	checked time.Time      // when resolve last ran
	appURL  string         // routeApp: the app's base URL
	fwd     *mcp.Forwarder // routeApp
	core    *Core          // routeLocal, built on first use
	foreign string         // the last URL reported as another Marginalia
}

// handle answers one message.
func (r *mcpRouter) handle(ctx context.Context, msg []byte) ([]byte, bool) {
	if r.kind == routeNone || (r.kind != routeApp && time.Since(r.checked) >= r.opts.Recheck) {
		r.resolve(ctx)
	}
	if r.kind == routeApp {
		reply, ok, failure := r.fwd.Forward(ctx, msg)
		if failure != mcp.Unreachable && failure != mcp.Unauthorized {
			return reply, ok // delivered, or it may have been: never send twice
		}
		// The app quit, or refused the token (it was regenerated, or
		// another server took the port): look again.
		r.log.Info("the app did not take the message; looking for it again", "url", r.appURL)
		r.resolve(ctx)
		if r.kind == routeApp {
			reply, ok, _ = r.fwd.Forward(ctx, msg)
			return reply, ok
		}
	}
	if r.kind == routeOff {
		return mcp.ErrorReplies(msg, apiOffMessage)
	}
	core, err := r.localCore()
	if err != nil {
		return mcp.ErrorReplies(msg, "Marginalia: "+err.Error())
	}
	return core.MCP().Handle(msg)
}

// resolve decides where messages go now and switches to it.
func (r *mcpRouter) resolve(ctx context.Context) {
	r.checked = time.Now()
	settings, err := r.opts.Load()
	if err != nil {
		r.log.Warn("cannot read the settings", "err", err)
		if r.kind == routeNone {
			r.switchTo(routeLocal, "", "")
		}
		return
	}
	app := FindLocalApp(settings)
	if app.URL != "" {
		token, err := config.ReadOptional(settings.MCPTokenFile())
		if err != nil {
			r.log.Warn("cannot read the API token", "err", err)
		}
		switch mcp.ProbeApp(ctx, app.URL, token) {
		case mcp.AppUp:
			r.switchTo(routeApp, app.URL, token)
			return
		case mcp.AppForeign:
			if r.foreign != app.URL {
				r.foreign = app.URL
				r.log.Warn("another Marginalia (one that does not hold this data folder's API token) answers there; not relaying to it", "url", app.URL)
			}
		}
	}
	if app.Running && app.URL == "" {
		r.switchTo(routeOff, "", "")
		return
	}
	r.switchTo(routeLocal, "", "")
}

// switchTo makes kind the route, closing what the previous one held.
func (r *mcpRouter) switchTo(kind routeKind, url, token string) {
	if kind != routeLocal && r.core != nil {
		r.core.Close() // commits pending edits before the app takes over
		r.core = nil
	}
	if r.fwd != nil {
		r.fwd.Close()
		r.fwd = nil
	}
	if kind == routeApp {
		r.fwd = mcp.NewForwarder(url+"/mcp", token)
	}
	if kind != r.kind || url != r.appURL {
		switch kind {
		case routeApp:
			r.log.Info("relaying MCP to the running app", "url", url+"/mcp")
		case routeOff:
			r.log.Warn("Marginalia is running with its local API turned off; MCP requests fail until it is turned on")
		}
	}
	r.kind, r.appURL = kind, url
}

// localCore returns the in-process core, building it on first use from the
// current settings.
func (r *mcpRouter) localCore() (*Core, error) {
	if r.core != nil {
		return r.core, nil
	}
	settings, err := r.opts.Load()
	if err != nil {
		return nil, err
	}
	// Edits are committed as the app would, but periodic git sync
	// (pull/push) is left to the app.
	settings.Git.AutosyncMinutes = 0
	core, err := NewCore(settings, CoreOptions{Mode: config.ModeDesktop, Version: r.opts.Version, Logger: r.log})
	if err != nil {
		return nil, err
	}
	if v := core.Vault(); v != nil {
		r.log.Info("serving MCP in-process", "vault", v.Root())
	} else {
		r.log.Warn("no vault is configured: choose one in the Marginalia app, or set NOTES_DIR")
	}
	r.core = core
	return core, nil
}

// close releases the route at the end of the session.
func (r *mcpRouter) close() {
	if r.core != nil {
		r.core.Close() // commits pending edits
		r.core = nil
	}
	if r.fwd != nil {
		r.fwd.Close()
		r.fwd = nil
	}
}
