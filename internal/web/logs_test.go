package web

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeLog puts lines in the current log file, oldest first, the way slog
// writes them.
func (e *env) writeLog(lines ...string) {
	e.t.Helper()
	path := filepath.Join(e.logDir, "stompwatch.log")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// logLine writes one slog JSON record at t0 plus an offset.
func logLine(at time.Duration, level, msg, extra string) string {
	stamp := t0.Add(at).Format(time.RFC3339Nano)
	line := `{"time":"` + stamp + `","level":"` + level + `","msg":"` + msg + `"`
	if extra != "" {
		line += "," + extra
	}
	return line + "}"
}

func TestLogsAreNewestFirstWithTheirAttributes(t *testing.T) {
	e := newEnv(t)
	e.writeLog(
		logLine(0, "INFO", "started", ""),
		logLine(time.Minute, "WARN", "free space is low", `"free_mb":900`),
		logLine(2*time.Minute, "ERROR", "a write failed", ""),
	)

	m := e.getJSON("/api/logs")
	if got, ok := m["truncated"].(bool); !ok || got {
		t.Errorf("truncated = %v, want false", m["truncated"])
	}
	lines := list(t, m, "lines")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	if got := str(t, lines[0], "msg"); got != "a write failed" {
		t.Errorf("the newest line is %q, want \"a write failed\"", got)
	}
	if got := str(t, lines[0], "level"); got != "ERROR" {
		t.Errorf("level = %q, want \"ERROR\"", got)
	}
	mid := lines[1]
	if got := num(t, mid, "ts_ms"); int64(got) != ms(time.Minute) {
		t.Errorf("ts_ms = %v, want %d", got, ms(time.Minute))
	}
	attrs := object(t, mid, "attrs")
	if got := num(t, attrs, "free_mb"); got != 900 {
		t.Errorf("attrs free_mb = %v, want 900", got)
	}
}

func TestLogsFilterByLevelAndText(t *testing.T) {
	e := newEnv(t)
	e.writeLog(
		logLine(0, "INFO", "started", ""),
		logLine(time.Minute, "WARN", "free space is low", ""),
		logLine(2*time.Minute, "ERROR", "a write failed", ""),
	)

	if got := len(list(t, e.getJSON("/api/logs?level=ERROR"), "lines")); got != 1 {
		t.Errorf("level=ERROR returned %d lines, want 1", got)
	}
	if got := len(list(t, e.getJSON("/api/logs?level=ERROR&level=WARN"), "lines")); got != 2 {
		t.Errorf("two levels returned %d lines, want 2", got)
	}
	if got := len(list(t, e.getJSON("/api/logs?q=SPACE"), "lines")); got != 1 {
		t.Errorf("q=SPACE returned %d lines, want 1; the search is case-insensitive", got)
	}
	if got := len(list(t, e.getJSON("/api/logs?limit=2"), "lines")); got != 2 {
		t.Errorf("limit=2 returned %d lines", got)
	}
	from := strconv.FormatInt(ms(time.Minute), 10)
	if got := len(list(t, e.getJSON("/api/logs?from="+from), "lines")); got != 2 {
		t.Errorf("from the first minute returned %d lines, want 2", got)
	}
}

// A log that hides the thing that went wrong is worse than no log.
func TestALineThatIsNotJSONComesBackAsRaw(t *testing.T) {
	e := newEnv(t)
	e.writeLog(
		logLine(0, "INFO", "started", ""),
		"panic: runtime error: index out of range",
	)
	lines := list(t, e.getJSON("/api/logs"), "lines")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	if got := str(t, lines[0], "level"); got != "RAW" {
		t.Errorf("level = %q, want \"RAW\"", got)
	}
	if got := str(t, lines[0], "msg"); got != "panic: runtime error: index out of range" {
		t.Errorf("msg = %q, want the whole line", got)
	}
}

// A log directory that is empty is not a failure. The box may have just
// started.
func TestNoLogFileIsAnEmptyList(t *testing.T) {
	e := newEnv(t)
	m := e.getJSON("/api/logs")
	if got := len(list(t, m, "lines")); got != 0 {
		t.Errorf("lines = %d, want none", got)
	}
}

func TestLogsRejectBadParameters(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ query, names string }{
		{"?level=LOUD", "level"},
		{"?limit=1001", "limit"},
		{"?limit=0", "limit"},
		{"?search=x", "search"},
	} {
		wantError(t, e.get("/api/logs"+c.query), 400, c.names)
	}
}

// The log pages back the way the health log and the events list do: the
// offset counts lines already read, so a long night is reachable without
// narrowing the range.
func TestLogsPageBackWithAnOffset(t *testing.T) {
	e := newEnv(t)
	e.writeLog(
		logLine(0, "INFO", "first", ""),
		logLine(time.Minute, "INFO", "second", ""),
		logLine(2*time.Minute, "INFO", "third", ""),
		logLine(3*time.Minute, "INFO", "fourth", ""),
		logLine(4*time.Minute, "INFO", "fifth", ""),
	)

	first := list(t, e.getJSON("/api/logs?limit=2"), "lines")
	if len(first) != 2 || str(t, first[0], "msg") != "fifth" || str(t, first[1], "msg") != "fourth" {
		t.Fatalf("the first page is %v, want fifth then fourth", msgsOf(t, first))
	}
	second := list(t, e.getJSON("/api/logs?limit=2&offset=2"), "lines")
	if len(second) != 2 || str(t, second[0], "msg") != "third" || str(t, second[1], "msg") != "second" {
		t.Errorf("the second page is %v, want third then second", msgsOf(t, second))
	}
}

// "more" says whether an older page exists. Without it the view would have to
// offer an Older page that comes back empty, which reads as a fault.
func TestLogsSayWhetherAnOlderPageExists(t *testing.T) {
	e := newEnv(t)
	e.writeLog(
		logLine(0, "INFO", "first", ""),
		logLine(time.Minute, "INFO", "second", ""),
		logLine(2*time.Minute, "INFO", "third", ""),
	)

	page := e.getJSON("/api/logs?limit=2")
	if got, ok := page["more"].(bool); !ok || !got {
		t.Errorf("more = %v on the first page of three lines, want true", page["more"])
	}
	if got := len(list(t, page, "lines")); got != 2 {
		t.Errorf("lines = %d, want 2; the extra line read to answer more must not be returned", got)
	}
	last := e.getJSON("/api/logs?limit=2&offset=2")
	if got, ok := last["more"].(bool); !ok || got {
		t.Errorf("more = %v on the last page, want false", last["more"])
	}
}

func TestLogsRejectANegativeOffset(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.get("/api/logs?offset=-1"), 400, "offset")
}

// msgsOf reads the msg out of each line, for a failure message that says
// which lines came back.
func msgsOf(t *testing.T, lines []map[string]any) []string {
	t.Helper()
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = str(t, l, "msg")
	}
	return out
}
