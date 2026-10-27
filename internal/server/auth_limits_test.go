package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// countingReader is a request body that records how much was read.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// A rate-limited client must not be able to make the server read (and
// buffer) its body, and login bodies are small.
func TestLoginChecksTheLimitBeforeReadingTheBody(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	f.srv.now = func() time.Time { return now }
	c := f.web()

	big := `{"password": "` + strings.Repeat("x", 1<<20) + `"}`
	expect(t, c.do("POST", "/api/auth/login", big), 413)
	for range loginAttempts {
		expect(t, c.do("POST", "/api/auth/login", map[string]string{"password": "wrong"}), 401)
	}
	body := &countingReader{r: strings.NewReader(big)}
	req := httptest.NewRequest("POST", "/api/auth/login", body)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	f.srv.NetHandler().ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests || body.read != 0 {
		t.Fatalf("limited login: status %d, %d body bytes read", res.Code, body.read)
	}
	// The longest allowed password still fits.
	other := f.client(f.srv.NetHandler(), "192.0.2.9:1")
	expect(t, other.do("POST", "/api/auth/login", map[string]string{"password": strings.Repeat("😀", 512)}), 401)
	// The app needs no login and is never limited.
	expect(t, f.app().do("POST", "/api/auth/login", map[string]string{"password": ""}), 200)
}

// Unauthenticated NetHandler requests can only send small bodies, whatever
// route they reach.
func TestUnauthenticatedBodiesAreCapped(t *testing.T) {
	f := newFixture(t)
	f.srv.mux.HandleFunc("POST /test/read-body", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		_, _ = fmt.Fprint(w, len(data))
	})
	large := strings.Repeat("x", maxPublicBody+1)
	expect(t, f.web().do("POST", "/test/read-body", large), 413)
	expect(t, f.web().do("POST", "/test/read-body", strings.Repeat("x", 1000)), 200)
	expect(t, f.bearer().do("POST", "/test/read-body", large), 200)
	expect(t, f.loggedIn().do("POST", "/test/read-body", large), 200)
}

// X-Forwarded-For is believed only from the configured proxies: never in
// the desktop app, the loopback addresses by default in server mode.
func TestForwardedForTrust(t *testing.T) {
	wrongLogins := func(f *fixture, n int) map[int]int {
		codes := map[int]int{}
		for i := range n {
			c := f.client(f.srv.NetHandler(), "127.0.0.1:5000")
			res := c.do("POST", "/api/auth/login", map[string]string{"password": "wrong"},
				"X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
			codes[res.Code]++
		}
		return codes
	}
	n := loginAttempts + 4

	desktop := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	if codes := wrongLogins(desktop, n); codes[429] != 4 {
		t.Errorf("desktop: %v, want the forwarded address ignored", codes)
	}
	server := newFixture(t)
	if codes := wrongLogins(server, n); codes[401] != n {
		t.Errorf("server behind a loopback proxy: %v, want every client counted apart", codes)
	}
	untrusting := newFixture(t, func(o *fixtureOptions) {
		o.settings = func(s *settingsT) { s.ForwardedAllowIPs = "" }
	})
	if codes := wrongLogins(untrusting, n); codes[429] != 4 {
		t.Errorf("server trusting no proxy: %v", codes)
	}

	for _, tc := range []struct {
		remote, allow string
		want          bool
	}{
		{"127.0.0.1:1", "127.0.0.1,::1", true},
		{"[::1]:1", "127.0.0.1,::1", true},
		{"[::ffff:127.0.0.1]:1", "127.0.0.1", true},
		{"127.0.0.2:1", "127.0.0.1,::1", false},
		{"10.1.2.3:1", "10.0.0.0/8", true},
		{"10.1.2.3:1", " 192.168.0.1 , 10.0.0.0/8 ", true},
		{"192.0.2.1:1", "*", true},
		{"192.0.2.1:1", "", false},
		{"mygo:3", "*", false},
	} {
		if got := proxyAllowed(tc.remote, tc.allow); got != tc.want {
			t.Errorf("proxyAllowed(%q, %q) = %v", tc.remote, tc.allow, got)
		}
	}
}

func TestLoginLimiterIsBounded(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := range maxLimiterKeys {
		l.failure(fmt.Sprintf("k%d", i), now)
	}
	for i := range loginAttempts {
		key := fmt.Sprintf("new%d", i)
		if !l.allow(key, now) {
			t.Fatalf("attempt %d refused too early", i)
		}
		l.failure(key, now)
	}
	if len(l.events) != maxLimiterKeys+1 {
		t.Errorf("tracked keys = %d, want the cap plus the overflow bucket", len(l.events))
	}
	if l.allow("another", now) {
		t.Error("new keys beyond the cap are not limited")
	}
	if !l.allow("k1", now) {
		t.Error("a tracked key is limited by the overflow bucket")
	}
	// Old entries expire and free the map again.
	later := now.Add(loginWindow + time.Second)
	if !l.allow("another", later) || len(l.events) != 0 {
		t.Errorf("after the window: %d keys tracked", len(l.events))
	}
}
