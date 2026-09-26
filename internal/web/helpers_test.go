package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/settings"
	"github.com/minayousseif/stompwatch/internal/store"
)

// t0 is a fixed instant every test builds its data around. It is 03:00 UTC,
// inside the default quiet hours, which the tests read in UTC.
var t0 = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// env is a server with a real database on a temporary directory.
type env struct {
	t       *testing.T
	srv     *Server
	store   *store.Store
	live    *settings.Live
	feed    *LiveFeed
	status  *health.Status
	stats   Stats
	dir     string
	logDir  string
	clipDir string
}

// fileConfig is the config file the tests start from.
func fileConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	c := config.Default()
	c.DBPath = filepath.Join(dir, "noise.db")
	c.ClipDir = filepath.Join(dir, "clips")
	c.LogDir = filepath.Join(dir, "logs")
	c.ExpectedCaptureGain = "none"
	// Pin the clip lengths so the tests do not move when a default changes.
	c.PreRoll = 10 * time.Second
	c.PostRoll = 5 * time.Second
	if err := c.Validate(); err != nil {
		t.Fatalf("the test config does not validate: %v", err)
	}
	return c
}

// sameConfig reports whether two configs hold the same values. RecordingPause
// is a slice, so Config cannot be compared with ==.
func sameConfig(a, b config.Config) bool { return reflect.DeepEqual(a, b) }

// newEnv builds a server. change may adjust the config before New sees it.
func newEnv(t *testing.T, change ...func(*Config, *config.Config)) *env {
	t.Helper()
	dir := t.TempDir()
	file := fileConfig(t, dir)
	e := &env{t: t, dir: dir, logDir: file.LogDir, clipDir: file.ClipDir, feed: NewLiveFeed()}

	var err error
	if e.store, err = store.Open(file.DBPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.store.Close() })
	if err := os.MkdirAll(file.LogDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(file.ClipDir, 0o750); err != nil {
		t.Fatal(err)
	}
	e.status = health.NewStatus(t0)

	c := Config{
		Store:    e.store,
		Settings: nil, // filled below, after the file config is final
		Status:   e.status,
		Live:     e.feed,
		Stats:    func() Stats { return e.stats },
		Capture: Capture{
			Device: "hw:EM01,0", Channel: 0, Gain: "no capture control",
			Calibration: "none", SensitivityDBFS: -13,
		},
		Started: t0.Add(-time.Hour),
		LogDir:  file.LogDir,
		LogName: "stompwatch.log",
		ClipDir: file.ClipDir,
		Log:     quietLog(),
		Now:     func() time.Time { return t0 },
	}
	for _, fn := range change {
		fn(&c, &file)
	}
	if e.live, err = settings.New(file, nil); err != nil {
		t.Fatal(err)
	}
	c.Settings = e.live

	if e.srv, err = New(c); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Quiet hours are local time. The tests state their data in UTC, so the
	// server reads the clock in UTC too.
	e.srv.loc = time.UTC
	return e
}

// do sends a request through the whole handler, middleware included.
func (e *env) do(method, target string, body any, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, r)
	if body != nil {
		// The dashboard's client says so on every body it sends.
		req.Header.Set("Content-Type", "application/json")
	}
	asOwner(req)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, req)
	return w
}

// testLogin is the login a request carries when the test names none, as
// `tailscale serve` sets it on every request it forwards. An /api request
// with no login at all is refused, and the tests that check that send
// their headers with send.
const testLogin = "owner@example.com"

// asOwner sets the login header the default auth_mode reads.
func asOwner(req *http.Request) { req.Header.Set("Tailscale-User-Login", testLogin) }

func (e *env) get(target string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, target, nil, headers...)
}

// getJSON sends a GET, insists on 200, and decodes the body.
func (e *env) getJSON(target string, headers ...string) map[string]any {
	e.t.Helper()
	w := e.get(target, headers...)
	if w.Code != 200 {
		e.t.Fatalf("GET %s = %d, want 200: %s", target, w.Code, w.Body.String())
	}
	return decode(e.t, w)
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("the body is not a JSON object: %v: %s", err, w.Body.String())
	}
	return m
}

// wantError insists on a status and that the message names a word.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, names string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d: %s", w.Code, status, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !hasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}
	var m struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("the error body is not JSON: %v: %s", err, w.Body.String())
	}
	if m.Error == "" {
		t.Fatalf("the error body has no message: %s", w.Body.String())
	}
	if names != "" && !contains(m.Error, names) {
		t.Errorf("the message %q does not name %q", m.Error, names)
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- seeding ---

// bin makes a one-second measurement.
func bin(sec int, laeq, lamax, baseline float64) meter.Bin {
	return meter.Bin{
		Start: t0.Add(time.Duration(sec) * time.Second), Samples: 48000,
		LAeq: laeq, LAmax: lamax, LowBand: laeq - 5, HighBand: laeq - 20, Baseline: baseline,
	}
}

func (e *env) addBins(bins ...meter.Bin) {
	e.t.Helper()
	if _, err := e.store.InsertBins(e.t.Context(), bins); err != nil {
		e.t.Fatal(err)
	}
}

// newEvent builds an event that starts at t0 plus offset, ready for
// insertEvent. A caller that needs HasContext, JumpDB or RiseDB set builds
// one of these and changes those fields before inserting it.
func (e *env) newEvent(offset time.Duration, dur time.Duration, lamax float64, class detect.Class) detect.Event {
	start := t0.Add(offset)
	return detect.Event{
		Start: start, End: start.Add(dur),
		LAeq: lamax - 5, LAmax: lamax, BaselineAtTrigger: 33,
		LowBand: lamax - 10, HighBand: lamax - 25, LowHighRatioDB: 15,
		Class: class, Confidence: 0.8, Envelope: []float64{0.25, 0.5, 1},
	}
}

// insertEvent stores an event through the store, the same path the collector
// uses, and returns its id.
func (e *env) insertEvent(ev detect.Event) int64 {
	e.t.Helper()
	id, err := e.store.InsertEvent(e.t.Context(), ev, ev.Start)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// addEvent stores an event that starts at t0 plus offset.
func (e *env) addEvent(offset time.Duration, dur time.Duration, lamax float64, class detect.Class) int64 {
	e.t.Helper()
	return e.insertEvent(e.newEvent(offset, dur, lamax, class))
}

func (e *env) addReview(id int64, status, note, reviewer string) {
	e.t.Helper()
	err := e.store.SetReview(e.t.Context(), store.Review{
		EventID: id, Status: status, Note: note, Reviewer: reviewer, At: t0,
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// ms is the epoch millisecond of t0 plus an offset, as a JSON number would
// carry it.
func ms(d time.Duration) int64 { return t0.Add(d).UnixMilli() }

// num reads a JSON number out of a decoded object.
func num(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("the response has no %q: %v", key, m)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%q is %T (%v), want a number", key, v, v)
	}
	return f
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("the response has no %q: %v", key, m)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%q is %T (%v), want a string", key, v, v)
	}
	return s
}

// list reads a JSON array of objects.
func list(t *testing.T, m map[string]any, key string) []map[string]any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("the response has no %q: %v", key, m)
	}
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("%q is %T, want an array", key, v)
	}
	out := make([]map[string]any, len(arr))
	for i, it := range arr {
		o, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("%q[%d] is %T, want an object", key, i, it)
		}
		out[i] = o
	}
	return out
}

func object(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("the response has no %q: %v", key, m)
	}
	o, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%q is %T (%v), want an object", key, v, v)
	}
	return o
}
