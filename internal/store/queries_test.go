package store

import (
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/meter"
)

// sample makes a one-second bin with a level and a baseline of its own, so a
// test can state what the energy mean and the median must come to.
func sample(sec int, laeq, lamax, baseline float64) meter.Bin {
	return meter.Bin{
		Start: t0.Add(time.Duration(sec) * time.Second), Samples: 48000,
		LAeq: laeq, LAmax: lamax, LowBand: laeq - 5, HighBand: laeq - 20, Baseline: baseline,
	}
}

// event makes an event that starts at a given instant.
func event(start time.Time, dur time.Duration, lamax float64, class detect.Class) detect.Event {
	return detect.Event{
		Start: start, End: start.Add(dur),
		LAeq: lamax - 5, LAmax: lamax, BaselineAtTrigger: 35,
		LowBand: lamax - 10, HighBand: lamax - 25, LowHighRatioDB: 15,
		Class: class, Confidence: 0.5, Envelope: []float64{0.25, 0.5, 1},
	}
}

func mustInsertEvent(t *testing.T, s *Store, e detect.Event) int64 {
	t.Helper()
	id, err := s.InsertEvent(ctx, e, e.Start)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	return id
}

func ms(tm time.Time) int64 { return tm.UnixMilli() }

// --- Timeline and samples ---

func TestTimelineReadsSecondRows(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.InsertBins(ctx, []meter.Bin{
		sample(0, 40, 45, 30), sample(1, 41, 46, 31), sample(2, 42, 47, 32),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Timeline(ctx, ms(t0), ms(t0.Add(2*time.Second)), false)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Timeline returned %d points, want 3: %+v", len(got), got)
	}
	if got[0].TMS != ms(t0) || got[0].LAeq != 40 || got[0].LAmax != 45 {
		t.Errorf("point 0 = %+v", got[0])
	}
	if got[0].Baseline == nil || *got[0].Baseline != 30 {
		t.Errorf("point 0 baseline = %v, want 30", got[0].Baseline)
	}
	if got[2].TMS != ms(t0.Add(2*time.Second)) || got[2].LAeq != 42 {
		t.Errorf("point 2 = %+v", got[2])
	}
	// Points come back in time order.
	for i := 1; i < len(got); i++ {
		if got[i].TMS <= got[i-1].TMS {
			t.Fatalf("point %d at %d is not after point %d at %d", i, got[i].TMS, i-1, got[i-1].TMS)
		}
	}
}

// The bounds are milliseconds but the rows are seconds. A bound part way
// through a second must keep that second, not drop it.
func TestTimelineKeepsTheSecondThatABoundFallsInside(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{sample(0, 40, 45, 30), sample(1, 41, 46, 31), sample(2, 42, 47, 32)})

	got, err := s.Timeline(ctx, ms(t0)+500, ms(t0)+1500, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Timeline over 500 ms to 1500 ms returned %d points, want seconds 0 and 1: %+v", len(got), got)
	}
	if got[0].TMS != ms(t0) || got[1].TMS != ms(t0)+1000 {
		t.Errorf("points at %d and %d, want %d and %d", got[0].TMS, got[1].TMS, ms(t0), ms(t0)+1000)
	}
}

func TestTimelineReadsMinuteRollup(t *testing.T) {
	s, _ := openTemp(t)
	// 10*log10((10^6 + 10^7 + 10^8) / 3) = 75.682 dB for the first minute.
	s.InsertBins(ctx, []meter.Bin{
		sample(0, 60, 65, 30), sample(1, 70, 75, 31), sample(2, 80, 88, 32),
		sample(61, 50, 55, 33),
	})
	got, err := s.Timeline(ctx, ms(t0), ms(t0.Add(90*time.Second)), true)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Timeline returned %d minute points, want 2: %+v", len(got), got)
	}
	if got[0].TMS != ms(t0) || math.Abs(got[0].LAeq-75.682) > 0.001 || got[0].LAmax != 88 {
		t.Errorf("minute 0 = %+v, want t %d laeq 75.682 lamax 88", got[0], ms(t0))
	}
	// A minute row holds no baseline, so the field stays empty rather than
	// carrying a made-up number.
	if got[0].Baseline != nil {
		t.Errorf("minute 0 baseline = %v, want nil", *got[0].Baseline)
	}
	if got[1].TMS != ms(t0.Add(time.Minute)) || got[1].LAeq != 50 || got[1].LAmax != 55 {
		t.Errorf("minute 1 = %+v", got[1])
	}
}

// A bound part way through a minute must keep that minute.
func TestTimelineMinuteKeepsThePartialMinute(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{sample(0, 60, 65, 30), sample(61, 50, 55, 33)})
	got, err := s.Timeline(ctx, ms(t0)+30_000, ms(t0)+90_000, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Timeline returned %d minute points, want both minutes: %+v", len(got), got)
	}
}

// A thinned timeline weights each minute by the seconds behind it, so the
// query has to hand that count back.
func TestMinutePointsCarryTheSecondCount(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{
		sample(0, 60, 65, 30), sample(1, 70, 75, 31), sample(2, 80, 88, 32),
		sample(61, 50, 55, 33),
	})
	got, err := s.MinutePoints(ctx, ms(t0), ms(t0.Add(90*time.Second)))
	if err != nil {
		t.Fatalf("MinutePoints: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("MinutePoints returned %d rows, want 2: %+v", len(got), got)
	}
	if got[0].TMS != ms(t0) || math.Abs(got[0].LAeq-75.682) > 0.001 || got[0].LAmax != 88 {
		t.Errorf("minute 0 = %+v, want t %d laeq 75.682 lamax 88", got[0], ms(t0))
	}
	if got[0].N != 3 {
		t.Errorf("minute 0 n = %d, want 3 seconds", got[0].N)
	}
	if got[1].N != 1 {
		t.Errorf("minute 1 n = %d, want 1 second", got[1].N)
	}
}

func TestSamplesReadsSecondRows(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{sample(0, 40, 45, 30), sample(1, 41, 46, 31), sample(9, 49, 54, 39)})
	got, err := s.Samples(ctx, ms(t0), ms(t0.Add(2*time.Second)))
	if err != nil {
		t.Fatalf("Samples: %v", err)
	}
	if len(got) != 2 || got[1].LAeq != 41 || got[1].Baseline == nil || *got[1].Baseline != 31 {
		t.Fatalf("Samples = %+v, want seconds 0 and 1", got)
	}
}

func TestTimelineEmptyRangeReturnsNoPoints(t *testing.T) {
	s, _ := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{sample(0, 40, 45, 30)})
	got, err := s.Timeline(ctx, ms(t0.Add(time.Hour)), ms(t0.Add(2*time.Hour)), false)
	if err != nil || len(got) != 0 {
		t.Fatalf("Timeline over an empty range = %+v, %v", got, err)
	}
}

// --- Events ---

// seedEvents stores five events with known levels, classes, durations and
// reviews. It returns their ids in the order they were stored.
func seedEvents(t *testing.T, s *Store) []int64 {
	t.Helper()
	specs := []struct {
		offset time.Duration
		dur    time.Duration
		lamax  float64
		class  detect.Class
	}{
		{0, 1 * time.Second, 55, detect.Running},
		{1 * time.Hour, 2 * time.Second, 65, detect.Jumping},
		{2 * time.Hour, 3 * time.Second, 75, detect.Stomping},
		{3 * time.Hour, 4 * time.Second, 60, detect.Running},
		{4 * time.Hour, 5 * time.Second, 70, detect.Unknown},
	}
	var ids []int64
	for _, sp := range specs {
		ids = append(ids, mustInsertEvent(t, s, event(t0.Add(sp.offset), sp.dur, sp.lamax, sp.class)))
	}
	reviews := []Review{
		{EventID: ids[0], Status: StatusVerified, Note: "Kids Running again", Reviewer: "me@example.com", At: t0},
		{EventID: ids[2], Status: StatusRejected, Note: "lorry outside", Reviewer: "me@example.com", At: t0},
		{EventID: ids[4], Status: StatusVerified, Note: "", Reviewer: "me@example.com", At: t0},
	}
	for _, r := range reviews {
		if err := s.SetReview(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func idsOf(rows []EventRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func sameIDs(got []int64, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestListEventsReturnsEveryFieldAndTheReview(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedEvents(t, s)
	if err := s.InsertMedia(ctx, Media{EventID: ids[0], Kind: KindAudio, Path: "a.wav",
		Bytes: 30044, Duration: 15 * time.Second, SHA256: "abc"}); err != nil {
		t.Fatal(err)
	}

	rows, total, err := s.ListEvents(ctx, EventFilter{Sort: "started_ms"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 5 || len(rows) != 5 {
		t.Fatalf("ListEvents = %d rows, total %d; want 5 and 5", len(rows), total)
	}
	e := rows[0]
	if e.ID != ids[0] || e.StartedMS != ms(t0) || e.EndedMS != ms(t0.Add(time.Second)) || e.DurationMS != 1000 {
		t.Errorf("event times = %+v", e)
	}
	if e.LAeq != 50 || e.LAmax != 55 || e.BaselineAtTrigger != 35 || e.LowEnergy != 45 ||
		e.HighEnergy != 30 || e.LowHighRatio != 15 || e.Class != "running" || e.Confidence != 0.5 {
		t.Errorf("event levels = %+v", e)
	}
	if e.Forced {
		t.Errorf("forced = true, want false")
	}
	if e.CreatedMS != ms(t0) {
		t.Errorf("created_ms = %d, want %d", e.CreatedMS, ms(t0))
	}
	if !e.HasAudio || e.HasVideo {
		t.Errorf("has_audio %v, has_video %v; want true and false", e.HasAudio, e.HasVideo)
	}
	if e.Review == nil {
		t.Fatal("review is nil, want the verified review")
	}
	if e.Review.Status != "verified" || e.Review.Note != "Kids Running again" ||
		e.Review.Reviewer != "me@example.com" || e.Review.At.UnixMilli() != ms(t0) {
		t.Errorf("review = %+v", *e.Review)
	}
	// An event with no review row has no review, and no clip.
	if rows[1].Review != nil {
		t.Errorf("event 2 review = %+v, want nil", *rows[1].Review)
	}
	if rows[1].HasAudio {
		t.Error("event 2 has_audio = true, want false")
	}
}

func TestListEventsSortsAndPagesWithoutChangingTotal(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedEvents(t, s)

	rows, total, err := s.ListEvents(ctx, EventFilter{Sort: "started_ms", Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || !sameIDs(idsOf(rows), []int64{ids[4], ids[3], ids[2], ids[1], ids[0]}) {
		t.Fatalf("newest first = %v, total %d", idsOf(rows), total)
	}

	rows, _, err = s.ListEvents(ctx, EventFilter{Sort: "lamax", Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	// 75, 70, 65, 60, 55 dB.
	if !sameIDs(idsOf(rows), []int64{ids[2], ids[4], ids[1], ids[3], ids[0]}) {
		t.Fatalf("loudest first = %v", idsOf(rows))
	}

	rows, _, err = s.ListEvents(ctx, EventFilter{Sort: "duration_ms"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(idsOf(rows), []int64{ids[0], ids[1], ids[2], ids[3], ids[4]}) {
		t.Fatalf("shortest first = %v", idsOf(rows))
	}

	rows, total, err = s.ListEvents(ctx, EventFilter{Sort: "started_ms", Desc: true, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Errorf("total with a limit = %d, want the count before the limit, 5", total)
	}
	if !sameIDs(idsOf(rows), []int64{ids[3], ids[2]}) {
		t.Fatalf("page = %v, want the second and third newest", idsOf(rows))
	}

	// An offset with no limit still skips.
	rows, _, err = s.ListEvents(ctx, EventFilter{Sort: "started_ms", Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(idsOf(rows), []int64{ids[3], ids[4]}) {
		t.Fatalf("offset with no limit = %v, want the last two", idsOf(rows))
	}
}

// A sort key is chosen from a fixed list. A caller's string never reaches the
// SQL text.
func TestListEventsRejectsAnUnknownSort(t *testing.T) {
	s, _ := openTemp(t)
	seedEvents(t, s)
	for _, bad := range []string{"id; DROP TABLE events", "laeq", "started_ms DESC", "'", "id"} {
		_, _, err := s.ListEvents(ctx, EventFilter{Sort: bad})
		if err == nil {
			t.Errorf("sort %q was accepted", bad)
		}
	}
	var n int
	if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("events table holds %d rows, %v; want 5", n, err)
	}
}

func TestListEventsFilters(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedEvents(t, s)
	f60, f70 := 60.0, 70.0

	tests := []struct {
		name string
		f    EventFilter
		want []int64
	}{
		{"time range", EventFilter{FromMS: ms(t0.Add(time.Hour)), ToMS: ms(t0.Add(2 * time.Hour))},
			[]int64{ids[1], ids[2]}},
		{"from only", EventFilter{FromMS: ms(t0.Add(3 * time.Hour))}, []int64{ids[3], ids[4]}},
		{"to only", EventFilter{ToMS: ms(t0.Add(time.Hour))}, []int64{ids[0], ids[1]}},
		{"one class", EventFilter{Classes: []string{"running"}}, []int64{ids[0], ids[3]}},
		{"two classes", EventFilter{Classes: []string{"jumping", "stomping"}}, []int64{ids[1], ids[2]}},
		{"status verified", EventFilter{Statuses: []string{"verified"}}, []int64{ids[0], ids[4]}},
		{"status none", EventFilter{Statuses: []string{"none"}}, []int64{ids[1], ids[3]}},
		{"status none or rejected", EventFilter{Statuses: []string{"none", "rejected"}},
			[]int64{ids[1], ids[2], ids[3]}},
		{"min lamax", EventFilter{MinLAmax: &f70}, []int64{ids[2], ids[4]}},
		{"max lamax", EventFilter{MaxLAmax: &f60}, []int64{ids[0], ids[3]}},
		{"lamax band", EventFilter{MinLAmax: &f60, MaxLAmax: &f70}, []int64{ids[1], ids[3], ids[4]}},
		{"min duration", EventFilter{MinDurationMS: 3000}, []int64{ids[2], ids[3], ids[4]}},
		{"note substring", EventFilter{Note: "running"}, []int64{ids[0]}},
		{"note is case insensitive", EventFilter{Note: "KIDS"}, []int64{ids[0]}},
		{"note misses unreviewed events", EventFilter{Note: "x"}, nil},
		{"class and status together", EventFilter{Classes: []string{"running"}, Statuses: []string{"none"}},
			[]int64{ids[3]}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.f.Sort = "started_ms"
			rows, total, err := s.ListEvents(ctx, tc.f)
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if !sameIDs(idsOf(rows), tc.want) {
				t.Fatalf("ids = %v, want %v", idsOf(rows), tc.want)
			}
			if total != len(tc.want) {
				t.Fatalf("total = %d, want %d", total, len(tc.want))
			}
		})
	}
}

// The note search must treat % and _ as ordinary characters, or a note
// search for "50_percent" would match anything.
func TestListEventsNoteSearchTakesWildcardsLiterally(t *testing.T) {
	s, _ := openTemp(t)
	a := mustInsertEvent(t, s, event(t0, time.Second, 55, detect.Running))
	b := mustInsertEvent(t, s, event(t0.Add(time.Hour), time.Second, 55, detect.Running))
	s.SetReview(ctx, Review{EventID: a, Status: StatusUnsure, Note: "100% sure", At: t0})
	s.SetReview(ctx, Review{EventID: b, Status: StatusUnsure, Note: "not certain", At: t0})

	rows, total, err := s.ListEvents(ctx, EventFilter{Note: "%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != a || total != 1 {
		t.Fatalf("note search for %%: ids %v, total %d; want only the note holding a per cent sign", idsOf(rows), total)
	}
	rows, _, err = s.ListEvents(ctx, EventFilter{Note: "_"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("note search for _ matched %v, want nothing", idsOf(rows))
	}
}

// Quiet-hour filtering happens in SQL, so the total and the paging agree with
// the rows.
func TestListEventsQuietHoursFilter(t *testing.T) {
	s, _ := openTemp(t)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone database: %v", err)
	}
	q := config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}

	// Three events inside quiet hours and three outside, over two nights.
	at := func(day, hour, min int) time.Time { return time.Date(2026, 5, day, hour, min, 0, 0, loc) }
	quiet := []time.Time{at(12, 22, 0), at(13, 2, 30), at(13, 23, 15)}
	loud := []time.Time{at(12, 21, 59), at(13, 7, 0), at(13, 12, 0)}
	var quietIDs []int64
	for _, when := range quiet {
		quietIDs = append(quietIDs, mustInsertEvent(t, s, event(when, time.Second, 55, detect.Running)))
	}
	for _, when := range loud {
		mustInsertEvent(t, s, event(when, time.Second, 55, detect.Running))
	}

	f := EventFilter{Quiet: &q, Loc: loc, Sort: "started_ms"}
	rows, total, err := s.ListEvents(ctx, f)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 3 || !sameIDs(idsOf(rows), quietIDs) {
		t.Fatalf("quiet events = %v, total %d; want %v and 3", idsOf(rows), total, quietIDs)
	}

	// Paging inside the quiet filter still reports the full count.
	f.Limit, f.Offset = 1, 1
	rows, total, err = s.ListEvents(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 1 || rows[0].ID != quietIDs[1] {
		t.Fatalf("page = %v, total %d; want the second quiet event and 3", idsOf(rows), total)
	}

	// With a time range as well, the spans are cut to that range.
	f = EventFilter{Quiet: &q, Loc: loc, Sort: "started_ms",
		FromMS: ms(at(13, 0, 0)), ToMS: ms(at(13, 23, 59))}
	rows, total, err = s.ListEvents(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || !sameIDs(idsOf(rows), []int64{quietIDs[1], quietIDs[2]}) {
		t.Fatalf("quiet events on 13 May = %v, total %d; want two", idsOf(rows), total)
	}
}

// An empty table has no earliest event to clamp the quiet spans to, and the
// answer is no events rather than an error.
func TestListEventsQuietHoursOnAnEmptyTable(t *testing.T) {
	s, _ := openTemp(t)
	q := config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	rows, total, err := s.ListEvents(ctx, EventFilter{Quiet: &q})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(rows) != 0 || total != 0 {
		t.Fatalf("rows %v, total %d; want none", idsOf(rows), total)
	}
}

// Events far apart make one range per night between them. SQLite refuses an
// expression more than 1000 deep, and a flat OR chain of 3000 nights was
// 3000 deep, so the filter failed on a box that had run for three years.
func TestListEventsQuietHoursOverManyYears(t *testing.T) {
	s, _ := openTemp(t)
	loc := time.FixedZone("test", 0)
	q := config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	first := time.Date(2026, 5, 12, 23, 0, 0, 0, loc)
	last := first.AddDate(0, 0, 3000)
	a := mustInsertEvent(t, s, event(first, time.Second, 55, detect.Running))
	mustInsertEvent(t, s, event(first.Add(14*time.Hour), time.Second, 55, detect.Running)) // 13:00
	b := mustInsertEvent(t, s, event(last, time.Second, 55, detect.Running))

	rows, total, err := s.ListEvents(ctx, EventFilter{Quiet: &q, Loc: loc, Sort: "started_ms"})
	if err != nil {
		t.Fatalf("ListEvents over 3000 nights: %v", err)
	}
	if total != 2 || !sameIDs(idsOf(rows), []int64{a, b}) {
		t.Fatalf("quiet events = %v, total %d; want %v and 2", idsOf(rows), total, []int64{a, b})
	}
}

func TestEventReadsOneRowAndReportsAMissingOne(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedEvents(t, s)
	got, err := s.Event(ctx, ids[2])
	if err != nil {
		t.Fatalf("Event: %v", err)
	}
	if got.ID != ids[2] || got.LAmax != 75 || got.Class != "stomping" ||
		got.Review == nil || got.Review.Status != "rejected" {
		t.Fatalf("event = %+v", got)
	}
	_, err = s.Event(ctx, 9999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Event for a missing id = %v, want ErrNotFound", err)
	}
}

// An event row carries the two rise values, so a reader can see why the
// classifier called the event what it did. An event with no level history
// before it carries neither, and says so with an invalid value rather than a
// zero.
func TestEventCarriesTheTwoRiseValues(t *testing.T) {
	s, _ := openTemp(t)
	measured := mustInsertEvent(t, s, testEvent())
	got, err := s.Event(ctx, measured)
	if err != nil {
		t.Fatalf("Event: %v", err)
	}
	if !got.JumpDB.Valid || got.JumpDB.Float64 != 17.1 {
		t.Errorf("JumpDB = %v; want a valid 17.1", got.JumpDB)
	}
	if !got.RiseDB.Valid || got.RiseDB.Float64 != 13.3 {
		t.Errorf("RiseDB = %v; want a valid 13.3", got.RiseDB)
	}

	blind := testEvent()
	blind.HasContext = false
	got, err = s.Event(ctx, mustInsertEvent(t, s, blind))
	if err != nil {
		t.Fatalf("Event: %v", err)
	}
	if got.JumpDB.Valid || got.RiseDB.Valid {
		t.Errorf("JumpDB, RiseDB = %v, %v; want neither for an event with no level history",
			got.JumpDB, got.RiseDB)
	}
}

// A clip row says when its first sample was taken, so the dashboard can put
// the waveform on the same clock as everything else. A row that has no start
// reads back as the zero time, which means "not recorded", not the epoch.
func TestMediaCarriesTheClipStart(t *testing.T) {
	s, _ := openTemp(t)
	id := mustInsertEvent(t, s, event(t0, time.Second, 55, detect.Running))
	// t0 is 2026-09-11 03:00:00 UTC. The clip starts ten seconds earlier.
	started := time.Date(2026, 9, 11, 2, 59, 50, 0, time.UTC)
	want := Media{EventID: id, Kind: KindAudio, Path: "a.wav", Bytes: 30044,
		Duration: 15 * time.Second, SHA256: "abc", Started: started}
	if err := s.InsertMedia(ctx, want); err != nil {
		t.Fatal(err)
	}
	var stored int64
	err := s.r.QueryRowContext(ctx, `SELECT started_ms FROM event_media WHERE event_id = ? AND kind = 'audio'`, id).
		Scan(&stored)
	if err != nil || stored != 1789095590000 {
		t.Fatalf("stored started_ms = %d, %v; want 1789095590000", stored, err)
	}

	list, err := s.EventMedia(ctx, id)
	if err != nil || len(list) != 1 || list[0] != want {
		t.Fatalf("EventMedia = %+v, %v; want %+v", list, err, want)
	}
	got, err := s.MediaFile(ctx, id, KindAudio)
	if err != nil || got != want {
		t.Fatalf("MediaFile = %+v, %v; want %+v", got, err, want)
	}

	// A clip whose start was never recorded must not read back as 1970.
	none := Media{EventID: id, Kind: KindVideo, Path: "b.mp4", SHA256: "def"}
	if err := s.InsertMedia(ctx, none); err != nil {
		t.Fatal(err)
	}
	silent, err := s.MediaFile(ctx, id, KindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if !silent.Started.IsZero() {
		t.Errorf("a clip with no recorded start reads back as %v, want the zero time", silent.Started)
	}
}

func TestEventEnvelopeAndMedia(t *testing.T) {
	s, _ := openTemp(t)
	id := mustInsertEvent(t, s, event(t0, time.Second, 55, detect.Running))
	env, err := s.EventEnvelope(ctx, id)
	if err != nil {
		t.Fatalf("EventEnvelope: %v", err)
	}
	if len(env) != 3 || env[0] != 0.25 || env[1] != 0.5 || env[2] != 1 {
		t.Fatalf("envelope = %v, want 0.25 0.5 1", env)
	}
	if _, err := s.EventEnvelope(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("EventEnvelope for a missing id = %v, want ErrNotFound", err)
	}

	want := Media{EventID: id, Kind: KindAudio, Path: "/data/clips/audio/a.wav",
		Bytes: 30044, Duration: 15 * time.Second, SHA256: "abc", Truncated: true}
	if err := s.InsertMedia(ctx, want); err != nil {
		t.Fatal(err)
	}
	list, err := s.EventMedia(ctx, id)
	if err != nil {
		t.Fatalf("EventMedia: %v", err)
	}
	if len(list) != 1 || list[0] != want {
		t.Fatalf("media = %+v, want %+v", list, want)
	}
	got, err := s.MediaFile(ctx, id, KindAudio)
	if err != nil || got != want {
		t.Fatalf("MediaFile = %+v, %v", got, err)
	}
	if _, err := s.MediaFile(ctx, id, KindVideo); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MediaFile for a missing kind = %v, want ErrNotFound", err)
	}
	if list, err := s.EventMedia(ctx, 9999); err != nil || len(list) != 0 {
		t.Fatalf("EventMedia for a missing event = %+v, %v; want none and no error", list, err)
	}
}

func TestDeleteReviewMakesAnEventUnreviewedAgain(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedEvents(t, s)
	if err := s.DeleteReview(ctx, ids[0]); err != nil {
		t.Fatalf("DeleteReview: %v", err)
	}
	got, err := s.Event(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.Review != nil {
		t.Fatalf("review after the delete = %+v, want nil", *got.Review)
	}
	// The event itself is untouched.
	if got.LAmax != 55 {
		t.Errorf("lamax = %v, want 55", got.LAmax)
	}
	// Deleting again is not an error: the caller wanted no review, and there
	// is none.
	if err := s.DeleteReview(ctx, ids[0]); err != nil {
		t.Errorf("second DeleteReview: %v", err)
	}
	// The other reviews are still there.
	rows, _, _ := s.ListEvents(ctx, EventFilter{Statuses: []string{"verified", "rejected"}})
	if len(rows) != 2 {
		t.Errorf("%d reviews left, want 2", len(rows))
	}
}

// --- Health ---

func TestListHealthFiltersAndCounts(t *testing.T) {
	s, _ := openTemp(t)
	adds := []struct {
		offset time.Duration
		kind   string
		detail string
		dur    time.Duration
	}{
		{0, HealthCaptureGap, "arecord exited", 1500 * time.Millisecond},
		{time.Hour, HealthDiskLow, "900 MB left", 0},
		{2 * time.Hour, HealthCaptureGap, "second gap", 250 * time.Millisecond},
		{3 * time.Hour, HealthWriteError, "disk full", 0},
	}
	for _, a := range adds {
		if err := s.AddHealth(ctx, t0.Add(a.offset), a.kind, a.detail, a.dur); err != nil {
			t.Fatal(err)
		}
	}

	rows, total, err := s.ListHealth(ctx, HealthFilter{})
	if err != nil {
		t.Fatalf("ListHealth: %v", err)
	}
	if total != 4 || len(rows) != 4 {
		t.Fatalf("ListHealth = %d rows, total %d; want 4 and 4", len(rows), total)
	}
	// Newest first.
	if rows[0].TSMS != ms(t0.Add(3*time.Hour)) || rows[0].Kind != "write_error" || rows[0].Detail != "disk full" {
		t.Errorf("newest row = %+v", rows[0])
	}
	if rows[3].TSMS != ms(t0) || rows[3].DurationMS != 1500 {
		t.Errorf("oldest row = %+v", rows[3])
	}
	// A health row may have no duration, and that reads as 0.
	if rows[2].DurationMS != 0 {
		t.Errorf("disk_low duration = %d, want 0", rows[2].DurationMS)
	}

	rows, total, err = s.ListHealth(ctx, HealthFilter{Kinds: []string{HealthCaptureGap}})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("capture_gap rows = %d, total %d, %v; want 2", len(rows), total, err)
	}
	rows, total, err = s.ListHealth(ctx, HealthFilter{
		FromMS: ms(t0.Add(time.Hour)), ToMS: ms(t0.Add(2 * time.Hour))})
	if err != nil || total != 2 || len(rows) != 2 || rows[0].Kind != "capture_gap" {
		t.Fatalf("range rows = %+v, total %d, %v", rows, total, err)
	}
	rows, total, err = s.ListHealth(ctx, HealthFilter{Limit: 1, Offset: 1})
	if err != nil || total != 4 || len(rows) != 1 || rows[0].TSMS != ms(t0.Add(2*time.Hour)) {
		t.Fatalf("page = %+v, total %d, %v; want the second newest and total 4", rows, total, err)
	}
}

// --- Summary ---

func TestSummaryCountsLevelsAndLoudest(t *testing.T) {
	s, _ := openTemp(t)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone database: %v", err)
	}
	q := config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}
	at := func(day, hour, min int) time.Time { return time.Date(2026, 5, day, hour, min, 0, 0, loc) }

	// Four events: two in quiet hours, two not. Two are reviewed, one of
	// those verified.
	quiet1 := mustInsertEvent(t, s, event(at(12, 23, 0), time.Second, 62, detect.Running))
	loudest := mustInsertEvent(t, s, event(at(13, 3, 0), time.Second, 81, detect.Stomping))
	day1 := mustInsertEvent(t, s, event(at(13, 10, 0), time.Second, 58, detect.Jumping))
	mustInsertEvent(t, s, event(at(13, 14, 0), time.Second, 70, detect.Unknown))
	s.SetReview(ctx, Review{EventID: quiet1, Status: StatusVerified, At: t0})
	s.SetReview(ctx, Review{EventID: day1, Status: StatusRejected, At: t0})

	// Three seconds of level: 60, 70 and 80 dB give an energy mean of
	// 10*log10((10^6 + 10^7 + 10^8) / 3) = 75.682 dB. The mean of the dB
	// numbers would be 70, which is a different and wrong answer.
	// Baselines 30, 44 and 32 have a median of 32.
	base := at(13, 1, 0)
	var bins []meter.Bin
	for i, v := range []struct{ laeq, base float64 }{{60, 30}, {70, 44}, {80, 32}} {
		bins = append(bins, meter.Bin{Start: base.Add(time.Duration(i) * time.Second), Samples: 48000,
			LAeq: v.laeq, LAmax: v.laeq + 5, LowBand: 40, HighBand: 30, Baseline: v.base})
	}
	if _, err := s.InsertBins(ctx, bins); err != nil {
		t.Fatal(err)
	}

	got, err := s.Summary(ctx, ms(at(12, 0, 0)), ms(at(14, 0, 0)), q, loc)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.Events != 4 || got.Reviewed != 2 || got.Verified != 1 || got.QuietHourEvents != 2 {
		t.Errorf("counts = %+v; want 4 events, 2 reviewed, 1 verified, 2 in quiet hours", got)
	}
	if !got.HaveLevels {
		t.Fatal("HaveLevels = false, want true")
	}
	if math.Abs(got.LAeq-75.682) > 0.001 {
		t.Errorf("LAeq = %.3f, want the energy mean 75.682", got.LAeq)
	}
	if got.Baseline != 32 {
		t.Errorf("Baseline = %v, want the median 32", got.Baseline)
	}
	if got.Loudest == nil || got.Loudest.ID != loudest || got.Loudest.LAmax != 81 {
		t.Fatalf("loudest = %+v, want event %d at 81 dB", got.Loudest, loudest)
	}
}

// An even number of samples has two middle values, and the median is their
// mean.
// An evening the owner explains is still measured and still stored, but it
// is not part of the headline. The count of muted events is reported, so the
// number is visible rather than silently dropped.
func TestSummaryLeavesMutedEventsOutOfTheCounts(t *testing.T) {
	s, _ := openTemp(t)
	q := config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}

	// t0 is 03:00 UTC, inside quiet hours. The fourth event is at 10:00,
	// outside them.
	mustInsertEvent(t, s, event(t0, time.Second, 60, detect.Running))
	muted := mustInsertEvent(t, s, event(t0.Add(30*time.Minute), time.Second, 70, detect.Jumping))
	loud := mustInsertEvent(t, s, event(t0.Add(time.Hour), time.Second, 65, detect.Stomping))
	mustInsertEvent(t, s, event(t0.Add(7*time.Hour), time.Second, 55, detect.Unknown))
	s.SetReview(ctx, Review{EventID: muted, Status: StatusVerified, At: t0})
	s.SetReview(ctx, Review{EventID: loud, Status: StatusRejected, At: t0})
	if _, err := s.AddMuteWindow(ctx, MuteWindow{
		StartMS: ms(t0.Add(29 * time.Minute)), EndMS: ms(t0.Add(31 * time.Minute)), Reason: "party",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Summary(ctx, ms(t0.Add(-time.Minute)), ms(t0.Add(8*time.Hour)), q, time.UTC)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.Events != 3 {
		t.Errorf("Events = %d, want 3: the four stored events less the muted one", got.Events)
	}
	if got.Muted != 1 {
		t.Errorf("Muted = %d, want 1", got.Muted)
	}
	if got.Reviewed != 1 || got.Verified != 0 {
		t.Errorf("Reviewed %d, Verified %d; want 1 and 0, the muted review left out",
			got.Reviewed, got.Verified)
	}
	if got.QuietHourEvents != 2 {
		t.Errorf("QuietHourEvents = %d, want 2: two of the three unmuted events are at night",
			got.QuietHourEvents)
	}
	if got.Loudest == nil || got.Loudest.ID != loud {
		t.Errorf("Loudest = %+v, want event %d: the louder one is muted", got.Loudest, loud)
	}
}

func TestSummaryMedianOfAnEvenNumberOfSamples(t *testing.T) {
	s, _ := openTemp(t)
	var bins []meter.Bin
	for i, b := range []float64{44, 30, 32, 40} {
		bins = append(bins, meter.Bin{Start: t0.Add(time.Duration(i) * time.Second), Samples: 48000,
			LAeq: 50, LAmax: 55, LowBand: 40, HighBand: 30, Baseline: b})
	}
	s.InsertBins(ctx, bins)
	got, err := s.Summary(ctx, ms(t0), ms(t0.Add(10*time.Second)),
		config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted: 30, 32, 40, 44. The median is (32 + 40) / 2 = 36.
	if got.Baseline != 36 {
		t.Errorf("Baseline = %v, want 36", got.Baseline)
	}
	if got.LAeq != 50 {
		t.Errorf("LAeq = %v, want 50", got.LAeq)
	}
}

// A range with no samples reports that it has no levels instead of a made-up
// zero that would draw as silence.
func TestSummaryWithNoSamplesSaysSo(t *testing.T) {
	s, _ := openTemp(t)
	mustInsertEvent(t, s, event(t0, time.Second, 55, detect.Running))
	got, err := s.Summary(ctx, ms(t0), ms(t0.Add(time.Hour)),
		config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}, time.UTC)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.HaveLevels {
		t.Error("HaveLevels = true, want false")
	}
	if got.LAeq != 0 || got.Baseline != 0 {
		t.Errorf("LAeq %v, Baseline %v; want 0 and 0 when there are no samples", got.LAeq, got.Baseline)
	}
	if got.Events != 1 || got.Loudest == nil {
		t.Errorf("events %d, loudest %v; want the one event", got.Events, got.Loudest)
	}
}

func TestSummaryWithNoEventsHasNoLoudest(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.Summary(ctx, ms(t0), ms(t0.Add(time.Hour)),
		config.QuietHours{Start: 22 * time.Hour, End: 7 * time.Hour}, time.UTC)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got.Loudest != nil || got.Events != 0 {
		t.Fatalf("summary = %+v, want no events and no loudest", got)
	}
}

// --- Counts, settings, mute windows, size ---

func TestCountsSince(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedEvents(t, s)
	s.DeleteReview(ctx, ids[0]) // leaves three unreviewed of five
	s.InsertBins(ctx, []meter.Bin{
		sample(0, 40, 45, 30), sample(1, 41, 46, 31), sample(3600, 42, 47, 32), sample(3601, 43, 48, 33),
	})

	got, err := s.CountsSince(ctx, ms(t0.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CountsSince: %v", err)
	}
	if got.EventsTotal != 5 || got.Unreviewed != 3 {
		t.Errorf("EventsTotal %d, Unreviewed %d; want 5 and 3", got.EventsTotal, got.Unreviewed)
	}
	if got.EventsSince != 4 {
		t.Errorf("EventsSince = %d, want the 4 events at or after one hour in", got.EventsSince)
	}
	if got.SamplesSince != 2 {
		t.Errorf("SamplesSince = %d, want 2", got.SamplesSince)
	}

	// A bound part way through a second keeps that second.
	got, err = s.CountsSince(ctx, ms(t0.Add(time.Hour))+500)
	if err != nil {
		t.Fatal(err)
	}
	if got.SamplesSince != 2 {
		t.Errorf("SamplesSince from half way through second 3600 = %d, want 2", got.SamplesSince)
	}
}

func TestSettingsRoundTripAndDelete(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.Settings(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("Settings on a new database = %v, %v; want none", got, err)
	}

	twelve, quiet := "12", "22:30"
	if err := s.PutSettings(ctx, map[string]*string{"threshold_db": &twelve, "quiet_start": &quiet}, t0); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	got, err = s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["threshold_db"] != "12" || got["quiet_start"] != "22:30" {
		t.Fatalf("Settings = %v", got)
	}
	var updated int64
	s.r.QueryRowContext(ctx, `SELECT updated_ms FROM config WHERE key = 'threshold_db'`).Scan(&updated)
	if updated != ms(t0) {
		t.Errorf("updated_ms = %d, want %d", updated, ms(t0))
	}

	// Writing again replaces the value, and nil removes the row so the file
	// default applies again.
	ten := "10"
	if err := s.PutSettings(ctx, map[string]*string{"threshold_db": &ten, "quiet_start": nil}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Settings(ctx)
	if len(got) != 1 || got["threshold_db"] != "10" {
		t.Fatalf("Settings after the replace and delete = %v", got)
	}
	// Deleting a key that is not there is not an error.
	if err := s.PutSettings(ctx, map[string]*string{"missing": nil}, t0); err != nil {
		t.Errorf("PutSettings deleting a missing key: %v", err)
	}
}

// A bad key rejects the whole call, so half a settings change never lands.
func TestPutSettingsRejectsABadKeyAndWritesNothing(t *testing.T) {
	s, _ := openTemp(t)
	v := "12"
	err := s.PutSettings(ctx, map[string]*string{"threshold_db": &v, "": &v}, t0)
	if err == nil {
		t.Fatal("PutSettings with an empty key succeeded, want an error")
	}
	got, _ := s.Settings(ctx)
	if len(got) != 0 {
		t.Fatalf("Settings = %v, want nothing written", got)
	}
}

func TestMuteWindows(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.MuteWindows(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("MuteWindows on a new database = %v, %v", got, err)
	}

	id, err := s.AddMuteWindow(ctx, MuteWindow{StartMS: ms(t0), EndMS: ms(t0.Add(time.Hour)), Reason: "party"})
	if err != nil || id == 0 {
		t.Fatalf("AddMuteWindow = %d, %v", id, err)
	}
	second, err := s.AddMuteWindow(ctx, MuteWindow{StartMS: ms(t0.Add(2 * time.Hour)), EndMS: ms(t0.Add(3 * time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.MuteWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []MuteWindow{
		{ID: id, StartMS: ms(t0), EndMS: ms(t0.Add(time.Hour)), Reason: "party"},
		{ID: second, StartMS: ms(t0.Add(2 * time.Hour)), EndMS: ms(t0.Add(3 * time.Hour))},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("MuteWindows = %+v, want %+v", got, want)
	}

	if err := s.DeleteMuteWindow(ctx, id); err != nil {
		t.Fatalf("DeleteMuteWindow: %v", err)
	}
	got, _ = s.MuteWindows(ctx)
	if len(got) != 1 || got[0].ID != second {
		t.Fatalf("MuteWindows after the delete = %+v", got)
	}
	if err := s.DeleteMuteWindow(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteMuteWindow for a missing id = %v, want ErrNotFound", err)
	}
}

// seedMuted stores two mute windows, 04:00 to 05:00 and 06:00 to 07:00, and
// seven events around their edges. It returns the ids in the order of the
// cases below, and whether each event overlaps a window.
type mutedCase struct {
	name  string
	start time.Duration
	dur   time.Duration
	muted bool
}

var mutedCases = []mutedCase{
	{"ends one millisecond before a window opens", 30 * time.Minute, 30*time.Minute - time.Millisecond, false},
	{"ends exactly when a window opens", 30 * time.Minute, 30 * time.Minute, true},
	{"sits inside a window", 90 * time.Minute, time.Minute, true},
	{"starts exactly when a window closes", 2 * time.Hour, time.Minute, true},
	{"starts one millisecond after a window closes", 2*time.Hour + time.Millisecond, time.Minute, false},
	{"covers a whole window and reaches into the next", 90 * time.Minute, 2 * time.Hour, true},
	{"falls between the two windows", 2*time.Hour + time.Second, 30 * time.Minute, false},
}

func seedMuted(t *testing.T, s *Store) []int64 {
	t.Helper()
	for _, w := range []MuteWindow{
		{StartMS: ms(t0.Add(time.Hour)), EndMS: ms(t0.Add(2 * time.Hour)), Reason: "party"},
		{StartMS: ms(t0.Add(3 * time.Hour)), EndMS: ms(t0.Add(4 * time.Hour)), Reason: "guests"},
	} {
		if _, err := s.AddMuteWindow(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	var ids []int64
	for _, c := range mutedCases {
		ids = append(ids, mustInsertEvent(t, s, event(t0.Add(c.start), c.dur, 60, detect.Running)))
	}
	return ids
}

// A mute window never stops the collector measuring or storing an event. It
// marks the events the owner can already explain. An event touching a window
// at either edge is inside it.
func TestMuteWindowsMarkTheEventsTheyCover(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedMuted(t, s)

	rows, total, err := s.ListEvents(ctx, EventFilter{Sort: "started_ms"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if total != 7 || len(rows) != 7 {
		t.Fatalf("ListEvents = %d rows, total %d; want 7 and 7, each event once", len(rows), total)
	}
	byID := map[int64]EventRow{}
	for _, r := range rows {
		if _, twice := byID[r.ID]; twice {
			t.Fatalf("event %d came back more than once", r.ID)
		}
		byID[r.ID] = r
	}
	for i, c := range mutedCases {
		if got := byID[ids[i]].Muted; got != c.muted {
			t.Errorf("an event that %s is muted %v, want %v", c.name, got, c.muted)
		}
		one, err := s.Event(ctx, ids[i])
		if err != nil {
			t.Fatal(err)
		}
		if one.Muted != c.muted {
			t.Errorf("Event for one that %s is muted %v, want %v", c.name, one.Muted, c.muted)
		}
	}
}

// The filter runs in the database, so the total, the limit and the offset all
// agree with the rows that come back.
func TestMutedFilterKeepsTheTotalHonest(t *testing.T) {
	s, _ := openTemp(t)
	ids := seedMuted(t, s)
	yes, no := true, false

	for _, c := range []struct {
		name  string
		muted *bool
		want  []int64
	}{
		// Time order, so the two events that start at the same moment come
		// back oldest id first.
		{"every event", nil, []int64{ids[0], ids[1], ids[2], ids[5], ids[3], ids[4], ids[6]}},
		{"only the muted ones", &yes, []int64{ids[1], ids[2], ids[5], ids[3]}},
		{"only the unmuted ones", &no, []int64{ids[0], ids[4], ids[6]}},
	} {
		rows, total, err := s.ListEvents(ctx, EventFilter{Sort: "started_ms", Muted: c.muted})
		if err != nil {
			t.Fatalf("%s: ListEvents: %v", c.name, err)
		}
		if !sameIDs(idsOf(rows), c.want) {
			t.Errorf("%s: ids = %v, want %v", c.name, idsOf(rows), c.want)
		}
		if total != len(c.want) {
			t.Errorf("%s: total = %d, want %d", c.name, total, len(c.want))
		}
	}

	// One page of the muted events must still report every match.
	rows, total, err := s.ListEvents(ctx, EventFilter{Sort: "started_ms", Muted: &yes, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || total != 4 {
		t.Errorf("a page of 2 = %d rows, total %d; want 2 and 4", len(rows), total)
	}
}

func TestDBSizeCountsTheFileAndTheWAL(t *testing.T) {
	s, path := openTemp(t)
	s.InsertBins(ctx, []meter.Bin{sample(0, 40, 45, 30)})

	main, wal, err := s.DBSize()
	if err != nil {
		t.Fatalf("DBSize: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if main != fi.Size() {
		t.Errorf("main = %d, want the file size %d", main, fi.Size())
	}
	if main <= 0 {
		t.Errorf("main = %d, want a size above 0", main)
	}
	wfi, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if wal != wfi.Size() || wal <= 0 {
		t.Errorf("wal = %d, want the -wal file size %d", wal, wfi.Size())
	}

	// A database with no WAL file reports 0 rather than failing.
	if err := os.Remove(path + "-wal"); err != nil {
		t.Skipf("cannot remove the WAL file: %v", err)
	}
	_, wal, err = s.DBSize()
	if err != nil || wal != 0 {
		t.Errorf("DBSize with no WAL file = %d, %v; want 0 and no error", wal, err)
	}
}

// A clip whose samples hit the 16-bit limit is distorted. The collector
// already counts them, but the count stayed in the log where nobody looks.
func TestMediaCarriesTheClippedSampleCount(t *testing.T) {
	s, _ := openTemp(t)
	id := mustInsertEvent(t, s, event(t0, time.Second, 55, detect.Running))

	err := s.InsertMedia(ctx, Media{
		EventID: id, Kind: KindAudio, Path: "a.wav", Bytes: 100,
		Duration: time.Second, SHA256: "abc", Clipped: 197,
	})
	if err != nil {
		t.Fatalf("InsertMedia: %v", err)
	}
	m, err := s.MediaFile(ctx, id, KindAudio)
	if err != nil {
		t.Fatalf("MediaFile: %v", err)
	}
	if m.Clipped != 197 {
		t.Errorf("Clipped = %d, want 197", m.Clipped)
	}
}

// SPEC.md section 15 decision 22: a video clip carries whether it holds the
// camera's own audio track. It is stored with the clip and not read from the
// setting later, because a clip keeps what it holds after the setting
// changes, so a database holds clips of both kinds.
func TestMediaRecordsWhetherTheClipCarriesCameraAudio(t *testing.T) {
	s, _ := openTemp(t)
	id := mustInsertEvent(t, s, event(t0, time.Second, 55, detect.Running))

	loud := Media{EventID: id, Kind: KindVideo, Path: "v.mp4", Bytes: 900,
		Duration: 20 * time.Second, SHA256: "def", CameraAudio: true}
	if err := s.InsertMedia(ctx, loud); err != nil {
		t.Fatal(err)
	}
	got, err := s.MediaFile(ctx, id, KindVideo)
	if err != nil || got != loud {
		t.Fatalf("MediaFile = %+v, %v; want %+v", got, err, loud)
	}

	// An audio clip is the measuring microphone's and is filtered, so it
	// never carries camera audio.
	quiet := Media{EventID: id, Kind: KindAudio, Path: "a.wav", Bytes: 100,
		Duration: 20 * time.Second, SHA256: "abc"}
	if err := s.InsertMedia(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	back, err := s.MediaFile(ctx, id, KindAudio)
	if err != nil {
		t.Fatal(err)
	}
	if back.CameraAudio {
		t.Error("a clip written with no camera audio reads back as carrying it")
	}
}

// Totals is what the owner reads before the data is removed, so every count
// must be its own: an events count that also counted the reviews, or an
// audio count that also counted the video, would understate or overstate
// what is about to be lost.
func TestTotalsCountsEveryKindOfRowSeparately(t *testing.T) {
	s, _ := openTemp(t)
	first := mustInsertEvent(t, s, event(t0, time.Second, 70, detect.Jumping))
	second := mustInsertEvent(t, s, event(t0.Add(time.Minute), time.Second, 71, detect.Running))
	mustInsertEvent(t, s, event(t0.Add(2*time.Minute), time.Second, 72, detect.Stomping))
	if _, err := s.InsertBins(ctx, []meter.Bin{sample(0, 40, 45, 30), sample(1, 41, 46, 31)}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Media{
		{EventID: first, Kind: KindAudio, Path: "a.wav", Bytes: 10, SHA256: "x"},
		{EventID: first, Kind: KindVideo, Path: "a.mp4", Bytes: 20, SHA256: "y"},
		{EventID: second, Kind: KindVideo, Path: "b.mp4", Bytes: 30, SHA256: "z"},
	} {
		if err := s.InsertMedia(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetReview(ctx, Review{EventID: first, Status: StatusVerified, At: t0}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{HealthCaptureGap, HealthDiskLow, HealthWriteError, HealthDataReset} {
		if err := s.AddHealth(ctx, t0, kind, "one", 0); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Totals(ctx)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	want := Totals{Events: 3, Seconds: 2, AudioClips: 1, VideoClips: 2, Reviews: 1, HealthRows: 4}
	if got != want {
		t.Errorf("Totals = %+v, want %+v", got, want)
	}
}

// An empty database holds nothing, and every count must be zero rather than
// an error: reset runs on a box that has just been installed too.
func TestTotalsOfAnEmptyDatabaseIsZero(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.Totals(ctx)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if got != (Totals{}) {
		t.Errorf("Totals of an empty database = %+v, want every count zero", got)
	}
}

// TotalsOf counts without opening the database for writing. stompwatch reset
// counts before it prints a preview, and a preview must change nothing: a
// read-write open applies any pending migration, and folds the write-ahead
// log into the database as it closes.
func TestTotalsOfDoesNotWriteToTheDatabase(t *testing.T) {
	s, path := openTemp(t)
	mustInsertEvent(t, s, event(t0, time.Second, 70, detect.Jumping))
	if _, err := s.InsertBins(ctx, []meter.Bin{sample(0, 40, 45, 30)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A write-ahead log and its shared-memory file, the way a collector that
	// was killed leaves them. What is in them does not matter here; that they
	// are still there afterwards does.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	before := fileState(t, path)

	got, err := TotalsOf(ctx, path)
	if err != nil {
		t.Fatalf("TotalsOf: %v", err)
	}
	if (got != Totals{Events: 1, Seconds: 1}) {
		t.Errorf("TotalsOf = %+v, want 1 event and 1 second", got)
	}
	// The database and its write-ahead log must both be exactly as they were.
	// The -shm file is not data: it is the shared-memory index SQLite keeps
	// beside a write-ahead log, and it writes that one even to read.
	after := fileState(t, path)
	for _, p := range []string{path, path + "-wal"} {
		if after[p] != before[p] {
			t.Errorf("%s is %q, was %q", p, after[p], before[p])
		}
	}
}

// SettingsOf reads the dashboard's saved settings without opening the
// database for writing, for the same reason TotalsOf does: stompwatch reset
// prints them in its preview, and a preview must change nothing.
func TestSettingsOfReadsTheSavedSettingsWithoutWriting(t *testing.T) {
	s, path := openTemp(t)
	pause, level := "22:00-07:00", "14"
	if err := s.PutSettings(ctx, map[string]*string{"recording_pause": &pause, "threshold_db": &level}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The two files a killed collector leaves, as in the TotalsOf test.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	before := fileState(t, path)

	got, err := SettingsOf(ctx, path)
	if err != nil {
		t.Fatalf("SettingsOf: %v", err)
	}
	if len(got) != 2 || got["recording_pause"] != "22:00-07:00" || got["threshold_db"] != "14" {
		t.Errorf("SettingsOf = %v, want recording_pause 22:00-07:00 and threshold_db 14", got)
	}
	after := fileState(t, path)
	for _, p := range []string{path, path + "-wal"} {
		if after[p] != before[p] {
			t.Errorf("%s is %q, was %q", p, after[p], before[p])
		}
	}
}

// fileState is the size and modification time of a database file and of the
// two files beside it, keyed by path. "gone" is a file that is not there.
func fileState(t *testing.T, path string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		fi, err := os.Stat(p)
		if err != nil {
			out[p] = "gone"
			continue
		}
		out[p] = fmt.Sprintf("%d bytes at %v", fi.Size(), fi.ModTime())
	}
	return out
}
