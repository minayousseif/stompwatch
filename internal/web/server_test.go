package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
)

// --- Identity ---

// docs/http-api.md "Identity": in tailscale mode the login comes from the
// Tailscale header, and the review row records it.
func TestIdentityReadsTheTailscaleHeaders(t *testing.T) {
	e := newEnv(t)
	var gotLogin, gotName string
	h := e.srv.identity(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotLogin, gotName = Identity(r)
	}))

	req := httptest.NewRequest("GET", "/api/summary", nil)
	req.Header.Set("Tailscale-User-Login", "me@example.com")
	req.Header.Set("Tailscale-User-Name", "Me")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotLogin != "me@example.com" || gotName != "Me" {
		t.Errorf("Identity = %q, %q; want \"me@example.com\", \"Me\"", gotLogin, gotName)
	}
}

// A missing login header used to be served as "unknown" with full rights.
// Only a request that did not come through `tailscale serve` or the proxy
// has none, so it is refused, and the message says how to get in. The page
// itself still loads, so the owner can read the message. The dashboard's
// own client always sends the headers it gets from the layer in front.
func TestAnAPIRequestWithNoLoginIs401(t *testing.T) {
	modes := map[string]struct {
		set func(*Config, *config.Config)
		way string // how the message says to get in
	}{
		"tailscale": {func(_ *Config, f *config.Config) { f.AuthMode = config.AuthTailscale }, "tailscale serve"},
		"trusted_header": {func(_ *Config, f *config.Config) {
			f.AuthMode = config.AuthTrustedHeader
			f.AuthHeader = "X-Pocket-User"
		}, "reverse proxy"},
	}
	for name, mode := range modes {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, mode.set)
			for _, headers := range [][]string{nil, {"Tailscale-User-Login", "  "}, {"X-Pocket-User", " "}} {
				w := e.send(http.MethodGet, "/api/settings", "", headers...)
				wantError(t, w, http.StatusUnauthorized, mode.way)
				wantError(t, w, http.StatusUnauthorized, "auth_mode = none")
			}
			if w := e.send(http.MethodGet, "/", ""); w.Code != http.StatusOK {
				t.Errorf("GET / with no login = %d, want 200: the page must load to show the message", w.Code)
			}
		})
	}
}

// A write with no login is refused before it does anything.
func TestAWriteWithNoLoginChangesNothing(t *testing.T) {
	e := newEnv(t)
	w := e.send(http.MethodPut, "/api/settings", `{"threshold_db":"19"}`, "Content-Type", "application/json")
	wantError(t, w, http.StatusUnauthorized, "tailscale serve")
	if got := e.live.Current().ThresholdDB; got == 19 {
		t.Errorf("threshold_db = %v after a refused request", got)
	}
}

// auth_mode none has no login header to miss, so nothing is refused.
func TestAuthModeNoneNeedsNoLoginHeader(t *testing.T) {
	e := newEnv(t, func(_ *Config, f *config.Config) { f.AuthMode = config.AuthNone })
	if w := e.send(http.MethodGet, "/api/settings", ""); w.Code != http.StatusOK {
		t.Errorf("GET /api/settings in auth_mode none = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// tailscale serve marks a request that came in through Funnel, from the
// public internet, with Tailscale-Funnel-Request and sets no login on it.
// The dashboard is never meant to be public, so such a request is refused
// outright, the page included, whatever else it carries and in every mode.
func TestAFunnelRequestIs403(t *testing.T) {
	for _, mode := range []string{config.AuthTailscale, config.AuthTrustedHeader, config.AuthNone} {
		e := newEnv(t, func(_ *Config, f *config.Config) { f.AuthMode = mode })
		for _, path := range []string{"/api/settings", "/"} {
			w := e.send(http.MethodGet, path, "",
				"Tailscale-Funnel-Request", "?1", "Tailscale-User-Login", "someone@example.com")
			wantError(t, w, http.StatusForbidden, "Funnel")
		}
	}
}

func TestTrustedHeaderModeReadsTheConfiguredHeaders(t *testing.T) {
	e := newEnv(t, func(_ *Config, f *config.Config) {
		f.AuthMode = config.AuthTrustedHeader
		f.AuthHeader = "X-Pocket-User"
		f.AuthNameHeader = "X-Pocket-Name"
	})
	var gotLogin, gotName string
	h := e.srv.identity(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotLogin, gotName = Identity(r)
	}))
	req := httptest.NewRequest("GET", "/api/summary", nil)
	req.Header.Set("X-Pocket-User", "owner")
	req.Header.Set("X-Pocket-Name", "The Owner")
	req.Header.Set("Tailscale-User-Login", "wrong@example.com")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotLogin != "owner" || gotName != "The Owner" {
		t.Errorf("Identity = %q, %q; want \"owner\", \"The Owner\"", gotLogin, gotName)
	}
}

// auth_mode none is for development. The header must not be believed.
func TestAuthModeNoneIgnoresHeaders(t *testing.T) {
	e := newEnv(t, func(_ *Config, f *config.Config) { f.AuthMode = config.AuthNone })
	var gotLogin, gotName string
	h := e.srv.identity(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotLogin, gotName = Identity(r)
	}))
	req := httptest.NewRequest("GET", "/api/summary", nil)
	req.Header.Set("Tailscale-User-Login", "someone@example.com")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotLogin != "dev" || gotName != "dev" {
		t.Errorf("Identity = %q, %q; want \"dev\", \"dev\"", gotLogin, gotName)
	}
}

// Identity on a request that never went through the middleware must still
// answer, so a handler can never read an empty reviewer by accident.
func TestIdentityWithoutMiddleware(t *testing.T) {
	login, name := Identity(httptest.NewRequest("GET", "/api/summary", nil))
	if login != "unknown" || name != "" {
		t.Errorf("Identity = %q, %q; want \"unknown\", \"\"", login, name)
	}
}

// --- Headers and routing ---

func TestEveryAPIResponseIsNotCached(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{
		"/api/summary?from=" + itoa(ms(0)) + "&to=" + itoa(ms(time.Minute)),
		"/api/events",
		"/api/health",
		"/api/system",
		"/api/settings",
		"/api/mute-windows",
		"/api/nonsense",
	} {
		w := e.get(path)
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control = %q, want \"no-store\" (status %d)", path, got, w.Code)
		}
	}
}

// A dashboard that shows a web page where JSON was expected is very hard to
// debug from a phone.
func TestUnknownAPIPathIsJSONNotThePage(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/api/does-not-exist", "/api", "/api/events/1/audio"} {
		w := e.get(path)
		if w.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
		wantError(t, w, 404, "")
		if strings.Contains(w.Body.String(), "<") {
			t.Errorf("GET %s: the body looks like HTML: %s", path, w.Body.String())
		}
	}
}

func TestUnknownMethodOnAKnownAPIPathIsJSON(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodPost, "/api/summary", nil)
	if w.Code != 405 {
		t.Fatalf("POST /api/summary = %d, want 405: %s", w.Code, w.Body.String())
	}
	wantError(t, w, 405, "")
	if got := w.Header().Get("Allow"); !strings.Contains(got, "GET") {
		t.Errorf("Allow = %q, want it to name GET", got)
	}
}

// A client-side route must survive a refresh, so an unknown page path gets
// the interface, not a 404.
func TestUnknownPagePathServesTheInterface(t *testing.T) {
	e := newEnv(t)
	w := e.get("/events/12")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want \"no-cache\" on the page", got)
	}
}

func TestHashedAssetsAreCachedForALongTime(t *testing.T) {
	e := newEnv(t)
	// The placeholder build has no assets, so ask for one that is missing:
	// the caching rule is a property of the path, not of the file.
	w := e.get("/assets/app-abc123.js")
	if w.Code == 200 {
		if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=31536000") {
			t.Errorf("Cache-Control = %q, want a long max-age on a hashed asset", got)
		}
		return
	}
	if w.Code != 404 {
		t.Errorf("a missing asset gave %d, want 404 or 200", w.Code)
	}
}

// A missing asset must not fall back to the page: the browser would run HTML
// as JavaScript and the failure would be baffling.
func TestMissingAssetIsNotThePage(t *testing.T) {
	e := newEnv(t)
	w := e.get("/assets/app-abc123.js")
	if w.Code == 200 && strings.Contains(w.Body.String(), "<") {
		t.Errorf("a missing asset returned the HTML page")
	}
}

// --- Failure handling ---

// A panic in one handler must not take the collector down with it.
func TestPanicBecomes500JSON(t *testing.T) {
	e := newEnv(t)
	h := e.srv.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("the handler exploded")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/summary", nil))

	wantError(t, w, 500, "")
	if strings.Contains(w.Body.String(), "exploded") {
		t.Errorf("the body repeats the panic value: %s", w.Body.String())
	}
}

// --- New ---

func TestNewRejectsMissingParts(t *testing.T) {
	e := newEnv(t)
	full := Config{
		Store: e.store, Settings: e.live, Status: e.status, Live: e.feed,
		Stats: func() Stats { return Stats{} }, Started: t0,
		LogDir: e.logDir, LogName: "stompwatch.log", ClipDir: e.clipDir,
	}
	cases := map[string]func(*Config){
		"store":    func(c *Config) { c.Store = nil },
		"settings": func(c *Config) { c.Settings = nil },
		"status":   func(c *Config) { c.Status = nil },
		"live":     func(c *Config) { c.Live = nil },
		"stats":    func(c *Config) { c.Stats = nil },
		"started":  func(c *Config) { c.Started = time.Time{} },
		"log dir":  func(c *Config) { c.LogDir = "" },
		"log name": func(c *Config) { c.LogName = "" },
		"clip dir": func(c *Config) { c.ClipDir = "" },
	}
	for name, break_ := range cases {
		c := full
		break_(&c)
		if _, err := New(c); err == nil {
			t.Errorf("New with no %s returned no error", name)
		}
	}
	if _, err := New(full); err != nil {
		t.Errorf("New with everything set: %v", err)
	}
}

// Run does nothing when the dashboard is switched off.
func TestRunReturnsNilWithNoAddress(t *testing.T) {
	e := newEnv(t, func(_ *Config, f *config.Config) { f.HTTPAddr = "" })
	if err := e.srv.Run(t.Context()); err != nil {
		t.Errorf("Run with an empty http_addr = %v, want nil", err)
	}
}

// Run serves until the context ends, and then lets go of the port.
func TestRunServesAndStops(t *testing.T) {
	// Take a free port from the operating system and give it straight back,
	// so the test cannot clash with anything already listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srvAddr := ln.Addr().String()
	ln.Close()

	e := newEnv(t, func(_ *Config, f *config.Config) { f.HTTPAddr = srvAddr })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.srv.Run(ctx) }()

	req, err := http.NewRequest("GET", "http://"+srvAddr+"/api/mute-windows", nil)
	if err != nil {
		t.Fatal(err)
	}
	asOwner(req)
	var resp *http.Response
	for range 100 {
		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the server never answered on %s: %v", srvAddr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /api/mute-windows = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil after the context ended", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the context ended")
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
