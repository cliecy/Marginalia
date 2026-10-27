package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"marginalia/internal/config"
	"marginalia/internal/mcp"
)

// PortFallbacks is how many ports after the configured one StartAPI tries
// when it is taken.
const PortFallbacks = 20

// EndpointFileName is the file in DataDir where a running app records the
// URL its API listener actually uses, or that it runs with the API turned
// off (see FindLocalApp).
const EndpointFileName = "api-endpoint.json"

// shutdownGrace bounds the graceful drain of a listener that is replaced.
const shutdownGrace = 5 * time.Second

// APIStatus describes the TCP API listener.
type APIStatus struct {
	// Enabled is Settings.API.Enabled (always true for Serve).
	Enabled bool `json:"enabled"`
	// Running is true while the listener accepts connections.
	Running bool `json:"running"`
	// URL is the base URL clients use, e.g. "http://127.0.0.1:8190": the
	// real address once running (it may be a fallback port), else the
	// configured one.
	URL string `json:"url"`
	// Error is why the listener is not running although enabled.
	Error string `json:"error,omitempty"`
}

// apiListener runs NetHandler on one TCP listener and replaces it when the
// API settings change.
type apiListener struct {
	server *Server

	mu    sync.Mutex
	fixed bool // started by Serve: the address does not follow the settings
	srv   *http.Server
	ln    net.Listener
	url   string
	err   error
	done  chan error // receives srv.Serve's unexpected failure
}

// StartAPI starts the loopback API listener (REST, MCP and the web UI on
// TCP) as Settings.API says: nothing when disabled; else Host:Port, or the
// next free port among the PortFallbacks following ones. The URL in use is
// reported by APIStatus and recorded in DataDir/api-endpoint.json. A running
// listener is replaced. The error says why nothing could be bound (it is
// also kept in APIStatus().Error).
func (s *Server) StartAPI() error {
	return s.api.start(s.core.Settings().API)
}

// RestartAPI re-applies Settings.API (PUT /api/app/settings calls it when
// the api section changes). It is StartAPI under another name.
func (s *Server) RestartAPI() error { return s.StartAPI() }

// StopAPI stops the API listener: it stops accepting at once and drains
// open requests until ctx ends (streams are cancelled), then closes what is
// left.
func (s *Server) StopAPI(ctx context.Context) error {
	s.api.mu.Lock()
	defer s.api.mu.Unlock()
	return s.api.stopLocked(ctx)
}

// APIStatus reports the API listener's state.
func (s *Server) APIStatus() APIStatus {
	cfg := s.core.Settings().API
	l := &s.api
	l.mu.Lock()
	defer l.mu.Unlock()
	st := APIStatus{Enabled: cfg.Enabled || l.fixed, URL: mcp.AppBaseURL(cfg.Host, cfg.Port)}
	if l.srv != nil {
		st.Running, st.URL = true, l.url
	} else if l.err != nil && st.Enabled {
		st.Error = l.err.Error()
	}
	return st
}

// Serve is `marginalia serve`: it serves NetHandler on addr (exactly that
// address; no port fallback) until ctx is cancelled, then shuts down
// gracefully (open requests get up to 10 s) and closes the core, which
// commits pending git edits. The listener ignores Settings.API.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	l := &s.api
	l.mu.Lock()
	_ = l.stopLocked(nil)
	l.fixed = true
	l.serveLocked(ln)
	done := l.done
	url := l.url
	l.mu.Unlock()
	s.log.Info("Marginalia is listening", "url", url)

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-done:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(serveErr, s.Shutdown(shutdownCtx))
}

func (l *apiListener) start(cfg config.APISettings) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fixed {
		return nil
	}
	_ = l.stopLocked(nil)
	l.err = nil
	if !cfg.Enabled {
		if l.server.core.Mode() == config.ModeDesktop {
			// Tell `marginalia mcp` that the app runs, so it does not
			// write the vault behind the app's back.
			writeEndpoint(l.server.core.Settings().DataDir, "", l.server.log)
		}
		return nil
	}
	ln, err := listenWithFallback(cfg.Host, cfg.Port)
	if err != nil {
		l.err = err
		l.server.log.Warn("the local API could not start", "err", err)
		return err
	}
	l.serveLocked(ln)
	if port := cfg.Port; port != 0 && l.url != mcp.AppBaseURL(cfg.Host, port) {
		l.server.log.Info("the configured API port is taken; using another one", "configured", port, "url", l.url)
	}
	return nil
}

// listenWithFallback binds host:port, else one of the PortFallbacks next
// ports. Port 0 binds any free port.
func listenWithFallback(host string, port int) (net.Listener, error) {
	if port == 0 {
		return net.Listen("tcp", net.JoinHostPort(host, "0"))
	}
	last := min(port+PortFallbacks, 65535)
	var firstErr error
	for p := port; p <= last; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, fmt.Errorf("无法在 %s 的端口 %d–%d 上启动本地 API：%w", host, port, last, firstErr)
}

// serveLocked serves NetHandler on ln. Callers hold l.mu.
func (l *apiListener) serveLocked(ln net.Listener) {
	s := l.server
	base, cancel := context.WithCancel(context.Background())
	srv := &http.Server{
		Handler:           s.NetHandler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return base },
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	// Shutdown cancels request contexts at once, so AI streams end
	// instead of holding the drain open.
	srv.RegisterOnShutdown(cancel)
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		cancel()
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return // stopped on purpose
		}
		s.log.Error("the API listener failed", "err", err)
		l.mu.Lock()
		if l.srv == srv { // still the current listener: report it as down
			l.srv, l.ln, l.url, l.done, l.err = nil, nil, "", nil, err
			removeEndpoint(s.core.Settings().DataDir)
		}
		l.mu.Unlock()
		_ = ln.Close()
		done <- err
	}()
	addr := ln.Addr().(*net.TCPAddr)
	host, _, _ := net.SplitHostPort(ln.Addr().String())
	if addr.IP.IsUnspecified() {
		host = "" // AppBaseURL maps the wildcard to loopback
		if addr.IP.To4() == nil {
			host = "::"
		}
	}
	l.srv, l.ln, l.done, l.err = srv, ln, done, nil
	l.url = mcp.AppBaseURL(host, addr.Port)
	writeEndpoint(s.core.Settings().DataDir, l.url, s.log)
}

// stopLocked closes the listener now and drains its connections: within
// ctx when given (synchronously), else in the background. Callers hold l.mu.
func (l *apiListener) stopLocked(ctx context.Context) error {
	removeEndpoint(l.server.core.Settings().DataDir)
	srv, ln := l.srv, l.ln
	if srv == nil {
		return nil
	}
	l.srv, l.ln, l.url, l.done = nil, nil, "", nil
	_ = ln.Close() // free the port before a replacement binds it
	drain := func(ctx context.Context) error {
		err := srv.Shutdown(ctx)
		if err != nil {
			_ = srv.Close()
		}
		return err
	}
	if ctx != nil {
		return drain(ctx)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = drain(ctx)
	}()
	return nil
}

// endpoint is the content of DataDir/api-endpoint.json: the API's base URL
// ("" while the API is turned off) and the process ID of the app.
type endpoint struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

func writeEndpoint(dataDir, url string, log *slog.Logger) {
	if dataDir == "" {
		return
	}
	data, _ := json.Marshal(endpoint{URL: url, PID: os.Getpid()})
	path := filepath.Join(dataDir, EndpointFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		log.Warn("cannot record the API address", "err", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Warn("cannot record the API address", "err", err)
	}
}

// removeEndpoint deletes the endpoint file when this process wrote it.
func removeEndpoint(dataDir string) {
	if ep, ok := readEndpoint(dataDir); ok && ep.PID == os.Getpid() {
		_ = os.Remove(filepath.Join(dataDir, EndpointFileName))
	}
}

// readEndpoint reads DataDir/api-endpoint.json. A file left behind by a
// process that no longer runs (a crashed app) is ignored.
func readEndpoint(dataDir string) (endpoint, bool) {
	if dataDir == "" {
		return endpoint{}, false
	}
	data, err := os.ReadFile(filepath.Join(dataDir, EndpointFileName))
	if err != nil {
		return endpoint{}, false
	}
	var ep endpoint
	if json.Unmarshal(data, &ep) != nil || ep.PID <= 0 {
		return endpoint{}, false
	}
	if ep.PID != os.Getpid() && !processAlive(ep.PID) {
		return endpoint{}, false
	}
	return ep, true
}

// LocalApp is what the settings of `marginalia mcp` tell about the
// desktop app of the same data folder.
type LocalApp struct {
	// URL is the base URL of the app's API to probe: the address a running
	// app recorded (it may use a fallback port), else the configured
	// Settings.API host and port. It is "" when the running app has its API
	// turned off.
	URL string
	// Running is true when a running app recorded itself in
	// DataDir/api-endpoint.json.
	Running bool
}

// FindLocalApp looks for the desktop app of s.DataDir. Probe the URL
// (mcp.ProbeApp) before use: something else may answer there.
func FindLocalApp(s config.Settings) LocalApp {
	if ep, ok := readEndpoint(s.DataDir); ok {
		return LocalApp{URL: ep.URL, Running: true}
	}
	return LocalApp{URL: mcp.AppBaseURL(s.API.Host, s.API.Port)}
}

// LocalAPIURL is the base URL of the desktop app's API for the settings of
// `marginalia mcp`: FindLocalApp's URL, or the configured Settings.API
// host and port when the running app has its API turned off.
func LocalAPIURL(s config.Settings) string {
	if app := FindLocalApp(s); app.URL != "" {
		return app.URL
	}
	return mcp.AppBaseURL(s.API.Host, s.API.Port)
}
