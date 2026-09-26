package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/store"
)

// seedEvents stores four events with different classes, levels and lengths.
// The ids come back in the order they were made.
type seeded struct{ running, jumping, stomping, day int64 }

func seedEvents(e *env) seeded {
	var s seeded
	s.running = e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	s.jumping = e.addEvent(2*time.Second, 500*time.Millisecond, 70, detect.Jumping)
	s.stomping = e.addEvent(3*time.Second, 5*time.Second, 55, detect.Stomping)
	// 09:00, outside quiet hours.
	s.day = e.addEvent(6*time.Hour, time.Second, 65, detect.Unknown)
	return s
}

func ids(t *testing.T, m map[string]any) []int64 {
	t.Helper()
	rows := list(t, m, "events")
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = int64(num(t, r, "id"))
	}
	return out
}

func same(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEventListCarriesEveryFieldOfAnEvent(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	e.addReview(id, "verified", "kids again", "me@example.com")

	m := e.getJSON("/api/events")
	if got := num(t, m, "total"); got != 1 {
		t.Fatalf("total = %v, want 1", got)
	}
	ev := list(t, m, "events")[0]

	want := map[string]float64{
		"id":                  float64(id),
		"started_ms":          float64(ms(time.Second)),
		"ended_ms":            float64(ms(3 * time.Second)),
		"duration_ms":         2000,
		"laeq":                55,
		"lamax":               60,
		"baseline_at_trigger": 33,
		"low_energy":          50,
		"high_energy":         35,
		"low_high_ratio":      15,
		"confidence":          0.8,
	}
	for k, v := range want {
		if got := num(t, ev, k); got != v {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
	if got := str(t, ev, "class"); got != "running" {
		t.Errorf("class = %q, want \"running\"", got)
	}
	for _, k := range []string{"forced", "has_audio", "has_video"} {
		if got, ok := ev[k].(bool); !ok || got {
			t.Errorf("%s = %v, want false", k, ev[k])
		}
	}

	rev := object(t, ev, "review")
	if got := str(t, rev, "status"); got != "verified" {
		t.Errorf("review status = %q, want \"verified\"", got)
	}
	if got := str(t, rev, "note"); got != "kids again" {
		t.Errorf("review note = %q, want \"kids again\"", got)
	}
	if got := str(t, rev, "reviewer"); got != "me@example.com" {
		t.Errorf("reviewer = %q, want \"me@example.com\"", got)
	}
	if got := num(t, rev, "reviewed_ms"); int64(got) != ms(0) {
		t.Errorf("reviewed_ms = %v, want %d", got, ms(0))
	}
}

func TestAnUnreviewedEventHasNoReview(t *testing.T) {
	e := newEnv(t)
	e.addEvent(time.Second, time.Second, 60, detect.Running)
	ev := list(t, e.getJSON("/api/events"), "events")[0]
	if v, ok := ev["review"]; !ok || v != nil {
		t.Errorf("review = %v, want null", v)
	}
}

func TestEventListFilters(t *testing.T) {
	e := newEnv(t)
	s := seedEvents(e)
	e.addReview(s.running, "verified", "kids again", "me@example.com")
	e.addReview(s.jumping, "rejected", "a door", "me@example.com")

	cases := []struct {
		name, query string
		want        []int64
	}{
		{"newest first by default", "", []int64{s.day, s.stomping, s.jumping, s.running}},
		{"oldest first", "?order=asc", []int64{s.running, s.jumping, s.stomping, s.day}},
		{"loudest first", "?sort=lamax", []int64{s.jumping, s.day, s.running, s.stomping}},
		{"longest first", "?sort=duration_ms", []int64{s.stomping, s.running, s.day, s.jumping}},
		{"one class", "?class=jumping", []int64{s.jumping}},
		{"two classes", "?class=jumping&class=running&order=asc", []int64{s.running, s.jumping}},
		{"verified only", "?status=verified", []int64{s.running}},
		{"rejected only", "?status=rejected", []int64{s.jumping}},
		{"unreviewed only", "?status=none&order=asc", []int64{s.stomping, s.day}},
		{"loud enough", "?min_lamax=65&order=asc", []int64{s.jumping, s.day}},
		{"quiet enough", "?max_lamax=60&order=asc", []int64{s.running, s.stomping}},
		{"long enough", "?min_duration_ms=2000&order=asc", []int64{s.running, s.stomping}},
		{"in quiet hours", "?quiet_only=1&order=asc", []int64{s.running, s.jumping, s.stomping}},
		{"a note", "?q=KIDS", []int64{s.running}},
		{"a note that matches nothing", "?q=cat", nil},
		{"a time range", "?from=" + strconv.FormatInt(ms(2*time.Second), 10) +
			"&to=" + strconv.FormatInt(ms(3*time.Second), 10) + "&order=asc",
			[]int64{s.jumping, s.stomping}},
		{"one page", "?limit=2", []int64{s.day, s.stomping}},
		{"the next page", "?limit=2&offset=2", []int64{s.jumping, s.running}},
	}
	for _, c := range cases {
		m := e.getJSON("/api/events" + c.query)
		if got := ids(t, m); !same(got, c.want) {
			t.Errorf("%s (%q): ids = %v, want %v", c.name, c.query, got, c.want)
		}
	}
}

// total counts the matches, not the page. A paged list that reported its own
// length would hide the rest of the night.
func TestTotalIsTheCountBeforeTheLimit(t *testing.T) {
	e := newEnv(t)
	seedEvents(e)
	m := e.getJSON("/api/events?limit=1")
	if got := num(t, m, "total"); got != 4 {
		t.Errorf("total = %v, want 4", got)
	}
	if got := len(list(t, m, "events")); got != 1 {
		t.Errorf("the page holds %d events, want 1", got)
	}
}

// The class filter accepts the three new classes, and the event carries the
// jump and rise the detector measured for it.
func TestEventsCarryJumpAndRiseAndAcceptTheNewClasses(t *testing.T) {
	e := newEnv(t)
	ev := e.newEvent(time.Second, time.Second, 60, detect.Impact)
	ev.HasContext, ev.JumpDB, ev.RiseDB = true, 17.1, 13.3
	e.insertEvent(ev)

	rows := list(t, e.getJSON("/api/events?class=impact"), "events")
	if len(rows) != 1 {
		t.Fatalf("class=impact returned %d events, want 1", len(rows))
	}
	if got := num(t, rows[0], "jump_db"); got != 17.1 {
		t.Errorf("jump_db = %v, want 17.1", got)
	}
	if got := num(t, rows[0], "rise_db"); got != 13.3 {
		t.Errorf("rise_db = %v, want 13.3", got)
	}

	for _, class := range []string{"steady", "airborne"} {
		if got := e.get("/api/events?class=" + class).Code; got != 200 {
			t.Errorf("class=%s: status %d, want 200", class, got)
		}
	}
}

// Nothing measured jump and rise for an event with less than 10 s of levels
// before it. The response must say so with null, never with 0.
func TestEventWithoutContextSaysNull(t *testing.T) {
	e := newEnv(t)
	ev := e.newEvent(time.Second, time.Second, 60, detect.Running)
	// HasContext defaults to false: nothing measured jump_db or rise_db.
	e.insertEvent(ev)

	row := list(t, e.getJSON("/api/events"), "events")[0]
	if v, ok := row["jump_db"]; !ok || v != nil {
		t.Errorf("jump_db = %v, want null: nothing measured it", v)
	}
	if v, ok := row["rise_db"]; !ok || v != nil {
		t.Errorf("rise_db = %v, want null: nothing measured it", v)
	}
}

// A typo in a parameter must never come back as a filtered list that looks
// unfiltered.
func TestEventListRejectsBadParameters(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ query, names string }{
		{"?klass=running", "klass"},
		{"?class=shouting", "class"},
		{"?status=maybe", "status"},
		{"?sort=loudness", "sort"},
		{"?order=up", "order"},
		{"?limit=0", "limit"},
		{"?limit=501", "limit"},
		{"?limit=all", "limit"},
		{"?offset=-1", "offset"},
		{"?min_lamax=loud", "min_lamax"},
		{"?min_duration_ms=-5", "min_duration_ms"},
		{"?quiet_only=yes", "quiet_only"},
		{"?from=yesterday", "from"},
	}
	for _, c := range cases {
		w := e.get("/api/events" + c.query)
		wantError(t, w, 400, c.names)
	}
}

// addMuteWindow stores a window the owner says they can explain.
func (e *env) addMuteWindow(startMS, endMS int64, reason string) {
	e.t.Helper()
	_, err := e.store.AddMuteWindow(e.t.Context(), store.MuteWindow{
		StartMS: startMS, EndMS: endMS, Reason: reason,
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// A mute window never stops the collector measuring, detecting or recording.
// It marks the events the owner already explains, and the list can be
// narrowed to either side of the mark.
func TestMutedEventsAreMarkedAndCanBeFiltered(t *testing.T) {
	e := newEnv(t)
	s := seedEvents(e)
	// The stomping event runs from t0 + 3 s to t0 + 8 s, so this window
	// covers it and nothing else.
	e.addMuteWindow(ms(4*time.Second), ms(5*time.Second), "party")

	all := e.getJSON("/api/events?order=asc")
	if got := num(t, all, "total"); got != 4 {
		t.Fatalf("total = %v, want 4: a mute window hides nothing", got)
	}
	for _, ev := range list(t, all, "events") {
		want := int64(num(t, ev, "id")) == s.stomping
		got, ok := ev["muted"].(bool)
		if !ok {
			t.Fatalf("event %v has no muted flag: %v", ev["id"], ev)
		}
		if got != want {
			t.Errorf("event %v muted = %v, want %v", ev["id"], got, want)
		}
	}

	for _, c := range []struct {
		query string
		want  []int64
	}{
		{"?muted=1&order=asc", []int64{s.stomping}},
		{"?muted=0&order=asc", []int64{s.running, s.jumping, s.day}},
	} {
		m := e.getJSON("/api/events" + c.query)
		if got := ids(t, m); !same(got, c.want) {
			t.Errorf("%q: ids = %v, want %v", c.query, got, c.want)
		}
		if got := num(t, m, "total"); int(got) != len(c.want) {
			t.Errorf("%q: total = %v, want %d", c.query, got, len(c.want))
		}
	}

	// One event of the detail view says the same thing.
	one := e.getJSON("/api/events/" + strconv.FormatInt(s.stomping, 10))
	if got, ok := one["muted"].(bool); !ok || !got {
		t.Errorf("the detail view says muted = %v, want true", one["muted"])
	}

	wantError(t, e.get("/api/events?muted=yes"), 400, "muted")
}

// The cap is the cap. A limit above it is refused, not quietly trimmed.
func TestLimitOfFiveHundredIsAccepted(t *testing.T) {
	e := newEnv(t)
	if w := e.get("/api/events?limit=500"); w.Code != 200 {
		t.Errorf("limit=500 gave %d, want 200: %s", w.Code, w.Body.String())
	}
}

// --- one event ---

func TestEventDetailCarriesMediaEnvelopeAndSamples(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	for i := range 90 {
		e.addBins(bin(i-30, 40, 45, 30))
	}
	path := filepath.Join(e.clipDir, "clip.wav")
	if err := os.WriteFile(path, []byte("not really a wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.store.InsertMedia(t.Context(), store.Media{
		EventID: id, Kind: store.KindAudio, Path: path, Bytes: 16,
		Duration: 15 * time.Second, SHA256: "abc123", Truncated: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	m := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
	if got := num(t, m, "id"); int64(got) != id {
		t.Errorf("id = %v, want %d", got, id)
	}
	if got, ok := m["has_audio"].(bool); !ok || !got {
		t.Errorf("has_audio = %v, want true", m["has_audio"])
	}

	media := list(t, m, "media")
	if len(media) != 1 {
		t.Fatalf("media = %d entries, want 1", len(media))
	}
	if got := str(t, media[0], "kind"); got != "audio" {
		t.Errorf("kind = %q, want \"audio\"", got)
	}
	if got := num(t, media[0], "bytes"); got != 16 {
		t.Errorf("bytes = %v, want 16", got)
	}
	if got := num(t, media[0], "duration_ms"); got != 15000 {
		t.Errorf("duration_ms = %v, want 15000", got)
	}
	if got := str(t, media[0], "sha256"); got != "abc123" {
		t.Errorf("sha256 = %q, want \"abc123\"", got)
	}
	if got, ok := media[0]["missing"].(bool); !ok || got {
		t.Errorf("missing = %v, want false", media[0]["missing"])
	}
	// A response never says where a file lives.
	for k, v := range media[0] {
		if s, ok := v.(string); ok && (s == path || contains(s, "/")) {
			t.Errorf("media %q = %q, which names a place on disk", k, s)
		}
	}

	env := object(t, m, "envelope")
	if got := num(t, env, "rate_hz"); got != 100 {
		t.Errorf("rate_hz = %v, want 100", got)
	}
	if got := num(t, env, "start_ms"); int64(got) != ms(time.Second) {
		t.Errorf("start_ms = %v, want %d", got, ms(time.Second))
	}
	vals, ok := env["values"].([]any)
	if !ok || len(vals) != 3 {
		t.Fatalf("values = %v, want the three stored samples", env["values"])
	}
	if vals[0] != 0.25 || vals[2] != 1.0 {
		t.Errorf("values = %v, want [0.25 0.5 1]", vals)
	}

	// The seconds from 30 s before the start to 30 s after the end.
	samples := list(t, m, "samples")
	if len(samples) != 63 {
		t.Errorf("samples = %d, want 63: 30 s before the start at 1 s, the 2 s of the event, and 30 s after", len(samples))
	}
	if got := int64(num(t, samples[0], "t")); got != ms(-29*time.Second) {
		t.Errorf("the first sample is at %d, want %d", got, ms(-29*time.Second))
	}
}

// addTimedClip stores an audio clip for an event, with a real file behind it
// so the clip does not read as missing. startedMS is the epoch millisecond of
// the first sample, or 0 for a clip whose start was never recorded.
func (e *env) addTimedClip(id int64, startedMS int64, dur time.Duration) {
	e.t.Helper()
	path := filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")
	if err := os.WriteFile(path, []byte("not really a wav"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	m := store.Media{EventID: id, Kind: store.KindAudio, Path: path, Bytes: 16,
		Duration: dur, SHA256: "abc123"}
	if startedMS != 0 {
		m.Started = time.UnixMilli(startedMS).UTC()
	}
	if err := e.store.InsertMedia(e.t.Context(), m); err != nil {
		e.t.Fatal(err)
	}
}

// clipOf reads the one media entry of an event.
func clipOf(e *env, id int64) map[string]any {
	e.t.Helper()
	media := list(e.t, e.getJSON("/api/events/"+strconv.FormatInt(id, 10)), "media")
	if len(media) != 1 {
		e.t.Fatalf("event %d has %d media entries, want 1", id, len(media))
	}
	return media[0]
}

// The dashboard draws the waveform and the trace on one clock, so a clip must
// say when its first sample was taken. The pre-roll in force is 10 s, so a
// clip for an event starting at t0 + 1 s was asked to begin at t0 - 9 s.
func TestClipSaysWhenItStartsAndHowMuchHeadIsMissing(t *testing.T) {
	for _, c := range []struct {
		name        string
		clipStart   time.Duration
		wantMissing float64
	}{
		{"the whole pre-roll is there", -9 * time.Second, 0},
		{"62 ms of the pre-roll is missing", -9*time.Second + 62*time.Millisecond, 62},
		{"3 s of the pre-roll is missing", -6 * time.Second, 3000},
		{"the clip starts earlier than asked", -11 * time.Second, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			id := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
			e.addTimedClip(id, ms(c.clipStart), 15*time.Second)

			clip := clipOf(e, id)
			if got := int64(num(t, clip, "start_ms")); got != ms(c.clipStart) {
				t.Errorf("start_ms = %d, want %d", got, ms(c.clipStart))
			}
			if got := num(t, clip, "missing_head_ms"); got != c.wantMissing {
				t.Errorf("missing_head_ms = %v, want %v", got, c.wantMissing)
			}
		})
	}
}

// A clip written before the start was recorded must say "unknown", not the
// epoch and not a guess. A guessed start would draw the waveform in the
// wrong place and nobody would know.
func TestAClipWithNoRecordedStartSaysUnknown(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	e.addTimedClip(id, 0, 15*time.Second)

	clip := clipOf(e, id)
	for _, key := range []string{"start_ms", "missing_head_ms"} {
		v, ok := clip[key]
		if !ok {
			t.Fatalf("the clip has no %q at all: %v", key, clip)
		}
		if v != nil {
			t.Errorf("%s = %v, want null for a clip whose start was never recorded", key, v)
		}
	}
}

// A clip that has vanished is exactly the kind of silent problem that should
// surface loudly. It must be reported, not hidden behind a 200.
func TestMissingClipIsFlaggedAndRecordedOnce(t *testing.T) {
	e := newEnv(t)
	var kinds []string
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, _ string, _ time.Duration) {
		kinds = append(kinds, kind)
	}
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	err := e.store.InsertMedia(t.Context(), store.Media{
		EventID: id, Kind: store.KindAudio,
		Path:  filepath.Join(e.clipDir, "gone.wav"),
		Bytes: 16, Duration: time.Second, SHA256: "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}

	url := "/api/events/" + strconv.FormatInt(id, 10)
	m := e.getJSON(url)
	media := list(t, m, "media")
	if got, ok := media[0]["missing"].(bool); !ok || !got {
		t.Errorf("missing = %v, want true", media[0]["missing"])
	}
	e.getJSON(url) // a second look must not fill the health log
	if len(kinds) != 1 || kinds[0] != "media_missing" {
		t.Errorf("health records = %v, want one media_missing", kinds)
	}
}

// A number that names no event is not an empty list. An empty list would
// read as "this event has nothing", which is a different fact.
func TestUnknownEventIs404(t *testing.T) {
	e := newEnv(t)
	e.addEvent(time.Second, time.Second, 60, detect.Running)
	wantError(t, e.get("/api/events/9999"), 404, "")
	wantError(t, e.get("/api/events/0"), 400, "")
	wantError(t, e.get("/api/events/abc"), 400, "")
}

// A quiet-hours question is answered with one span per night. A range of
// millions of days once built millions of spans and the collector died of
// out-of-memory, so a range longer than 3660 days is refused before any
// span is built. The summary counts quiet-hour events, so it asks the same.
func TestAQuietHoursRangeLongerThanTenYearsIs400(t *testing.T) {
	e := newEnv(t)
	seedEvents(e)
	day := int64(24 * 60 * 60 * 1000)
	from := ms(0)
	for _, target := range []string{
		"/api/events?quiet_only=1&from=" + itoa(from) + "&to=" + itoa(from+3661*day),
		"/api/summary?from=" + itoa(from) + "&to=" + itoa(from+3661*day),
		"/api/events?quiet_only=1&from=1&to=900000000000000000",
		"/api/summary?from=1&to=900000000000000000",
	} {
		start := time.Now()
		w := e.get(target)
		wantError(t, w, http.StatusBadRequest, "3660 days")
		if took := time.Since(start); took > time.Second {
			t.Errorf("GET %s took %v, want a fast refusal", target, took)
		}
	}
	// Just inside the limit is still answered, and so is a long range with
	// no quiet-hours question in it.
	for _, target := range []string{
		"/api/events?quiet_only=1&from=" + itoa(from) + "&to=" + itoa(from+3659*day),
		"/api/summary?from=" + itoa(from) + "&to=" + itoa(from+3659*day),
		"/api/events?from=1&to=900000000000000000",
	} {
		if w := e.get(target); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200: %s", target, w.Code, w.Body.String())
		}
	}
}

// With one side of the range left open, the store fills it from the oldest
// or newest event. The other side can still be absurd, and the answer must
// still come back, fast and right.
func TestQuietOnlyWithOneOpenSideStillAnswers(t *testing.T) {
	e := newEnv(t)
	s := seedEvents(e)
	start := time.Now()
	m := e.getJSON("/api/events?quiet_only=1&order=asc&to=900000000000000000")
	if took := time.Since(start); took > time.Second {
		t.Errorf("the request took %v, want well under a second", took)
	}
	if got, want := ids(t, m), []int64{s.running, s.jumping, s.stomping}; !same(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}
