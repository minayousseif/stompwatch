package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// QuietHours is the nightly span that matters most, in local time. A span
// that wraps past midnight is normal and is the usual case.
type QuietHours struct{ Start, End time.Duration } // since local midnight

// Span is one quiet period as two instants.
type Span struct{ From, To time.Time }

// maxSpans is the most spans Spans returns. A range of millions of days once
// built one span per day until the process ran out of memory. Ten years of
// nights fit under it; a caller that asks for more gets the first ones, and
// the web handlers refuse such a range before they ask.
const maxSpans = 3700

// Contains reports whether the wall clock of t is inside the quiet hours.
// It reads the clock rather than counting from midnight, so the answer stays
// right on a day that a daylight-saving change made shorter or longer.
func (q QuietHours) Contains(t time.Time) bool {
	if q.Start == q.End {
		return false
	}
	h, m, s := t.Clock()
	off := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
		time.Duration(s)*time.Second + time.Duration(t.Nanosecond())
	if q.Start < q.End {
		return off >= q.Start && off < q.End
	}
	return off >= q.Start || off < q.End
}

// Spans returns the quiet spans that overlap [from, to), clipped to that
// range, and at most maxSpans of them. It works in the location of from.
// Each day's boundary is built with time.Date, so a daylight-saving change
// shortens or lengthens that one night instead of shifting every later night.
func (q QuietHours) Spans(from, to time.Time) []Span {
	if q.Start == q.End || !from.Before(to) {
		return nil
	}
	loc := from.Location()
	to = to.In(loc)

	// Start a day early: last night's span can still be running at from.
	y, mo, d := from.In(loc).Date()
	var spans []Span
	for day := -1; ; day++ {
		start := dayTime(y, mo, d+day, q.Start, loc)
		if !start.Before(to) || len(spans) == maxSpans {
			return spans
		}
		endDay := day
		if q.Start > q.End {
			endDay++ // the span wraps past midnight into the next day
		}
		end := dayTime(y, mo, d+endDay, q.End, loc)
		if s, ok := narrow(start, end, from, to); ok {
			spans = append(spans, s)
		}
	}
}

// dayTime is the instant at which a wall clock reads off on one calendar day.
// time.Date normalizes a day number outside the month, so d+1 is safe, and it
// gives the wall clock its real offset from UTC on that day.
func dayTime(y int, mo time.Month, d int, off time.Duration, loc *time.Location) time.Time {
	h := int(off / time.Hour)
	m := int(off % time.Hour / time.Minute)
	s := int(off % time.Minute / time.Second)
	return time.Date(y, mo, d, h, m, s, 0, loc)
}

// PauseWindows is the list of daily spans in which the collector records no
// event, clip or video. Empty means no pause. The parser refuses windows
// that overlap or touch, so each instant is in at most one window.
type PauseWindows []QuietHours

// Contains reports whether the wall clock of t is inside any window.
func (w PauseWindows) Contains(t time.Time) bool {
	_, ok := w.Window(t)
	return ok
}

// Window returns the first window whose hours contain t. The parser that
// builds a PauseWindows from the config file refuses any two windows that
// overlap or touch, so at most one window can ever contain a given instant:
// the first match is the only match.
func (w PauseWindows) Window(t time.Time) (QuietHours, bool) {
	for _, q := range w {
		if q.Contains(t) {
			return q, true
		}
	}
	return QuietHours{}, false
}

// Spans returns the pause spans that overlap [from, to), clipped to that
// range, sorted by start, with overlapping spans merged into one. Like
// QuietHours.Spans it returns at most maxSpans, the first ones.
func (w PauseWindows) Spans(from, to time.Time) []Span {
	var all []Span
	for _, q := range w {
		all = append(all, q.Spans(from, to)...)
	}
	slices.SortFunc(all, func(a, b Span) int { return a.From.Compare(b.From) })
	var merged []Span
	for _, s := range all {
		if n := len(merged); n > 0 && !s.From.After(merged[n-1].To) {
			if s.To.After(merged[n-1].To) {
				merged[n-1].To = s.To
			}
			continue
		}
		merged = append(merged, s)
	}
	if len(merged) > maxSpans {
		merged = merged[:maxSpans]
	}
	return merged
}

// String is the form the config file and the dashboard use:
// "22:00-07:00, 12:00-13:00", or "" for no pause.
func (w PauseWindows) String() string {
	parts := make([]string, len(w))
	for i, q := range w {
		parts[i] = windowString(q)
	}
	return strings.Join(parts, ", ")
}

// windowString renders one window as "HH:MM-HH:MM".
func windowString(q QuietHours) string {
	return clockString(q.Start) + "-" + clockString(q.End)
}

// clockString renders an offset from midnight as a 24-hour clock reading.
func clockString(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// narrow narrows a span to [from, to) and reports whether anything is left.
func narrow(start, end, from, to time.Time) (Span, bool) {
	if start.Before(from) {
		start = from
	}
	if end.After(to) {
		end = to
	}
	if !start.Before(end) {
		return Span{}, false
	}
	return Span{From: start, To: end}, true
}
