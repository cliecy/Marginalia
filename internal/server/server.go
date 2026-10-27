// Package server is Marginalia's HTTP layer: the port of the Python
// backend's main.py plus the desktop extensions.
//
// One route table serves two entry points:
//
//   - AppHandler, registered for the mygo:// scheme of the desktop app. Its
//     requests come from the app's own webview, so they need no login; a
//     CSRF check on the Origin header keeps other pages out.
//   - NetHandler, served on TCP: the desktop app's loopback API and
//     `marginalia serve`. Requests authenticate with the API token
//     (Authorization: Bearer) or with the session cookie of the password
//     login, exactly like the Python server.
//
// Both serve the embedded web UI (internal/webui) with the same security
// headers as the Python backend.
package server

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"marginalia/internal/magpie"
	"marginalia/internal/mcp"
	"marginalia/internal/webui"
)

const isWindows = runtime.GOOS == "windows"

// Options configures New.
type Options struct {
	// AppOrigins are the Origin values AppHandler accepts (nil =
	// AppOrigins("")). The desktop app passes AppOrigins(devURL) with the
	// dev server URL while running under `mygo dev`.
	AppOrigins []string
	// Static is the web UI to serve. nil = Settings.StaticDir when it holds
	// an index.html, else the embedded build (webui.FS()).
	Static fs.FS
	// Magpie is the Magpie integration (nil = magpie.Default: the real
	// gateway and CLI).
	Magpie *magpie.Client
	// Executable is the command Magpie's library runs for the stdio MCP
	// server (`<Executable> mcp`); "" = os.Executable().
	Executable string
	// Logger receives the server's logs (nil = slog.Default()).
	Logger *slog.Logger
}

// Server serves the Marginalia API and web UI for a Core. Create it with
// New; it is safe for concurrent use.
type Server struct {
	core       *Core
	log        *slog.Logger
	appOrigins []string
	static     fs.FS // nil when no frontend build is available
	magpie     *magpie.Client
	executable string
	limiter    *loginLimiter
	now        func() time.Time

	mux    *http.ServeMux
	routes []route
	app    http.Handler
	net    http.Handler

	api          apiListener
	registration registrationCache
}

// access is the authentication a route requires.
type access int

const (
	accessPublic access = iota // anyone (static files, health, login)
	accessUser                 // app, session or token
	accessApp                  // app or session — never the API token
)

// route is one entry of the route table.
type route struct {
	method string
	path   string
	access access
	vault  bool // needs a vault (409 no_vault otherwise)
	// documented routes are part of the public REST API and must appear in
	// openapi.json.
	documented bool
	handler    http.Handler
}

// New returns the server for core.
func New(core *Core, opts Options) *Server {
	s := &Server{
		core:       core,
		log:        opts.Logger,
		appOrigins: opts.AppOrigins,
		magpie:     opts.Magpie,
		executable: opts.Executable,
		limiter:    newLoginLimiter(),
		now:        time.Now,
		mux:        http.NewServeMux(),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.appOrigins == nil {
		s.appOrigins = AppOrigins("")
	}
	if s.magpie == nil {
		s.magpie = magpie.Default
	}
	s.static = opts.Static
	if s.static == nil {
		s.static = s.defaultStatic()
	}
	s.api.server = s
	s.registerRoutes()
	s.app = s.appFront(s.mux)
	s.net = s.netFront(s.mux)
	return s
}

// defaultStatic picks the UI: StaticDir on disk when it has an index.html
// (`serve --static`, NOTES_STATIC_DIR), else the embedded build, else nil.
func (s *Server) defaultStatic() fs.FS {
	if dir := s.core.Settings().StaticDir; dir != "" {
		disk := os.DirFS(dir)
		if webui.HasIndexFS(disk) {
			return disk
		}
		s.log.Debug("static folder has no index.html; using the embedded UI", "dir", dir)
	}
	if webui.HasIndex() {
		return webui.FS()
	}
	s.log.Warn("the web UI is not built into this binary (npm --prefix frontend run build:app)")
	return nil
}

// Core returns the server's core.
func (s *Server) Core() *Core { return s.core }

// AppHandler is the handler for the desktop app's mygo:// scheme
// (mygo.Protocol.Handle("mygo", srv.AppHandler())). Its requests are
// trusted, but if an Origin header is present it must be one of the app's
// origins (else 403), and requests without an Origin may only be GET or
// HEAD (navigations, <img>, <iframe>), except a request referred by the
// desktop app's root page. WKWebView omits Origin for same-scheme fetches.
func (s *Server) AppHandler() http.Handler { return s.app }

// NetHandler is the handler for TCP listeners: the desktop app's loopback
// API (see StartAPI) and `marginalia serve`. It authenticates with the
// Bearer API token or the session cookie, applies the Python Origin guard to
// cookie-authenticated unsafe requests and adds CORS headers for
// Settings.API.CORSOrigins.
func (s *Server) NetHandler() http.Handler { return s.net }

// appFront is the AppHandler middleware.
func (s *Server) appFront(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w.Header(), r.URL.Path)
		origin := r.Header.Get("Origin")
		switch {
		case origin != "":
			if !s.isAppOrigin(origin) {
				writeError(w, r, errOrigin)
				return
			}
			// Under `mygo dev` the page (Vite) is another origin than the
			// scheme: let it read responses, with credentials.
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", "*")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", r.Header.Get("Access-Control-Request-Method"))
				if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
					h.Set("Access-Control-Allow-Headers", req)
				}
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		case r.Method != http.MethodGet && r.Method != http.MethodHead && !appRootReferer(r):
			writeError(w, r, errOrigin)
			return
		}
		user := s.core.state().settings.LoginUser
		next.ServeHTTP(w, withAuth(r, authInfo{kind: authApp, user: user}))
	})
}

// appRootReferer accepts only the SPA's root document, not sandbox.html or
// arbitrary vault files served under the same custom scheme. Browsers set
// Referer themselves; a missing or foreign referer remains untrusted.
func appRootReferer(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Referer"))
	return err == nil && u.User == nil && strings.EqualFold(u.Scheme, "mygo") &&
		strings.EqualFold(u.Host, "localhost") && u.Path == "/" && u.RawQuery == "" && u.Fragment == ""
}

func (s *Server) isAppOrigin(origin string) bool {
	for _, o := range s.appOrigins {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// netFront is the NetHandler middleware.
func (s *Server) netFront(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w.Header(), r.URL.Path)
		st := s.core.state()
		if applyCORS(w, r, st) {
			return
		}
		info, exp := s.authenticate(r, st)
		if info.kind == authNone {
			// Only public routes (login, static files, health) are left, and
			// none of them needs a large or slow body.
			r.Body = http.MaxBytesReader(w, r.Body, maxPublicBody)
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(publicReadTimeout))
		}
		if info.kind != authToken && !sameOriginOrAllowed(r, st) {
			writeError(w, r, errOrigin)
			return
		}
		if info.kind == authSession && r.URL.Path != "/api/auth/logout" {
			lifetime := time.Duration(max(st.settings.SessionHours, 1)) * time.Hour
			if issued := time.Unix(exp, 0).Add(-lifetime); s.now().Sub(issued) > sessionRefresh {
				s.setSession(w, st, info.user) // sliding expiry, like Starlette
			}
		}
		next.ServeHTTP(w, withAuth(r, info))
	})
}

// Content-Security-Policy values of the Python backend.
const (
	sandboxCDNs = "https://cdn.jsdelivr.net https://cdnjs.cloudflare.com https://unpkg.com"
	// appCSP applies to every response except /api/file and /sandbox.html.
	appCSP = "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; " +
		"font-src 'self' data:; frame-src 'self'; object-src 'self'; base-uri 'none'; frame-ancestors 'self'"
	// sandboxCSP applies to /sandbox.html, where AI "visual" blocks run in an
	// <iframe sandbox="allow-scripts">: CDN libraries, no network requests.
	sandboxCSP = "sandbox allow-scripts; default-src 'none'; " +
		"script-src 'unsafe-inline' 'unsafe-eval' " + sandboxCDNs + "; style-src 'unsafe-inline' " + sandboxCDNs + " " +
		"https://fonts.googleapis.com; font-src data: https://fonts.gstatic.com https://cdn.jsdelivr.net; " +
		"img-src data: blob: https:; media-src data: blob: https:; connect-src 'none'; frame-ancestors 'self'"
	// fileCSP applies to raw vault files other than PDFs.
	fileCSP = "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
)

// securityHeaders sets the headers of the Python "guard" middleware. They
// are set before the handler runs, which may still override them.
func securityHeaders(h http.Header, path string) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	switch {
	case path == "/sandbox.html":
		h.Set("Content-Security-Policy", sandboxCSP)
		h.Set("Cache-Control", "no-cache")
	case !strings.HasPrefix(path, "/api/file"):
		h.Set("Content-Security-Policy", appCSP)
	}
}

// guard wraps a route handler with its access and vault requirements.
func (s *Server) guard(rt route, fn func(http.ResponseWriter, *http.Request, *state) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := authOf(r)
		switch rt.access {
		case accessUser:
			if info.kind == authNone {
				writeError(w, r, errLoginRequired)
				return
			}
		case accessApp:
			switch info.kind {
			case authNone:
				writeError(w, r, errLoginRequired)
				return
			case authToken:
				writeError(w, r, errAppOnly)
				return
			}
		}
		st := s.core.state()
		if rt.vault && st.vault == nil {
			writeError(w, r, errNoVault)
			return
		}
		if err := fn(w, r, st); err != nil {
			writeError(w, r, err)
		}
	})
}

// handle adds a route to the table and the mux.
func (s *Server) handle(method, path string, acc access, needVault, documented bool, fn func(http.ResponseWriter, *http.Request, *state) error) {
	rt := route{method: method, path: path, access: acc, vault: needVault, documented: documented}
	rt.handler = s.guard(rt, fn)
	s.routes = append(s.routes, rt)
	s.mux.Handle(method+" "+path, rt.handler)
}

// registerRoutes builds the route table (port of main.py plus the desktop
// extensions).
func (s *Server) registerRoutes() {
	const pub, user, app = accessPublic, accessUser, accessApp
	const doc, undoc = true, false
	const vault, novault = true, false

	s.handle("GET", "/healthz", pub, novault, doc, s.healthz)

	// auth
	s.handle("POST", "/api/auth/login", pub, novault, doc, s.login)
	s.handle("POST", "/api/auth/logout", pub, novault, doc, s.logout)
	s.handle("GET", "/api/auth/me", user, novault, doc, s.me)
	s.handle("POST", "/api/client-error", user, novault, doc, s.clientError)

	// vault
	s.handle("GET", "/api/tree", user, vault, doc, s.tree)
	s.handle("GET", "/api/note", user, vault, doc, s.readNote)
	s.handle("PUT", "/api/note", user, vault, doc, s.saveNote)
	s.handle("POST", "/api/note", user, vault, doc, s.createNote)
	s.handle("DELETE", "/api/note", user, vault, doc, s.deleteNote)
	s.handle("POST", "/api/move", user, vault, doc, s.move)
	s.handle("GET", "/api/file", user, vault, doc, s.rawFile)
	s.handle("POST", "/api/upload", user, vault, doc, s.upload)
	s.handle("GET", "/api/search", user, vault, doc, s.search)
	s.handle("GET", "/api/backlinks", user, vault, doc, s.backlinks)
	s.handle("GET", "/api/index", user, vault, doc, s.index)
	s.handle("GET", "/api/graph", user, vault, doc, s.graph)

	// git
	s.handle("GET", "/api/git/status", user, vault, doc, s.gitStatus)
	s.handle("POST", "/api/git/sync", user, vault, doc, s.gitSync)
	s.handle("GET", "/api/history", user, vault, doc, s.history)
	s.handle("GET", "/api/history/version", user, vault, doc, s.historyVersion)

	// AI
	s.handle("GET", "/api/ai/models", user, novault, doc, s.models)
	s.handle("GET", "/api/ai/prompts", user, vault, doc, s.prompts)
	s.handle("POST", "/api/ai/chat", user, vault, doc, s.chat)
	s.handle("POST", "/api/ai/agent", user, vault, doc, s.agent)
	s.handle("POST", "/api/ai/apply", user, vault, doc, s.apply)

	// chats
	s.handle("GET", "/api/chats", user, vault, doc, s.listChats)
	s.handle("GET", "/api/chats/load", user, vault, doc, s.loadChat)
	s.handle("PUT", "/api/chats", user, vault, doc, s.saveChat)

	// external access
	s.handle("GET", "/api/mcp-info", user, novault, doc, s.mcpInfo)
	s.handle("GET", "/api/openapi.json", pub, novault, doc, s.openAPI)
	s.mux.Handle("/mcp", s.mcpHandler())

	// desktop / app (never with the API token)
	s.handle("GET", "/api/app/settings", app, novault, undoc, s.getSettings)
	s.handle("PUT", "/api/app/settings", app, novault, undoc, s.putSettings)
	s.handle("POST", "/api/app/vault/init", app, novault, undoc, s.initVault)
	s.handle("GET", "/api/app/integrations", app, novault, undoc, s.integrations)
	s.handle("POST", "/api/app/token/rotate", app, novault, undoc, s.rotateToken)
	s.handle("POST", "/api/app/magpie/use", app, novault, undoc, s.magpieUse)
	s.handle("POST", "/api/app/magpie/register-mcp", app, novault, undoc, s.magpieRegister)
	s.handle("POST", "/api/app/magpie/unregister-mcp", app, novault, undoc, s.magpieUnregister)

	// static UI
	s.mux.HandleFunc("GET /sandbox.html", s.sandbox)
	s.mux.HandleFunc("/api/", s.apiFallback)
	s.mux.HandleFunc("/", s.spa)
}

// apiFallback answers unknown /api/ paths with FastAPI's 404, and known
// paths with a wrong method with 405.
func (s *Server) apiFallback(w http.ResponseWriter, r *http.Request) {
	var allow []string
	for _, rt := range s.routes {
		if rt.path == r.URL.Path {
			allow = append(allow, rt.method)
			if rt.method == http.MethodGet {
				allow = append(allow, http.MethodHead)
			}
		}
	}
	if len(allow) == 0 {
		writeError(w, r, errNotFound)
		return
	}
	w.Header().Set("Allow", strings.Join(allow, ", "))
	writeError(w, r, &apiError{Status: http.StatusMethodNotAllowed, Detail: "Method Not Allowed"})
}

// healthz is GET /healthz: {"ok": true}. On the TCP listener, ?nonce=…
// adds "proof" (mcp.HealthProof over the API token and the port the request
// came in on), with which `marginalia mcp` checks that it found this app
// before it sends the token (mcp.ProbeApp).
func (s *Server) healthz(w http.ResponseWriter, r *http.Request, _ *state) error {
	nonce := r.URL.Query().Get("nonce")
	local, _ := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if nonce == "" || len(nonce) > mcp.HealthNonceMax || local == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proof": mcp.HealthProof(s.core.Token(), nonce, local.Port)})
	return nil
}

// executablePath is the command registered for the stdio MCP server.
func (s *Server) executablePath() string {
	if s.executable != "" {
		return s.executable
	}
	exe, err := os.Executable()
	if err != nil {
		return "marginalia"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// Shutdown stops the API listener (gracefully, within ctx) and closes the
// core, which commits pending git edits. Call it once when the app quits.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.StopAPI(ctx)
	s.core.Close()
	return err
}
