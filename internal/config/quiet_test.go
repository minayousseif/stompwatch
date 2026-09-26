package config

import (
	"testing"
	"time"
)

func nyc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone database: %v", err)
	}
	return loc
}

// The usual quiet span wraps past midnight, so Contains must answer for both
// the evening part and the morning part.
func TestQuietHoursContainsAcrossMidnight(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	tests := []struct {
		hour, min int
		want      bool
	}{
		{21, 59, false},
		{22, 0, true},
		{23, 30, true},
		{0, 0, true},
		{6, 59, true},
		{7, 0, false},
		{7, 1, false},
		{12, 0, false},
	}
	for _, tc := range tests {
		at := time.Date(2026, 5, 14, tc.hour, tc.min, 0, 0, loc)
		if got := q.Contains(at); got != tc.want {
			t.Errorf("Contains(%02d:%02d) = %v, want %v", tc.hour, tc.min, got, tc.want)
		}
	}
}

// A span that does not wrap is the daytime case, for example a rule about
// working hours.
func TestQuietHoursContainsWithinOneDay(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 9 * time.Hour, End: 17 * time.Hour}
	tests := []struct {
		hour int
		want bool
	}{
		{8, false},
		{9, true},
		{16, true},
		{17, false},
		{23, false},
		{2, false},
	}
	for _, tc := range tests {
		at := time.Date(2026, 5, 14, tc.hour, 0, 0, 0, loc)
		if got := q.Contains(at); got != tc.want {
			t.Errorf("Contains(%02d:00) = %v, want %v", tc.hour, got, tc.want)
		}
	}
}

// On the day the clocks go forward, 07:30 is only six and a half hours after
// midnight. Contains must read the clock, because the owner's quiet hours end
// when the clock says 07:00, not when nine hours have passed.
func TestQuietHoursContainsOnTheDayTheClocksChange(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	// In 2026 the clocks in New York go forward on 8 March at 02:00.
	tests := []struct {
		hour, min int
		want      bool
	}{
		{1, 30, true},  // still the small hours
		{6, 59, true},  // the last minute of quiet hours
		{7, 30, false}, // six and a half hours after midnight, but past 07:00
		{8, 0, false},
	}
	for _, tc := range tests {
		at := time.Date(2026, 3, 8, tc.hour, tc.min, 0, 0, loc)
		if got := q.Contains(at); got != tc.want {
			t.Errorf("Contains(8 March %02d:%02d) = %v, want %v", tc.hour, tc.min, got, tc.want)
		}
	}
	// The same on the day the clocks go back, when 07:30 is eight and a half
	// hours after midnight.
	for _, tc := range []struct {
		hour, min int
		want      bool
	}{{6, 30, true}, {7, 30, false}} {
		at := time.Date(2026, 11, 1, tc.hour, tc.min, 0, 0, loc)
		if got := q.Contains(at); got != tc.want {
			t.Errorf("Contains(1 November %02d:%02d) = %v, want %v", tc.hour, tc.min, got, tc.want)
		}
	}
}

// rfc3339 renders a span boundary so a test can state the expected wall clock
// and the expected offset from UTC as literal text.
func rfc3339(ts time.Time) string { return ts.Format(time.RFC3339) }

func TestQuietHoursSpansOverThreeNights(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	from := time.Date(2026, 5, 12, 12, 0, 0, 0, loc)
	to := time.Date(2026, 5, 15, 12, 0, 0, 0, loc)

	got := q.Spans(from, to)
	want := [][2]string{
		{"2026-05-12T22:00:00-04:00", "2026-05-13T07:00:00-04:00"},
		{"2026-05-13T22:00:00-04:00", "2026-05-14T07:00:00-04:00"},
		{"2026-05-14T22:00:00-04:00", "2026-05-15T07:00:00-04:00"},
	}
	if len(got) != len(want) {
		t.Fatalf("Spans returned %d spans, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if rfc3339(got[i].From) != w[0] || rfc3339(got[i].To) != w[1] {
			t.Errorf("span %d = %s..%s, want %s..%s", i, rfc3339(got[i].From), rfc3339(got[i].To), w[0], w[1])
		}
	}
}

// A span that is already running when the range opens, and one that is still
// running when it closes, are both clipped to the range.
func TestQuietHoursSpansClipToTheRange(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	from := time.Date(2026, 5, 13, 2, 0, 0, 0, loc)
	to := time.Date(2026, 5, 13, 23, 30, 0, 0, loc)

	got := q.Spans(from, to)
	want := [][2]string{
		{"2026-05-13T02:00:00-04:00", "2026-05-13T07:00:00-04:00"},
		{"2026-05-13T22:00:00-04:00", "2026-05-13T23:30:00-04:00"},
	}
	if len(got) != len(want) {
		t.Fatalf("Spans returned %d spans, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if rfc3339(got[i].From) != w[0] || rfc3339(got[i].To) != w[1] {
			t.Errorf("span %d = %s..%s, want %s..%s", i, rfc3339(got[i].From), rfc3339(got[i].To), w[0], w[1])
		}
	}
}

// A range that touches no quiet hour returns nothing.
func TestQuietHoursSpansEmptyRange(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	from := time.Date(2026, 5, 13, 8, 0, 0, 0, loc)
	to := time.Date(2026, 5, 13, 21, 0, 0, 0, loc)
	if got := q.Spans(from, to); len(got) != 0 {
		t.Fatalf("Spans = %v, want none", got)
	}
	if got := q.Spans(to, from); len(got) != 0 {
		t.Fatalf("Spans with to before from = %v, want none", got)
	}
}

// The night the clocks go forward is one hour shorter. Building each day's
// boundary with time.Date gets this right; adding 24 hours does not.
func TestQuietHoursSpansAcrossSpringForward(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	// In 2026 the clocks in New York go forward on 8 March at 02:00.
	from := time.Date(2026, 3, 6, 12, 0, 0, 0, loc)
	to := time.Date(2026, 3, 9, 12, 0, 0, 0, loc)

	got := q.Spans(from, to)
	want := []struct {
		from, to string
		hours    float64
	}{
		{"2026-03-06T22:00:00-05:00", "2026-03-07T07:00:00-05:00", 9},
		{"2026-03-07T22:00:00-05:00", "2026-03-08T07:00:00-04:00", 8},
		{"2026-03-08T22:00:00-04:00", "2026-03-09T07:00:00-04:00", 9},
	}
	if len(got) != len(want) {
		t.Fatalf("Spans returned %d spans, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if rfc3339(got[i].From) != w.from || rfc3339(got[i].To) != w.to {
			t.Errorf("span %d = %s..%s, want %s..%s", i, rfc3339(got[i].From), rfc3339(got[i].To), w.from, w.to)
		}
		if h := got[i].To.Sub(got[i].From).Hours(); h != w.hours {
			t.Errorf("span %d lasts %.2f hours, want %.2f", i, h, w.hours)
		}
	}
}

// The night the clocks go back is one hour longer.
func TestQuietHoursSpansAcrossFallBack(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	// In 2026 the clocks in New York go back on 1 November at 02:00.
	from := time.Date(2026, 10, 31, 12, 0, 0, 0, loc)
	to := time.Date(2026, 11, 1, 12, 0, 0, 0, loc)

	got := q.Spans(from, to)
	if len(got) != 1 {
		t.Fatalf("Spans returned %d spans, want 1: %v", len(got), got)
	}
	if rfc3339(got[0].From) != "2026-10-31T22:00:00-04:00" || rfc3339(got[0].To) != "2026-11-01T07:00:00-05:00" {
		t.Errorf("span = %s..%s, want 2026-10-31T22:00:00-04:00..2026-11-01T07:00:00-05:00",
			rfc3339(got[0].From), rfc3339(got[0].To))
	}
	if h := got[0].To.Sub(got[0].From).Hours(); h != 10 {
		t.Errorf("span lasts %.2f hours, want 10", h)
	}
}

// A quiet window inside one day must not be turned into a wrapping one.
func TestQuietHoursSpansWithinOneDay(t *testing.T) {
	loc := nyc(t)
	q := QuietHours{Start: 13 * time.Hour, End: 14*time.Hour + 30*time.Minute}
	from := time.Date(2026, 5, 12, 0, 0, 0, 0, loc)
	to := time.Date(2026, 5, 14, 0, 0, 0, 0, loc)

	got := q.Spans(from, to)
	want := [][2]string{
		{"2026-05-12T13:00:00-04:00", "2026-05-12T14:30:00-04:00"},
		{"2026-05-13T13:00:00-04:00", "2026-05-13T14:30:00-04:00"},
	}
	if len(got) != len(want) {
		t.Fatalf("Spans returned %d spans, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if rfc3339(got[i].From) != w[0] || rfc3339(got[i].To) != w[1] {
			t.Errorf("span %d = %s..%s, want %s..%s", i, rfc3339(got[i].From), rfc3339(got[i].To), w[0], w[1])
		}
	}
}

// Several windows can overlap, for example an evening window extended by a
// late one. Spans must come out sorted by start, with the overlap merged
// into a single span rather than counted twice.
func TestPauseWindowsSpansAreSortedAndMerged(t *testing.T) {
	loc := time.FixedZone("test", 0)
	w := PauseWindows{
		{Start: 22 * time.Hour, End: 23 * time.Hour},
		{Start: 12 * time.Hour, End: 13 * time.Hour},
		{Start: 22*time.Hour + 30*time.Minute, End: 23*time.Hour + 30*time.Minute}, // overlaps the first
	}
	from := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	spans := w.Spans(from, from.Add(24*time.Hour))
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (the overlapping pair merged): %v", len(spans), spans)
	}
	if !spans[0].From.Equal(from.Add(12*time.Hour)) || !spans[0].To.Equal(from.Add(13*time.Hour)) {
		t.Errorf("first span = %v, want 12:00-13:00", spans[0])
	}
	if !spans[1].From.Equal(from.Add(22*time.Hour)) || !spans[1].To.Equal(from.Add(23*time.Hour+30*time.Minute)) {
		t.Errorf("second span = %v, want 22:00-23:30", spans[1])
	}
}

// Window is what a caller uses to report which window is active, so it must
// find the one that actually contains the instant, not just say yes or no.
func TestPauseWindowsWindowFindsTheOneThatContainsTheInstant(t *testing.T) {
	loc := time.FixedZone("test", 0)
	w := PauseWindows{{Start: 22 * time.Hour, End: 7 * time.Hour}, {Start: 12 * time.Hour, End: 13 * time.Hour}}
	if q, ok := w.Window(time.Date(2026, 9, 14, 12, 30, 0, 0, loc)); !ok || q.End != 13*time.Hour {
		t.Errorf("Window(12:30) = %+v, %v; want the 12:00-13:00 window", q, ok)
	}
	if _, ok := w.Window(time.Date(2026, 9, 14, 15, 0, 0, 0, loc)); ok {
		t.Errorf("Window(15:00) found a window; want none")
	}
	if _, ok := (PauseWindows)(nil).Window(time.Date(2026, 9, 14, 12, 30, 0, 0, loc)); ok {
		t.Errorf("an empty list found a window")
	}
}

// A range of millions of days once built one span per day until the
// process ran out of memory. Spans now stops at a hard limit, whoever asks.
// The limit is written here as a number, not read from the package, so a
// change to it has to change this test too.
func TestQuietHoursSpansStopAtAHardLimit(t *testing.T) {
	loc := time.FixedZone("test", 0)
	q := QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	from := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	to := from.Add(100_000 * 24 * time.Hour) // about 270 years

	start := time.Now()
	spans := q.Spans(from, to)
	if took := time.Since(start); took > time.Second {
		t.Errorf("Spans took %v over 100,000 days, want well under a second", took)
	}
	if n := len(spans); n < 3600 || n > 3700 {
		t.Fatalf("got %d spans over 100,000 days, want the limit of 3700 (and not a short list)", n)
	}
	// The spans it keeps are the first ones, so what it returns is still true.
	if !spans[0].From.Equal(from) || !spans[0].To.Equal(from.Add(7*time.Hour)) {
		t.Errorf("first span = %v, want 00:00-07:00 on the first day", spans[0])
	}
	if !spans[1].From.Equal(from.Add(22 * time.Hour)) {
		t.Errorf("second span starts at %v, want 22:00 on the first day", spans[1].From)
	}
}

// PauseWindows.Spans gathers the spans of each window, so it must stop too.
func TestPauseWindowsSpansStopAtAHardLimit(t *testing.T) {
	loc := time.FixedZone("test", 0)
	w := PauseWindows{{Start: 22 * time.Hour, End: 7 * time.Hour}, {Start: 12 * time.Hour, End: 13 * time.Hour}}
	from := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	spans := w.Spans(from, from.Add(100_000*24*time.Hour))
	if n := len(spans); n < 3600 || n > 3700 {
		t.Fatalf("got %d spans over 100,000 days, want the limit of 3700", n)
	}
}
