package health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)

func TestStatusCheck(t *testing.T) {
	const (
		commitLimit = 10 * time.Minute // 2 x 5 min heartbeat interval
		audioLimit  = 10 * time.Second
	)
	tests := []struct {
		name       string
		audioAgo   time.Duration
		commitAgo  time.Duration
		wantErrHas string
	}{
		{"healthy", 3 * time.Second, 9 * time.Minute, ""},
		{"no bins written", 3 * time.Second, 11 * time.Minute, "no bins written for 11m0s"},
		{"no audio", 20 * time.Second, time.Minute, "no audio for 20s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := t0.Add(time.Hour)
			s := NewStatus(t0)
			s.AudioArrived(now.Add(-tc.audioAgo))
			s.BinsCommitted(now.Add(-tc.commitAgo))
			err := s.Check(now, commitLimit, audioLimit)
			if tc.wantErrHas == "" {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrHas) {
				t.Fatalf("Check = %v, want an error containing %q", err, tc.wantErrHas)
			}
		})
	}
}

// Before the first audio or commit, the start time counts as the last one,
// so a collector that never starts is caught once the limits pass.
func TestStatusCheckCountsFromStart(t *testing.T) {
	s := NewStatus(t0)
	if err := s.Check(t0.Add(5*time.Second), 10*time.Minute, 10*time.Second); err != nil {
		t.Errorf("5 s after start: %v, want nil", err)
	}
	if err := s.Check(t0.Add(11*time.Second), 10*time.Minute, 10*time.Second); err == nil {
		t.Error("11 s after start with no audio: nil, want an error")
	}
}

type recordingServer struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
	got  chan struct{}
	code int
}

func newServer(t *testing.T, code int) (*recordingServer, *httptest.Server) {
	rs := &recordingServer{got: make(chan struct{}, 100), code: code}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		rs.reqs = append(rs.reqs, r)
		rs.body = append(rs.body, string(b))
		rs.mu.Unlock()
		w.WriteHeader(rs.code)
		rs.got <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return rs, srv
}

func (rs *recordingServer) waitFor(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-rs.got:
		case <-time.After(5 * time.Second):
			t.Fatalf("server got %d of %d requests", i, n)
		}
	}
}

type events struct {
	mu     sync.Mutex
	pings  int
	skips  []error
	errs   []error
	notify chan struct{}
}

func newEvents() *events { return &events{notify: make(chan struct{}, 100)} }

func (e *events) config(url string, tick <-chan time.Time, check func(time.Time) error) HeartbeatConfig {
	return HeartbeatConfig{
		URL:     url,
		Tick:    tick,
		Check:   check,
		Now:     func() time.Time { return t0 },
		OnPing:  func() { e.mu.Lock(); e.pings++; e.mu.Unlock(); e.notify <- struct{}{} },
		OnSkip:  func(err error) { e.mu.Lock(); e.skips = append(e.skips, err); e.mu.Unlock(); e.notify <- struct{}{} },
		OnError: func(err error) { e.mu.Lock(); e.errs = append(e.errs, err); e.mu.Unlock(); e.notify <- struct{}{} },
	}
}

func (e *events) waitFor(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-e.notify:
		case <-time.After(5 * time.Second):
			t.Fatalf("got %d of %d heartbeat outcomes", i, n)
		}
	}
}

func runHeartbeat(t *testing.T, cfg HeartbeatConfig) context.CancelFunc {
	t.Helper()
	h, err := NewHeartbeat(cfg)
	if err != nil {
		t.Fatalf("NewHeartbeat: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestHeartbeatPingsOnEachTickWhileHealthy(t *testing.T) {
	rs, srv := newServer(t, http.StatusOK)
	tick := make(chan time.Time)
	ev := newEvents()
	runHeartbeat(t, ev.config(srv.URL+"/abc", tick, func(time.Time) error { return nil }))

	for i := 0; i < 3; i++ {
		tick <- t0
	}
	ev.waitFor(t, 3)
	rs.waitFor(t, 3)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, r := range rs.reqs {
		if r.URL.Path != "/abc" {
			t.Errorf("ping path = %s, want /abc", r.URL.Path)
		}
	}
	if ev.pings != 3 {
		t.Errorf("pings = %d, want 3", ev.pings)
	}
}

// SPEC.md section 6.8.1: while collection has stalled, stay silent. Silence is the
// alert.
func TestHeartbeatStaysSilentWhileUnhealthy(t *testing.T) {
	rs, srv := newServer(t, http.StatusOK)
	tick := make(chan time.Time)
	ev := newEvents()
	stalled := errors.New("no audio for 20s")
	runHeartbeat(t, ev.config(srv.URL, tick, func(time.Time) error { return stalled }))

	for i := 0; i < 3; i++ {
		tick <- t0
	}
	ev.waitFor(t, 3)
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if len(ev.skips) != 3 || !errors.Is(ev.skips[0], stalled) {
		t.Errorf("skips = %v, want 3 x %v", ev.skips, stalled)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.reqs) != 0 {
		t.Errorf("server got %d requests while unhealthy, want 0", len(rs.reqs))
	}
}

// A failed ping is counted and reported, and the next tick tries again.
func TestHeartbeatSurvivesServerErrors(t *testing.T) {
	_, srv := newServer(t, http.StatusInternalServerError)
	tick := make(chan time.Time)
	ev := newEvents()
	runHeartbeat(t, ev.config(srv.URL, tick, func(time.Time) error { return nil }))

	tick <- t0
	tick <- t0
	ev.waitFor(t, 2)
	ev.mu.Lock()
	defer ev.mu.Unlock()
	if len(ev.errs) != 2 || ev.pings != 0 {
		t.Errorf("errors %d, pings %d; want 2 and 0", len(ev.errs), ev.pings)
	}
}

// healthchecks.io takes a failure at <ping URL>/fail. The query string must
// stay on the URL.
func TestHeartbeatFailPostsReasonToFailURL(t *testing.T) {
	rs, srv := newServer(t, http.StatusOK)
	ev := newEvents()
	h, err := NewHeartbeat(ev.config(srv.URL+"/abc?rid=7", nil, func(time.Time) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Fail(context.Background(), "disk below floor"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	rs.waitFor(t, 1)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r := rs.reqs[0]
	if r.Method != http.MethodPost || r.URL.Path != "/abc/fail" || r.URL.RawQuery != "rid=7" ||
		!strings.Contains(rs.body[0], "disk below floor") {
		t.Errorf("request %s %s?%s body %q", r.Method, r.URL.Path, r.URL.RawQuery, rs.body[0])
	}
}

func TestHeartbeatWithEmptyURLIsDisabled(t *testing.T) {
	h, err := NewHeartbeat(HeartbeatConfig{URL: "", Check: func(time.Time) error { return nil }})
	if err != nil {
		t.Fatalf("NewHeartbeat with empty URL: %v", err)
	}
	done := make(chan struct{})
	go func() { h.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run with an empty URL did not return")
	}
	if err := h.Fail(context.Background(), "x"); err != nil {
		t.Errorf("Fail with an empty URL = %v, want nil", err)
	}
}

func TestNewHeartbeatRejectsBadURL(t *testing.T) {
	for _, u := range []string{"hc-ping.com/abc", "ftp://example.com/x", "https://"} {
		if _, err := NewHeartbeat(HeartbeatConfig{URL: u, Check: func(time.Time) error { return nil }}); err == nil {
			t.Errorf("NewHeartbeat(%q) returned no error", u)
		}
	}
}
