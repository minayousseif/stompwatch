package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// at is the JSON time slog writes: RFC 3339 with nanoseconds.
func at(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func record(ms int64, level, msg string) string {
	return fmt.Sprintf(`{"time":%q,"level":%q,"msg":%q}`+"\n", at(ms), level, msg)
}

// write puts content in one log file, as the writer would have left it.
func write(t *testing.T, dir, file string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(strings.Join(lines, "")), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func msgs(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Msg
	}
	return out
}

// The newest line comes first, and the current file comes before the older
// ones.
func TestReadReturnsTheNewestLineFirstAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name+".2", record(1000, "INFO", "a"), record(2000, "INFO", "b"))
	write(t, dir, name+".1", record(3000, "INFO", "c"), record(4000, "INFO", "d"))
	write(t, dir, name, record(5000, "INFO", "e"), record(6000, "INFO", "f"))

	lines, truncated, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if truncated {
		t.Error("truncated is true; the whole log fits in the budget")
	}
	want := []string{"f", "e", "d", "c", "b", "a"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

// A gap in the numbering ends the scan: a file after the gap is not part of
// this log any more.
func TestReadStopsAtAMissingFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name, record(5000, "INFO", "e"))
	write(t, dir, name+".2", record(1000, "INFO", "a"))

	lines, _, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := msgs(lines); len(got) != 1 || got[0] != "e" {
		t.Errorf("messages = %v, want [e]", got)
	}
}

func TestReadParsesTheSlogFields(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name,
		`{"time":"2026-09-11T00:00:00.250Z","level":"WARN","msg":"free space is low","dir":"/data","free_mb":900}`+"\n")

	lines, _, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	l := lines[0]
	want := time.Date(2026, 9, 11, 0, 0, 0, 250e6, time.UTC).UnixMilli()
	if l.TSMS != want {
		t.Errorf("TSMS = %d, want %d", l.TSMS, want)
	}
	if l.Level != "WARN" || l.Msg != "free space is low" {
		t.Errorf("Level = %q, Msg = %q", l.Level, l.Msg)
	}
	if len(l.Attrs) != 2 {
		t.Fatalf("Attrs = %v, want dir and free_mb only", l.Attrs)
	}
	if l.Attrs["dir"] != "/data" {
		t.Errorf("Attrs[dir] = %v, want /data", l.Attrs["dir"])
	}
	if n, ok := l.Attrs["free_mb"].(float64); !ok || n != 900 {
		t.Errorf("Attrs[free_mb] = %v, want 900", l.Attrs["free_mb"])
	}
}

// A line that is not JSON is the one most likely to matter. Never drop it.
func TestReadKeepsAnUnparsableLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name,
		record(1000, "INFO", "before"),
		"panic: runtime error: index out of range\n",
		"{\"time\": broken\n",
		record(2000, "INFO", "after"),
	)
	lines, _, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %v", len(lines), msgs(lines))
	}
	for _, i := range []int{1, 2} {
		if lines[i].Level != "RAW" {
			t.Errorf("lines[%d].Level = %q, want RAW", i, lines[i].Level)
		}
		if lines[i].TSMS != 0 {
			t.Errorf("lines[%d].TSMS = %d, want 0", i, lines[i].TSMS)
		}
	}
	if lines[1].Msg != "{\"time\": broken" {
		t.Errorf("lines[1].Msg = %q, want the raw text without the newline", lines[1].Msg)
	}
	if lines[2].Msg != "panic: runtime error: index out of range" {
		t.Errorf("lines[2].Msg = %q", lines[2].Msg)
	}
}

// A line whose time is missing or unreadable is still returned, and a time
// filter must not hide it.
func TestReadKeepsALineWithNoTime(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name,
		`{"level":"ERROR","msg":"no time here"}`+"\n",
		`{"time":"half past three","level":"ERROR","msg":"bad time"}`+"\n",
		record(5000, "INFO", "normal"),
	)
	lines, _, err := Read(dir, name, Filter{FromMS: 4000, ToMS: 6000, Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"normal", "bad time", "no time here"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
	for _, l := range lines[1:] {
		if l.TSMS != 0 {
			t.Errorf("%q has TSMS %d, want 0", l.Msg, l.TSMS)
		}
		if l.Level != "ERROR" {
			t.Errorf("%q has level %q, want ERROR", l.Msg, l.Level)
		}
	}
}

// The box has no clock of its own, so it can write a line dated before 1970
// while it waits for the network time. That line must not cut the log view
// short when the caller asked for no start time.
func TestReadWithNoStartTimeKeepsALineFromBeforeTheEpoch(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name,
		`{"time":"1969-12-31T23:59:00Z","level":"WARN","msg":"the clock is not set"}`+"\n",
		record(2000, "INFO", "time is set"),
		record(3000, "INFO", "started"),
	)
	lines, _, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"started", "time is set", "the clock is not set"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if lines[2].TSMS != -60000 {
		t.Errorf("TSMS = %d, want -60000", lines[2].TSMS)
	}
}

func TestReadFilters(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name,
		record(1000, "DEBUG", "opening the device"),
		record(2000, "INFO", "started"),
		record(3000, "WARN", "free space is low"),
		record(4000, "ERROR", "write failed"),
		record(5000, "INFO", "stopped"),
	)
	tests := []struct {
		name string
		f    Filter
		want []string
	}{
		{"everything", Filter{Limit: 10}, []string{"stopped", "write failed", "free space is low", "started", "opening the device"}},
		{"one level", Filter{Limit: 10, Levels: []string{"WARN"}}, []string{"free space is low"}},
		{"two levels", Filter{Limit: 10, Levels: []string{"WARN", "ERROR"}}, []string{"write failed", "free space is low"}},
		{"from", Filter{Limit: 10, FromMS: 4000}, []string{"stopped", "write failed"}},
		{"to", Filter{Limit: 10, ToMS: 2000}, []string{"started", "opening the device"}},
		{"between", Filter{Limit: 10, FromMS: 2000, ToMS: 3000}, []string{"free space is low", "started"}},
		{"query", Filter{Limit: 10, Query: "SPACE"}, []string{"free space is low"}},
		{"query on the level", Filter{Limit: 10, Query: "debug"}, []string{"opening the device"}},
		{"limit", Filter{Limit: 2}, []string{"stopped", "write failed"}},
		{"limit below one", Filter{Limit: 0}, []string{"stopped"}},
		{"nothing matches", Filter{Limit: 10, Query: "elephant"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lines, _, err := Read(dir, name, tc.f)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got := msgs(lines); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("messages = %v, want %v", got, tc.want)
			}
		})
	}
}

// Stopping at the byte budget must be reported, or the dashboard would show
// a short list as if it were the whole log.
func TestReadReportsWhenItRunsOutOfBudget(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 4000; i++ {
		lines = append(lines, record(int64(1000+i), "INFO", fmt.Sprintf("message %04d", i)))
	}
	write(t, dir, name, lines...)
	if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || fi.Size() < 200<<10 {
		t.Fatalf("the test file is too small to need more than one block: %v", err)
	}

	got, truncated, err := Read(dir, name, Filter{Limit: 5000, FromMS: 1000, MaxBytes: 100 << 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !truncated {
		t.Error("truncated is false, but the scan could not reach the start of the range")
	}
	if len(got) == 0 || len(got) >= 4000 {
		t.Errorf("got %d lines, want some but not all 4000", len(got))
	}
	if got[0].Msg != "message 3999" {
		t.Errorf("the first line is %q, want the newest", got[0].Msg)
	}

	// With the whole log inside the budget nothing is truncated.
	_, truncated, err = Read(dir, name, Filter{Limit: 5000, FromMS: 1000})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if truncated {
		t.Error("truncated is true, but the whole log fits in the default budget")
	}
}

// Reaching the limit is not truncation: the caller got what it asked for.
func TestReadDoesNotCallTheLimitTruncation(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 1000; i++ {
		lines = append(lines, record(int64(1000+i), "INFO", fmt.Sprintf("message %04d", i)))
	}
	write(t, dir, name, lines...)

	got, truncated, err := Read(dir, name, Filter{Limit: 3})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if truncated {
		t.Error("truncated is true after the limit was reached")
	}
	if len(got) != 3 || got[0].Msg != "message 0999" {
		t.Errorf("got %d lines starting at %q", len(got), got[0].Msg)
	}
}

// A big file must be read from the end in blocks, not loaded whole.
func TestReadDoesNotLoadTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const want = 4 << 20
	for n := 0; n < want; {
		rec := record(int64(1000+n), "INFO", fmt.Sprintf("message %07d", n))
		if _, err := f.WriteString(rec); err != nil {
			t.Fatalf("WriteString: %v", err)
		}
		n += len(rec)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	lines, _, err := Read(dir, name, Filter{Limit: 3})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if used := after.TotalAlloc - before.TotalAlloc; used > want/4 {
		t.Errorf("Read allocated %d bytes for a %d byte file; it must read blocks from the end",
			used, want)
	}
}

// No log file yet is not an error. The dashboard shows an empty list.
func TestReadWithNoFiles(t *testing.T) {
	lines, truncated, err := Read(t.TempDir(), name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 0 || truncated {
		t.Errorf("got %d lines, truncated %v", len(lines), truncated)
	}
	lines, _, err = Read(filepath.Join(t.TempDir(), "gone"), name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read with no directory: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("got %d lines", len(lines))
	}
}

// A file that does not end with a newline is the normal shape while the
// program is still writing.
func TestReadFileWithNoFinalNewline(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name, record(1000, "INFO", "first"), `{"time":"`+at(2000)+`","level":"INFO","msg":"last"}`)
	lines, _, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"last", "first"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

// An empty line carries nothing, so it is not worth a row in the log view.
func TestReadSkipsEmptyLines(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name, record(1000, "INFO", "first"), "\n", "\n", record(2000, "INFO", "last"))
	lines, _, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"last", "first"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

// A line longer than one read block must come back whole.
func TestReadHandlesALineLongerThanABlock(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("y", 200<<10)
	write(t, dir, name,
		record(1000, "INFO", "first"),
		`{"time":"`+at(2000)+`","level":"INFO","msg":"`+long+`"}`+"\n",
		record(3000, "INFO", "last"),
	)
	lines, _, err := Read(dir, name, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if len(lines[1].Msg) != len(long) {
		t.Errorf("the long message came back as %d bytes, want %d", len(lines[1].Msg), len(long))
	}
}

// The writer and the reader are two halves of one thing, so test them
// together on real files.
func TestWriteThenRead(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{Dir: dir, Name: name, MaxBytes: 400, Keep: 4})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < 40; i++ {
		level := "INFO"
		if i%10 == 0 {
			level = "ERROR"
		}
		if _, err := w.Write([]byte(record(int64(1000+i), level, fmt.Sprintf("message %02d", i)))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines, _, err := Read(dir, name, Filter{Limit: 100, Levels: []string{"ERROR"}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Rotation dropped the oldest files, so only the later errors survive.
	if len(lines) == 0 {
		t.Fatal("no ERROR lines came back")
	}
	if lines[0].Msg != "message 30" {
		t.Errorf("the first line is %q, want message 30", lines[0].Msg)
	}
	for _, l := range lines {
		if l.Level != "ERROR" {
			t.Errorf("%q has level %q", l.Msg, l.Level)
		}
	}
}

// An offset skips that many of the newest matching lines, so the view can
// page back through the log without widening its range.
func TestReadSkipsTheNewestLinesWhenAnOffsetIsGiven(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name, record(1000, "INFO", "a"), record(2000, "INFO", "b"),
		record(3000, "INFO", "c"), record(4000, "INFO", "d"),
		record(5000, "INFO", "e"), record(6000, "INFO", "f"))

	lines, _, err := Read(dir, name, Filter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"d", "c"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

// The offset counts matching lines, not lines read. A page of WARN lines is
// the same page whatever INFO lines sit between them.
func TestTheOffsetCountsOnlyTheLinesTheFilterKeeps(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name, record(1000, "WARN", "a"), record(2000, "INFO", "skip me"),
		record(3000, "WARN", "b"), record(4000, "INFO", "skip me too"),
		record(5000, "WARN", "c"))

	lines, _, err := Read(dir, name, Filter{Levels: []string{"WARN"}, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"b"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

// An offset past the end of the log is an empty page, not an error and not
// the last page over again.
func TestReadReturnsNothingWhenTheOffsetIsPastTheEnd(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name, record(1000, "INFO", "a"), record(2000, "INFO", "b"))

	lines, _, err := Read(dir, name, Filter{Limit: 5, Offset: 9})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("lines = %v, want none", msgs(lines))
	}
}

// A negative offset is the first page, the way a Limit below 1 is 1.
func TestANegativeOffsetIsTheFirstPage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, name, record(1000, "INFO", "a"), record(2000, "INFO", "b"))

	lines, _, err := Read(dir, name, Filter{Limit: 5, Offset: -3})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"b", "a"}
	if got := msgs(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("messages = %v, want %v", got, want)
	}
}
