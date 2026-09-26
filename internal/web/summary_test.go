package web

import (
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
)

// seedTenSeconds stores ten seconds at 40 dB with baselines 30 to 39, so the
// energy mean is 40 and the median baseline is 34.5.
func seedTenSeconds(e *env) {
	for i := range 10 {
		e.addBins(bin(i, 40, 45+float64(i), 30+float64(i)))
	}
}

func summaryURL(from, to time.Duration) string {
	return "/api/summary?from=" + strconv.FormatInt(ms(from), 10) +
		"&to=" + strconv.FormatInt(ms(to), 10)
}

// A mute window changes nothing about the measurement. It keeps the events
// the owner explains out of the headline, and says how many there were, so
// the number is visible rather than silently dropped.
func TestSummaryLeavesMutedEventsOutOfTheHeadline(t *testing.T) {
	e := newEnv(t)
	seedTenSeconds(e)
	kept := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	loud := e.addEvent(5*time.Second, time.Second, 70, detect.Jumping)
	e.addReview(loud, "verified", "our own party", "me@example.com")
	e.addMuteWindow(ms(5*time.Second), ms(6*time.Second), "party")

	m := e.getJSON(summaryURL(0, 9*time.Second))
	for key, want := range map[string]float64{
		"events": 1, "muted": 1, "reviewed": 0, "verified": 0, "quiet_hour_events": 1,
	} {
		if got := num(t, m, key); got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	// The loudest event of the night is the muted one, so the headline names
	// the loudest of the rest.
	loudest := object(t, m, "loudest")
	if got := int64(num(t, loudest, "id")); got != kept {
		t.Errorf("loudest id = %d, want %d", got, kept)
	}
}

func TestSummaryCountsAndMeasuresTheRange(t *testing.T) {
	e := newEnv(t)
	seedTenSeconds(e)
	quiet := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	loudest := e.addEvent(2*time.Second, time.Second, 70, detect.Jumping)
	e.addEvent(3*time.Second, time.Second, 65, detect.Stomping)
	e.addReview(quiet, "verified", "", "me@example.com")
	e.addReview(loudest, "rejected", "", "me@example.com")

	m := e.getJSON(summaryURL(0, 9*time.Second))

	if got := num(t, m, "events"); got != 3 {
		t.Errorf("events = %v, want 3", got)
	}
	if got := num(t, m, "reviewed"); got != 2 {
		t.Errorf("reviewed = %v, want 2", got)
	}
	if got := num(t, m, "verified"); got != 1 {
		t.Errorf("verified = %v, want 1", got)
	}
	// 03:00 UTC is inside the default quiet hours of 22:00 to 07:00, and all
	// three events start there.
	if got := num(t, m, "quiet_hour_events"); got != 3 {
		t.Errorf("quiet_hour_events = %v, want 3", got)
	}
	// Ten seconds at 40 dB have an energy mean of 40 dB.
	if got := num(t, m, "laeq"); got != 40 {
		t.Errorf("laeq = %v, want 40", got)
	}
	// Baselines 30 to 39: the median is the mean of 34 and 35.
	if got := num(t, m, "baseline"); got != 34.5 {
		t.Errorf("baseline = %v, want 34.5", got)
	}
	if got := num(t, m, "from"); int64(got) != ms(0) {
		t.Errorf("from = %v, want %d", got, ms(0))
	}

	l := object(t, m, "loudest")
	if got := num(t, l, "id"); int64(got) != loudest {
		t.Errorf("loudest id = %v, want %d", got, loudest)
	}
	if got := num(t, l, "lamax"); got != 70 {
		t.Errorf("loudest lamax = %v, want 70", got)
	}
	if got := num(t, l, "started_ms"); int64(got) != ms(2*time.Second) {
		t.Errorf("loudest started_ms = %v, want %d", got, ms(2*time.Second))
	}
}

// An event that starts outside quiet hours must not be counted as one that
// did. 09:00 is after the quiet hours end at 07:00.
func TestSummaryCountsOnlyQuietHourEventsAsQuiet(t *testing.T) {
	e := newEnv(t)
	e.addEvent(time.Second, time.Second, 60, detect.Running) // 03:00, quiet
	e.addEvent(6*time.Hour, time.Second, 61, detect.Running) // 09:00, not quiet

	m := e.getJSON(summaryURL(0, 7*time.Hour))
	if got := num(t, m, "events"); got != 2 {
		t.Fatalf("events = %v, want 2", got)
	}
	if got := num(t, m, "quiet_hour_events"); got != 1 {
		t.Errorf("quiet_hour_events = %v, want 1", got)
	}
}

// No measurements means no level. A zero would draw as silence, which is a
// claim nobody made.
func TestSummaryOfAnEmptyRangeHasNoLevel(t *testing.T) {
	e := newEnv(t)
	m := e.getJSON(summaryURL(0, time.Minute))

	if got := num(t, m, "events"); got != 0 {
		t.Errorf("events = %v, want 0", got)
	}
	if m["loudest"] != nil {
		t.Errorf("loudest = %v, want null", m["loudest"])
	}
	if m["laeq"] != nil {
		t.Errorf("laeq = %v, want null", m["laeq"])
	}
	if m["baseline"] != nil {
		t.Errorf("baseline = %v, want null", m["baseline"])
	}
}

func TestSummaryRejectsBadRanges(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ url, names string }{
		{"/api/summary", "from"},
		{"/api/summary?from=0", "to"},
		{"/api/summary?from=abc&to=1", "from"},
		{"/api/summary?from=2000&to=1000", "to"},
		{"/api/summary?from=0&to=1&res=1s", "res"},
	}
	for _, c := range cases {
		w := e.get(c.url)
		wantError(t, w, 400, c.names)
	}
}
