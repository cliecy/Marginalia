package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// SessionCookie is the name of the login session cookie.
const SessionCookie = "marginalia_session"

// Login rate limit: failed attempts per client address and window, as in
// the Python backend. At most maxLimiterKeys addresses are tracked; failures
// of further addresses share one overflow bucket.
const (
	loginAttempts  = 8
	loginWindow    = 300 * time.Second
	maxLimiterKeys = 10000
	overflowKey    = "\x00overflow"
)

// sessionRefresh is how often a sliding session cookie is re-issued.
// (Starlette re-sent it on every response.)
const sessionRefresh = time.Hour

// authKind tells how a request is authenticated.
type authKind int

const (
	authNone    authKind = iota
	authApp              // through AppHandler: the desktop app's own pages
	authSession          // password login cookie (NetHandler)
	authToken            // Bearer API token (NetHandler)
)

func (k authKind) String() string {
	switch k {
	case authApp:
		return "app"
	case authSession:
		return "session"
	case authToken:
		return "token"
	}
	return "none"
}

// authInfo is the authentication of one request.
type authInfo struct {
	kind authKind
	user string
}

type authKey struct{}

func withAuth(r *http.Request, info authInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authKey{}, info))
}

// authOf returns the request's authentication (authNone when absent).
func authOf(r *http.Request) authInfo {
	info, _ := r.Context().Value(authKey{}).(authInfo)
	return info
}

// ---------------------------------------------------------------- session

// session is the signed content of the session cookie.
type session struct {
	User string `json:"user"`
	Exp  int64  `json:"exp"` // Unix seconds
}

// signSession encodes s as base64url(JSON) "." base64url(HMAC-SHA256).
func signSession(secret []byte, s session) string {
	payload, _ := json.Marshal(s)
	enc := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(enc))
	return enc + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// parseSession verifies a cookie value and returns its session when the
// signature matches and it has not expired.
func parseSession(secret []byte, value string, now time.Time) (session, bool) {
	enc, sig, ok := strings.Cut(value, ".")
	if !ok || enc == "" {
		return session{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return session{}, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(enc))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return session{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return session{}, false
	}
	var s session
	if json.Unmarshal(payload, &s) != nil || s.User == "" || now.Unix() >= s.Exp {
		return session{}, false
	}
	return s, true
}

// setSession issues a fresh session cookie for user.
func (s *Server) setSession(w http.ResponseWriter, st *state, user string) {
	lifetime := time.Duration(max(st.settings.SessionHours, 1)) * time.Hour
	value := signSession(s.core.sessionSecret, session{User: user, Exp: s.now().Add(lifetime).Unix()})
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(lifetime / time.Second),
		HttpOnly: true,
		Secure:   st.settings.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSession deletes the session cookie.
func clearSession(w http.ResponseWriter, st *state) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   st.settings.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ---------------------------------------------------------- authentication

// bearerToken returns the token of an "Authorization: Bearer <token>"
// header ("" when absent).
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// authenticate resolves the authentication of a NetHandler request: a valid
// Bearer token (or ?token= on /mcp), else a valid session cookie. The
// session's expiry is returned for sliding renewal.
func (s *Server) authenticate(r *http.Request, st *state) (authInfo, int64) {
	if s.core.checkToken(bearerToken(r)) {
		return authInfo{kind: authToken, user: st.settings.LoginUser}, 0
	}
	if r.URL.Path == "/mcp" && s.core.checkToken(r.URL.Query().Get("token")) {
		return authInfo{kind: authToken, user: st.settings.LoginUser}, 0
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		if sess, ok := parseSession(s.core.sessionSecret, c.Value, s.now()); ok {
			return authInfo{kind: authSession, user: sess.User}, sess.Exp
		}
	}
	return authInfo{}, 0
}

// ------------------------------------------------------------------ origins

// AppOrigins returns the origins of the desktop app's own pages, which
// AppHandler accepts: mygo://localhost, http://mygo.localhost on Windows
// (where WebView2 serves custom schemes), and the origin of devURL (the Vite
// dev server `mygo dev` loads pages from) when it is not "".
func AppOrigins(devURL string) []string {
	origins := []string{"mygo://localhost"}
	if isWindows {
		origins = append(origins, "http://mygo.localhost")
	}
	if o := originOf(devURL); o != "" && !slices.Contains(origins, o) {
		origins = append(origins, o)
	}
	return origins
}

// originOf returns "scheme://host[:port]" of raw, or "" if it has none.
func originOf(raw string) string {
	raw = strings.TrimSpace(raw)
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" || rest == "" {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	if host == "" {
		return ""
	}
	return strings.ToLower(scheme) + "://" + strings.ToLower(host)
}

// requestScheme is "https" for TLS requests, or what a trusted reverse
// proxy says in X-Forwarded-Proto (see trustsProxy), else "http".
func requestScheme(r *http.Request, trusted bool) string {
	if r.TLS != nil {
		return "https"
	}
	if trusted {
		if proto := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])); proto == "https" || proto == "http" {
			return proto
		}
	}
	return "http"
}

// clientIP is the address used for login rate limiting: the peer address,
// or the closest X-Forwarded-For hop when the peer is a trusted proxy.
func clientIP(r *http.Request, trusted bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if trusted {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			hops := strings.Split(fwd, ",")
			if hop := strings.TrimSpace(hops[len(hops)-1]); hop != "" {
				return hop
			}
		}
	}
	if host == "" {
		return "?"
	}
	return host
}

// trustsProxy reports whether r's peer is a reverse proxy whose
// X-Forwarded-For and X-Forwarded-Proto headers are believed: one of
// Settings.ForwardedAllowIPs, like uvicorn's --forwarded-allow-ips (serve
// defaults to the loopback addresses, as uvicorn --proxy-headers did; the
// desktop app to none, since nothing sits in front of its loopback API and
// trusting the header would let any local client pick its own rate-limit
// key). Requests through AppHandler never trust it.
func trustsProxy(r *http.Request, st *state) bool {
	if authOf(r).kind == authApp {
		return false
	}
	return proxyAllowed(r.RemoteAddr, st.settings.ForwardedAllowIPs)
}

// proxyAllowed reports whether the peer remoteAddr ("host:port") is in allow,
// a comma-separated list of IP addresses and CIDR ranges ("*" = any peer,
// "" = none).
func proxyAllowed(remoteAddr, allow string) bool {
	if strings.TrimSpace(allow) == "" {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, item := range strings.Split(allow, ",") {
		item = strings.TrimSpace(item)
		switch {
		case item == "*":
			return true
		case strings.Contains(item, "/"):
			if prefix, err := netip.ParsePrefix(item); err == nil && prefix.Contains(addr) {
				return true
			}
		default:
			if a, err := netip.ParseAddr(item); err == nil && a.Unmap().WithZone("") == addr {
				return true
			}
		}
	}
	return false
}

// sameOriginOrAllowed is the Python "guard" middleware: unsafe requests
// carrying an Origin must come from this server's own origin or from one of
// AllowedOrigins.
func sameOriginOrAllowed(r *http.Request, st *state) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == requestScheme(r, trustsProxy(r, st))+"://"+r.Host {
		return true
	}
	return slices.Contains(st.settings.AllowedOrigins, origin)
}

// corsAllowed reports whether origin may call the API cross-origin with a
// Bearer token (settings API.CORSOrigins; "*" allows any origin).
func corsAllowed(origin string, st *state) bool {
	if origin == "" || origin == "null" {
		return false
	}
	for _, o := range st.settings.API.CORSOrigins {
		if o == "*" || strings.EqualFold(strings.TrimRight(o, "/"), origin) {
			return true
		}
	}
	return false
}

// corsRequestHeaders are the request headers cross-origin API clients may
// send.
const corsRequestHeaders = "Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID"

// applyCORS adds CORS headers for allowed origins and answers preflights.
// It reports whether the request was a preflight it answered. Credentials
// (cookies) are never allowed cross-origin: CORS is for Bearer clients.
func applyCORS(w http.ResponseWriter, r *http.Request, st *state) bool {
	origin := r.Header.Get("Origin")
	if !corsAllowed(origin, st) {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Add("Vary", "Origin")
	h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id, Content-Disposition, WWW-Authenticate")
	if r.Method != http.MethodOptions || r.Header.Get("Access-Control-Request-Method") == "" {
		return false
	}
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", corsRequestHeaders)
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
	return true
}

// -------------------------------------------------------------- rate limit

// loginLimiter counts failed logins per client address in a sliding
// window (Python's LoginLimiter).
type loginLimiter struct {
	mu       sync.Mutex
	attempts int
	window   time.Duration
	events   map[string][]time.Time
	lastGC   time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: loginAttempts, window: loginWindow, events: map[string][]time.Time{}}
}

// prune drops events older than the window; callers hold mu.
func (l *loginLimiter) prune(key string, now time.Time) []time.Time {
	events := l.events[key]
	i := 0
	for i < len(events) && now.Sub(events[i]) > l.window {
		i++
	}
	events = events[i:]
	if len(events) == 0 {
		delete(l.events, key)
		return nil
	}
	l.events[key] = events
	return events
}

// bucket returns the key failures of key are counted under: key itself
// unless the map is full and key is not in it yet. Callers hold mu.
func (l *loginLimiter) bucket(key string) string {
	if _, ok := l.events[key]; ok || len(l.events) < maxLimiterKeys {
		return key
	}
	return overflowKey
}

// allow reports whether key may try to log in now.
func (l *loginLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastGC) > l.window {
		l.lastGC = now
		for k := range l.events {
			l.prune(k, now)
		}
	}
	return len(l.prune(l.bucket(key), now)) < l.attempts
}

// failure records a failed attempt of key.
func (l *loginLimiter) failure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key = l.bucket(key)
	l.events[key] = append(l.events[key], now)
}
