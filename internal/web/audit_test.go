package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
)

// healthRow is one system_health row the server asked to write.
type healthRow struct{ kind, detail string }

// auditEnv is a server whose health rows and log lines the test can read.
func auditEnv(t *testing.T) (*env, *[]healthRow, *bytes.Buffer) {
	t.Helper()
	var logged bytes.Buffer
	e := newEnv(t, func(c *Config, _ *config.Config) {
		c.Log = slog.New(slog.NewTextHandler(&logged, nil))
	})
	var rows []healthRow
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, detail string, _ time.Duration) {
		rows = append(rows, healthRow{kind, detail})
	}
	return e, &rows, &logged
}

// wantDetail fails the test unless the one row has the kind and names each
// part.
func wantDetail(t *testing.T, rows []healthRow, kind string, parts ...string) {
	t.Helper()
	if len(rows) != 1 || rows[0].kind != kind {
		t.Fatalf("health rows = %v, want one %s row", rows, kind)
	}
	for _, p := range parts {
		if !strings.Contains(rows[0].detail, p) {
			t.Errorf("the %s detail %q does not name %q", kind, rows[0].detail, p)
		}
	}
}

var asAlex = []string{"Tailscale-User-Login", "alex@example.com"}

// A settings change used to log only how many keys it touched. The record
// now says who changed what, from which value to which, in the log and in a
// system_health row, which nothing can edit afterwards. The old values here
// are the defaults, written out by hand.
func TestASettingsChangeSaysWhoChangedWhat(t *testing.T) {
	e, rows, logged := auditEnv(t)

	w := e.do(http.MethodPut, "/api/settings",
		map[string]any{"threshold_db": "19", "quiet_start": "22:30", "min_duration_ms": "400"}, asAlex...)
	if w.Code != 200 {
		t.Fatalf("PUT settings = %d: %s", w.Code, w.Body.String())
	}
	wantDetail(t, *rows, "settings_changed",
		"alex@example.com", "threshold_db: 15 -> 19", "quiet_start: 22:00 -> 22:30")
	// min_duration_ms was sent with the value it already had, so nothing
	// about it changed and the record does not claim it did.
	if strings.Contains((*rows)[0].detail, "min_duration_ms") {
		t.Errorf("the detail %q names a setting that did not change", (*rows)[0].detail)
	}
	for _, want := range []string{
		"key=threshold_db old=15 new=19", "key=quiet_start old=22:00 new=22:30", "by=alex@example.com",
	} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log does not hold %q:\n%s", want, logged.String())
		}
	}

	// null goes back to the config file value, and that is a change too.
	*rows = nil
	w = e.do(http.MethodPut, "/api/settings", map[string]any{"threshold_db": nil}, asAlex...)
	if w.Code != 200 {
		t.Fatalf("PUT settings = %d: %s", w.Code, w.Body.String())
	}
	wantDetail(t, *rows, "settings_changed", "threshold_db: 19 -> 15")

	// A refused change changed nothing, so it records nothing.
	*rows = nil
	w = e.do(http.MethodPut, "/api/settings", map[string]any{"threshold_db": "banana"}, asAlex...)
	wantError(t, w, 400, "threshold_db")
	if len(*rows) != 0 {
		t.Errorf("a refused change wrote %v", *rows)
	}
}

// A mute window takes events out of the headline counts, so adding and
// removing one is recorded with who did it and which window it was.
func TestMuteWindowsSayWhoAddedAndRemovedThem(t *testing.T) {
	e, rows, _ := auditEnv(t)

	w := e.do(http.MethodPost, "/api/mute-windows",
		map[string]any{"start_ms": ms(0), "end_ms": ms(2 * time.Hour), "reason": "party"}, asAlex...)
	if w.Code != 201 {
		t.Fatalf("POST = %d: %s", w.Code, w.Body.String())
	}
	id := strconv.FormatInt(int64(num(t, decode(t, w), "id")), 10)
	// t0 is 03:00 UTC, and the tests read the clock in UTC.
	window := []string{"alex@example.com", "window " + id, "2026-09-11 03:00", "2026-09-11 05:00", "party"}
	wantDetail(t, *rows, "mute_window_added", window...)

	*rows = nil
	if w := e.do(http.MethodDelete, "/api/mute-windows/"+id, nil, asAlex...); w.Code != 204 {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}
	wantDetail(t, *rows, "mute_window_removed", window...)

	*rows = nil
	wantError(t, e.do(http.MethodDelete, "/api/mute-windows/"+id, nil, asAlex...), 404, "")
	if len(*rows) != 0 {
		t.Errorf("removing a window that is not there wrote %v", *rows)
	}
}

// The health list refuses a kind it does not know, so the new kinds must
// be known, or the System screen could not filter by them.
func TestTheHealthListKnowsTheAuditKinds(t *testing.T) {
	e := newEnv(t)
	for _, kind := range []string{"settings_changed", "mute_window_added", "mute_window_removed"} {
		if w := e.get("/api/health?kind=" + kind); w.Code != 200 {
			t.Errorf("GET /api/health?kind=%s = %d, want 200: %s", kind, w.Code, w.Body.String())
		}
	}
}
