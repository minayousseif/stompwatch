package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/meter"
)

// stream opens the live endpoint against a real listener, which a recorder
// cannot do: the answer never ends.
func (e *env) stream(t *testing.T) (*bufio.Reader, *http.Response, func()) {
	t.Helper()
	ts := httptest.NewServer(e.srv.Handler())
	req, err := http.NewRequest("GET", ts.URL+"/api/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	asOwner(req)
	ctx, cancel := context.WithCancel(t.Context())
	resp, err := ts.Client().Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		ts.Close()
		t.Fatal(err)
	}
	return bufio.NewReader(resp.Body), resp, func() {
		cancel()
		resp.Body.Close()
		ts.Close()
	}
}

// keepPublishing sends the same second over and over until the test stops
// it. The feed drops a bin that arrives before the browser has subscribed,
// which is the point of it, so a test must not send exactly once.
func keepPublishing(e *env, b meter.Bin) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.feed.Publish(b)
			time.Sleep(time.Millisecond)
		}
	}()
	return func() { close(stop); <-done }
}

// readFrame returns the next non-empty line of the stream.
func readFrame(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the live stream: %v", err)
		}
		if line = strings.TrimRight(line, "\r\n"); line != "" {
			return line
		}
	}
}

func decodeFrame(t *testing.T, line string) map[string]any {
	t.Helper()
	data, ok := strings.CutPrefix(line, "data: ")
	if !ok {
		t.Fatalf("the line %q is not a data line", line)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("the data is not JSON: %v: %s", err, data)
	}
	return m
}

func TestLiveStreamCarriesEachMeasuredSecond(t *testing.T) {
	e := newEnv(t)
	e.srv.staleAfter = time.Hour // out of the way of this test
	e.srv.keepalive = time.Hour
	br, resp, done := e.stream(t)
	defer done()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want \"text/event-stream\"", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want \"no-cache\" on the stream", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want \"no\"", got)
	}

	stop := keepPublishing(e, bin(0, 41.2, 55, 33))
	line := readFrame(t, br)
	stop()

	m := decodeFrame(t, line)
	if got := m["laeq"]; got != 41.2 {
		t.Errorf("laeq = %v, want 41.2", got)
	}
	if got := m["lamax"]; got != 55.0 {
		t.Errorf("lamax = %v, want 55", got)
	}
	if got := m["baseline"]; got != 33.0 {
		t.Errorf("baseline = %v, want 33", got)
	}
	if got := m["t"]; got != float64(ms(0)) {
		t.Errorf("t = %v, want %d", got, ms(0))
	}
	if got, ok := m["stale"].(bool); !ok || got {
		t.Errorf("stale = %v, want false on a fresh second", m["stale"])
	}
}

// The interface must be able to show that measurement has stopped, rather
// than freezing on an old number.
func TestLiveStreamSaysWhenMeasurementHasStopped(t *testing.T) {
	e := newEnv(t)
	e.srv.staleAfter = 20 * time.Millisecond
	e.srv.keepalive = time.Hour
	br, _, done := e.stream(t)
	defer done()

	m := decodeFrame(t, readFrame(t, br))
	if got, ok := m["stale"].(bool); !ok || !got {
		t.Fatalf("stale = %v after silence, want true", m["stale"])
	}
	// Nothing has been measured yet, so there is no level to show. A zero
	// would draw as silence.
	if m["laeq"] != nil {
		t.Errorf("laeq = %v before the first second, want null", m["laeq"])
	}
}

// A stale line repeats the last real reading, so the interface can gray out
// a number the owner can still read.
func TestAStaleLineKeepsTheLastReading(t *testing.T) {
	e := newEnv(t)
	e.srv.staleAfter = 30 * time.Millisecond
	e.srv.keepalive = time.Hour
	br, _, done := e.stream(t)
	defer done()

	stop := keepPublishing(e, bin(0, 41.2, 55, 33))
	first := decodeFrame(t, readFrame(t, br))
	stop()
	if got, ok := first["stale"].(bool); !ok || got {
		t.Fatalf("the first line is stale: %v", first)
	}

	// The next line comes when the silence has lasted long enough.
	second := decodeFrame(t, readFrame(t, br))
	if got, ok := second["stale"].(bool); !ok || !got {
		t.Fatalf("stale = %v after the measurement stopped, want true", second["stale"])
	}
	if got := second["laeq"]; got != 41.2 {
		t.Errorf("laeq = %v on the stale line, want the last reading 41.2", got)
	}
}

func TestLiveStreamSendsAKeepalive(t *testing.T) {
	e := newEnv(t)
	e.srv.staleAfter = time.Hour
	e.srv.keepalive = 20 * time.Millisecond
	br, _, done := e.stream(t)
	defer done()

	if line := readFrame(t, br); line != ":keepalive" {
		t.Errorf("the first line is %q, want \":keepalive\"", line)
	}
}

func TestLiveStreamRejectsAQueryItDoesNotKnow(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.get("/api/live?rate=10"), 400, "rate")
}
