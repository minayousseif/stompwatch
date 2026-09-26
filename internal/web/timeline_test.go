package web

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/store"
)

func timelineURL(from, to time.Duration, res string) string {
	u := "/api/timeline?from=" + strconv.FormatInt(ms(from), 10) +
		"&to=" + strconv.FormatInt(ms(to), 10)
	if res != "" {
		u += "&res=" + res
	}
	return u
}

func TestTimelineSecondsCarryTheBaseline(t *testing.T) {
	e := newEnv(t)
	e.addBins(bin(0, 40, 45, 30), bin(1, 41, 46, 31), bin(2, 42, 47, 32))

	m := e.getJSON(timelineURL(0, 2*time.Second, "1s"))
	if got := str(t, m, "resolution"); got != "1s" {
		t.Fatalf("resolution = %q, want \"1s\"", got)
	}
	pts := list(t, m, "points")
	if len(pts) != 3 {
		t.Fatalf("points = %d, want 3", len(pts))
	}
	if got := num(t, pts[0], "t"); int64(got) != ms(0) {
		t.Errorf("point 0 t = %v, want %d", got, ms(0))
	}
	if got := num(t, pts[0], "laeq"); got != 40 {
		t.Errorf("point 0 laeq = %v, want 40", got)
	}
	if got := num(t, pts[2], "lamax"); got != 47 {
		t.Errorf("point 2 lamax = %v, want 47", got)
	}
	if got := num(t, pts[1], "baseline"); got != 31 {
		t.Errorf("point 1 baseline = %v, want 31", got)
	}
}

// A minute row is an energy mean and a peak. It keeps no baseline, and
// inventing one would be a measurement nobody made.
func TestTimelineMinutesAreTheRollupAndHaveNoBaseline(t *testing.T) {
	e := newEnv(t)
	e.addBins(bin(0, 40, 50, 30), bin(1, 40, 55, 31), bin(2, 40, 60, 32))

	m := e.getJSON(timelineURL(0, 12*time.Hour, "1m"))
	if got := str(t, m, "resolution"); got != "1m" {
		t.Fatalf("resolution = %q, want \"1m\"", got)
	}
	pts := list(t, m, "points")
	if len(pts) != 1 {
		t.Fatalf("points = %d, want 1 minute row", len(pts))
	}
	if got := num(t, pts[0], "laeq"); got != 40 {
		t.Errorf("laeq = %v, want 40, the energy mean of three seconds at 40 dB", got)
	}
	if got := num(t, pts[0], "lamax"); got != 60 {
		t.Errorf("lamax = %v, want 60, the loudest of the three", got)
	}
	if v, ok := pts[0]["baseline"]; !ok || v != nil {
		t.Errorf("baseline = %v, want null on a minute row", v)
	}
}

// A week of minutes is more points than one response may carry, so the
// server thins them into wider buckets instead of refusing.
func TestTimelineThinsAWeekIntoFiveMinuteBuckets(t *testing.T) {
	e := newEnv(t)
	e.addBins(bin(0, 40, 45, 30))

	m := e.getJSON(timelineURL(0, 7*24*time.Hour, ""))
	if got := str(t, m, "resolution"); got != "1m" {
		t.Errorf("resolution = %q, want \"1m\"", got)
	}
	// 10081 whole minutes: 1 and 2 minute buckets both leave more than 5000
	// points, and 5 minute buckets leave 2017.
	if got := num(t, m, "bucket_ms"); got != 300000 {
		t.Errorf("bucket_ms = %v, want 300000", got)
	}
	if pts := list(t, m, "points"); len(pts) != 1 {
		t.Errorf("points = %d, want the one bucket that holds the single minute", len(pts))
	}
}

// The bucket is the smallest one on the list that fits, so a view is never
// coarser than it has to be.
func TestTimelineUsesTheSmallestBucketThatFits(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		minutes  int
		bucketMS int64
	}{
		{5000, 60000},       // 5000 points is the cap, so minutes go one for one
		{5001, 120000},      // one more minute needs 2-minute buckets
		{10000, 120000},     // 5000 buckets of 2 minutes is still the cap
		{10001, 300000},     // one more needs 5-minute buckets
		{31 * 1440, 600000}, // 44641 minutes fit in 4465 buckets of 10 minutes
	}
	for _, c := range cases {
		span := time.Duration(c.minutes-1) * time.Minute
		m := e.getJSON(timelineURL(0, span, ""))
		if got := int64(num(t, m, "bucket_ms")); got != c.bucketMS {
			t.Errorf("%d minutes gave bucket_ms %d, want %d", c.minutes, got, c.bucketMS)
		}
	}
}

// A range wider than a month is refused. Beyond it even the widest bucket
// stops being a useful answer to the question the owner asked.
func TestTimelineRefusesMoreThanThirtyOneDays(t *testing.T) {
	e := newEnv(t)
	w := e.get(timelineURL(0, 31*24*time.Hour+time.Millisecond, ""))
	wantError(t, w, 400, "31 days")
}

// Inside a bucket the level is the energy mean of the minutes weighted by
// the seconds behind each one. A minute cut short by a restart must not
// count as much as a full one.
func TestBucketWeightsTheEnergyMeanBySecondsPerMinute(t *testing.T) {
	e := newEnv(t)
	// Minute 0 holds three seconds at 40 dB; minute 1 holds one at 50 dB.
	e.addBins(bin(0, 40, 45, 30), bin(1, 40, 45, 30), bin(2, 40, 45, 30), bin(60, 50, 60, 30))

	// 6000 whole minutes need 2-minute buckets, and both minutes fall in the
	// first of them.
	m := e.getJSON(timelineURL(0, 5999*time.Minute, ""))
	if got := int64(num(t, m, "bucket_ms")); got != 120000 {
		t.Fatalf("bucket_ms = %d, want 120000", got)
	}
	pts := list(t, m, "points")
	if len(pts) != 1 {
		t.Fatalf("points = %d, want one bucket holding both minutes: %v", len(pts), pts)
	}
	// 10*log10((3*10^4 + 1*10^5) / 4) = 45.1188 dB. Weighting every minute
	// the same would give 10*log10((10^4 + 10^5) / 2) = 47.4036 dB.
	if got := num(t, pts[0], "laeq"); got < 45.1183 || got > 45.1193 {
		t.Errorf("laeq = %v, want 45.1188, the energy mean of 3 s at 40 dB and 1 s at 50 dB", got)
	}
	if got := num(t, pts[0], "lamax"); got != 60 {
		t.Errorf("lamax = %v, want 60, the loudest second in the bucket", got)
	}
	if v, ok := pts[0]["baseline"]; !ok || v != nil {
		t.Errorf("baseline = %v, want null on a bucket", v)
	}
	if got := int64(num(t, pts[0], "t")); got != ms(0) {
		t.Errorf("the bucket starts at %d, want %d", got, ms(0))
	}
}

// A straight line drawn across an hour when the microphone was unplugged is
// a claim nobody made.
func TestABucketWithNoMinutesIsLeftOut(t *testing.T) {
	e := newEnv(t)
	e.addBins(bin(0, 40, 45, 30), bin(100*60, 50, 55, 30))

	m := e.getJSON(timelineURL(0, 5999*time.Minute, ""))
	pts := list(t, m, "points")
	if len(pts) != 2 {
		t.Fatalf("points = %d, want only the two buckets that hold a measurement", len(pts))
	}
	if got := int64(num(t, pts[0], "t")); got != ms(0) {
		t.Errorf("the first bucket is at %d, want %d", got, ms(0))
	}
	if got := int64(num(t, pts[1], "t")); got != ms(100*time.Minute) {
		t.Errorf("the second bucket is at %d, want %d", got, ms(100*time.Minute))
	}
	for i, pt := range pts {
		if got := num(t, pt, "laeq"); got == 0 {
			t.Errorf("bucket %d reads 0 dB, which is silence nobody measured", i)
		}
	}
}

// The bucket width is part of the answer: a point every 5 minutes is a
// different fact from a point every minute.
func TestTimelineReportsTheBucketWidth(t *testing.T) {
	e := newEnv(t)
	if got := num(t, e.getJSON(timelineURL(0, time.Hour, "1s")), "bucket_ms"); got != 1000 {
		t.Errorf("bucket_ms for seconds = %v, want 1000", got)
	}
	if got := num(t, e.getJSON(timelineURL(0, 12*time.Hour, "1m")), "bucket_ms"); got != 60000 {
		t.Errorf("bucket_ms for unthinned minutes = %v, want 60000", got)
	}
}

// Asking for seconds over a wide range would read the whole second table.
func TestTimelineRefusesSecondsOverAWideRange(t *testing.T) {
	e := newEnv(t)
	w := e.get(timelineURL(0, 25*time.Hour, "1s"))
	wantError(t, w, 400, "1m")
}

func TestTimelineRejectsAnUnknownResolution(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.get(timelineURL(0, time.Hour, "1h")), 400, "res")
	wantError(t, e.get("/api/timeline?from=0&to=1&step=5"), 400, "step")
}

func TestTimelineCarriesEventsAndTheirStatus(t *testing.T) {
	e := newEnv(t)
	reviewed := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	e.addEvent(5*time.Second, time.Second, 66, detect.Jumping)
	e.addReview(reviewed, "verified", "kids again", "me@example.com")

	m := e.getJSON(timelineURL(0, time.Hour, ""))
	evs := list(t, m, "events")
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	byID := map[int64]map[string]any{}
	for _, ev := range evs {
		byID[int64(num(t, ev, "id"))] = ev
	}
	got := byID[reviewed]
	if got == nil {
		t.Fatalf("the reviewed event %d is not on the timeline", reviewed)
	}
	if s := str(t, got, "status"); s != "verified" {
		t.Errorf("status = %q, want \"verified\"", s)
	}
	if s := str(t, got, "class"); s != "running" {
		t.Errorf("class = %q, want \"running\"", s)
	}
	if v := num(t, got, "ended_ms"); int64(v) != ms(3*time.Second) {
		t.Errorf("ended_ms = %v, want %d", v, ms(3*time.Second))
	}
	// An event with no review row has no status, not a made-up one.
	for id, ev := range byID {
		if id == reviewed {
			continue
		}
		if s := str(t, ev, "status"); s != "" {
			t.Errorf("an unreviewed event has status %q, want the empty string", s)
		}
	}
}

// The quiet hours are ground truth on the timeline, so the owner can see at
// a glance whether a loud moment was at a time that matters.
func TestTimelineShadesQuietHours(t *testing.T) {
	e := newEnv(t)
	// t0 is 03:00 UTC. The default quiet hours run 22:00 to 07:00, so the
	// night that holds t0 ends at 07:00 and the next starts at 22:00.
	m := e.getJSON(timelineURL(0, 20*time.Hour, ""))
	spans := list(t, m, "quiet")
	if len(spans) != 2 {
		t.Fatalf("quiet spans = %d, want 2: %v", len(spans), spans)
	}
	// The first span is last night, clipped to the start of the range, and
	// it ends at 07:00.
	if got := int64(num(t, spans[0], "from")); got != ms(0) {
		t.Errorf("span 0 starts at %d, want the start of the range, %d", got, ms(0))
	}
	if got := int64(num(t, spans[0], "to")); got != ms(4*time.Hour) {
		t.Errorf("span 0 ends at %d, want 07:00, %d", got, ms(4*time.Hour))
	}
	// The second starts at 22:00 and is clipped to the end of the range.
	if got := int64(num(t, spans[1], "from")); got != ms(19*time.Hour) {
		t.Errorf("span 1 starts at %d, want 22:00, %d", got, ms(19*time.Hour))
	}
	if got := int64(num(t, spans[1], "to")); got != ms(20*time.Hour) {
		t.Errorf("span 1 ends at %d, want the end of the range, %d", got, ms(20*time.Hour))
	}
}

// A range with no night in it gets no shading, not one span of the whole day.
func TestTimelineHasNoQuietSpanInTheAfternoon(t *testing.T) {
	e := newEnv(t)
	// 09:00 to 13:00, well after the quiet hours end at 07:00.
	m := e.getJSON(timelineURL(6*time.Hour, 10*time.Hour, ""))
	if spans := list(t, m, "quiet"); len(spans) != 0 {
		t.Errorf("quiet spans = %v, want none", spans)
	}
}

// A whole night is more seconds than one response may carry. The server
// thins them the way it thins minutes, rather than sending the owner to the
// minute rollup, which keeps no baseline.
func TestSecondsAreThinnedIntoBuckets(t *testing.T) {
	e := newEnv(t)
	// Two seconds at different levels in the first bucket, one in the second.
	e.addBins(bin(0, 40, 49, 30), bin(1, 46, 47, 32), bin(2, 50, 60, 34))

	// 6000 whole seconds need 2-second buckets.
	m := e.getJSON(timelineURL(0, 5999*time.Second, "1s"))
	if got := str(t, m, "resolution"); got != "1s" {
		t.Fatalf("resolution = %q, want 1s", got)
	}
	if got := int64(num(t, m, "bucket_ms")); got != 2000 {
		t.Fatalf("bucket_ms = %d, want 2000", got)
	}
	pts := list(t, m, "points")
	if len(pts) != 2 {
		t.Fatalf("points = %d, want two buckets: %v", len(pts), pts)
	}
	// 10*log10((10^4 + 10^4.6) / 2) = 43.9629 dB. Averaging the dB values
	// instead would give 43.0000.
	if got := num(t, pts[0], "laeq"); math.Abs(got-43.9629) > 0.001 {
		t.Errorf("bucket 0 laeq = %.4f, want 43.9629", got)
	}
	// The loudest of the two, which is not the later of them.
	if got := num(t, pts[0], "lamax"); got != 49 {
		t.Errorf("bucket 0 lamax = %v, want 49", got)
	}
	// The baseline must survive the thinning. It is the line the trigger
	// level is measured from, and it is the most useful context on the chart.
	// 10*log10((10^3 + 10^3.2) / 2) = 31.1141 dB.
	if got := num(t, pts[0], "baseline"); math.Abs(got-31.1141) > 0.001 {
		t.Errorf("bucket 0 baseline = %.4f, want 31.1141", got)
	}
}

func TestOneSecondResolutionCoversAWholeDay(t *testing.T) {
	e := newEnv(t)
	if w := e.get(timelineURL(0, 24*time.Hour, "1s")); w.Code != 200 {
		t.Errorf("a 24-hour range at res=1s = %d, want 200: %s", w.Code, w.Body.String())
	}
	wantError(t, e.get(timelineURL(0, 24*time.Hour+time.Millisecond, "1s")), 400, "1m")
}

// The resolution must not change in the middle of a night. A night that
// starts at 18:00 crosses midnight before it is six hours old.
func TestAutoStaysOnSecondsForAWholeNight(t *testing.T) {
	e := newEnv(t)
	for _, span := range []time.Duration{6 * time.Hour, 7 * time.Hour, 18 * time.Hour, 24 * time.Hour} {
		m := e.getJSON(timelineURL(0, span, ""))
		if got := str(t, m, "resolution"); got != "1s" {
			t.Errorf("a %v range with res=auto gave resolution %q, want 1s", span, got)
		}
	}
	m := e.getJSON(timelineURL(0, 24*time.Hour+time.Millisecond, ""))
	if got := str(t, m, "resolution"); got != "1m" {
		t.Errorf("a range over a day gave resolution %q, want 1m", got)
	}
}

// A thinned bucket with no seconds behind it is left out, so a stretch when
// nothing was measured draws as a break rather than a straight line.
func TestAThinnedSecondBucketWithNoRowsIsLeftOut(t *testing.T) {
	e := newEnv(t)
	e.addBins(bin(0, 40, 45, 30), bin(5000, 40, 45, 30))

	pts := list(t, e.getJSON(timelineURL(0, 5999*time.Second, "1s")), "points")
	if len(pts) != 2 {
		t.Fatalf("points = %d, want one for each measured second and nothing between", len(pts))
	}
	if got := int64(num(t, pts[1], "t")); got != t0.Add(5000*time.Second).UnixMilli() {
		t.Errorf("the second bucket starts at %d, want %d", got, t0.Add(5000*time.Second).UnixMilli())
	}
}

// The stat strip can say how many events a mute window covers, but the
// trace drew them like any other until the marker carried the flag.
func TestTimelineMarkersSayWhichEventsAreMuted(t *testing.T) {
	e := newEnv(t)
	plain := e.addEvent(time.Second, time.Second, 60, detect.Running)
	covered := e.addEvent(10*time.Second, time.Second, 61, detect.Jumping)
	if _, err := e.store.AddMuteWindow(t.Context(), store.MuteWindow{
		StartMS: ms(9 * time.Second), EndMS: ms(12 * time.Second), Reason: "a party",
	}); err != nil {
		t.Fatal(err)
	}

	got := map[int64]bool{}
	for _, m := range list(t, e.getJSON(timelineURL(0, time.Hour, "")), "events") {
		got[int64(num(t, m, "id"))] = m["muted"] == true
	}
	if got[plain] {
		t.Errorf("event %d is outside every window but is marked muted", plain)
	}
	if !got[covered] {
		t.Errorf("event %d falls inside a window but is not marked muted", covered)
	}
}

// The range length was once worked out in nanoseconds, which wraps for a
// range of about 290 years or more. A wrapped length read as short, passed
// the 31-day limit, and asked for one quiet span per day of the whole range.
func TestAnAbsurdRangeDoesNotWrapPastTheLimit(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{
		"from=-9000000000000000000&to=9000000000000000000",
		"from=1&to=9223372036854775807",
	} {
		for _, path := range []string{"/api/timeline?", "/api/export/timeline.png?"} {
			start := time.Now()
			wantError(t, e.get(path+q), 400, "31 days")
			if took := time.Since(start); took > time.Second {
				t.Errorf("GET %s%s took %v, want a fast refusal", path, q, took)
			}
		}
	}
}
