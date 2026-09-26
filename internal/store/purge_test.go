package store

import (
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/meter"
)

// mediaFor stores an event with one clip of each kind it is given, and
// returns the event's number. The sizes are distinct so a test can tell
// which row a total came from.
func (helper *purgeFixture) event(startOffset time.Duration, kinds map[string]int64) int64 {
	helper.t.Helper()
	ev := testEvent()
	ev.Start = t0.Add(startOffset)
	ev.End = ev.Start.Add(2 * time.Second)
	id, err := helper.s.InsertEvent(ctx, ev, ev.Start)
	if err != nil {
		helper.t.Fatal(err)
	}
	for kind, bytes := range kinds {
		err := helper.s.InsertMedia(ctx, Media{
			EventID: id, Kind: kind, Path: "clip", Bytes: bytes,
			Duration: 10 * time.Second, SHA256: strings.Repeat("ab", 32),
		})
		if err != nil {
			helper.t.Fatal(err)
		}
	}
	return id
}

type purgeFixture struct {
	t *testing.T
	s *Store
}

func newPurgeFixture(t *testing.T) *purgeFixture {
	t.Helper()
	s, _ := openTemp(t)
	return &purgeFixture{t: t, s: s}
}

// A purge row is raw evidence in the same sense an event row is: it records
// that the owner deleted a recording. Once written it cannot be unwritten,
// or the record could be made to say a file was never deleted.
func TestPurgeRowsRejectUpdateAndDelete(t *testing.T) {
	f := newPurgeFixture(t)
	id := f.event(0, map[string]int64{"audio": 1000})
	err := f.s.RecordPurge(ctx, []Purge{{EventID: id, Kind: "audio", Bytes: 1000, At: t0, By: "alex"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE media_purge SET bytes = 0`,
		`UPDATE media_purge SET purged_by = 'somebody else'`,
		`DELETE FROM media_purge`,
	} {
		if _, err := f.s.w.ExecContext(ctx, stmt); err == nil {
			t.Errorf("%s succeeded", stmt)
		}
	}
}

// Recording a purge writes to media_purge and to nothing else. The event,
// the seconds around it, and the media row with its size and its hash are
// the record that the clip existed (SPEC.md section 3 rule 3).
func TestRecordPurgeChangesNoRawTable(t *testing.T) {
	f := newPurgeFixture(t)
	if _, err := f.s.InsertBins(ctx, []meter.Bin{bin(0, 60), bin(1, 70)}); err != nil {
		t.Fatal(err)
	}
	id := f.event(0, map[string]int64{"audio": 1000, "video": 8_000_000})
	f.s.SetReview(ctx, Review{EventID: id, Status: StatusVerified, Note: "heard it", At: t0})

	before := map[string]string{}
	for _, tbl := range []string{"events", "samples_1s", "event_media", "event_review"} {
		before[tbl] = tableHash(t, f.s.r, tbl)
	}

	err := f.s.RecordPurge(ctx, []Purge{
		{EventID: id, Kind: "audio", Bytes: 1000, At: t0, By: "alex"},
		{EventID: id, Kind: "video", Bytes: 8_000_000, At: t0, By: "alex"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for tbl, want := range before {
		if got := tableHash(t, f.s.r, tbl); got != want {
			t.Errorf("table %s changed when a purge was recorded", tbl)
		}
	}
	var rows int
	if err := f.s.r.QueryRowContext(ctx, `SELECT count(*) FROM media_purge`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("media_purge holds %d rows, want 2", rows)
	}
}

// The purge carries who did it and when, so the event screen can say the
// recording was deleted on a date by a person.
func TestPurgedMediaReturnsWhoAndWhen(t *testing.T) {
	f := newPurgeFixture(t)
	first := f.event(0, map[string]int64{"audio": 1000, "video": 2000})
	second := f.event(time.Hour, map[string]int64{"audio": 3000})
	untouched := f.event(2*time.Hour, map[string]int64{"audio": 4000})

	at := time.Date(2026, 9, 12, 14, 30, 0, 0, time.UTC)
	err := f.s.RecordPurge(ctx, []Purge{
		{EventID: first, Kind: "audio", Bytes: 1000, At: at, By: "alex"},
		{EventID: second, Kind: "audio", Bytes: 3000, At: at, By: "alex"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := f.s.PurgedMedia(ctx, []int64{first, second, untouched})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("PurgedMedia covered %d events, want 2: %v", len(got), got)
	}
	if _, ok := got[untouched]; ok {
		t.Errorf("event %d has no purge but appears in the answer", untouched)
	}
	one := got[first]
	if len(one) != 1 {
		t.Fatalf("event %d has %d purges, want 1", first, len(one))
	}
	if one[0].Kind != "audio" || one[0].Bytes != 1000 || one[0].By != "alex" {
		t.Errorf("purge = %+v, want the audio row of 1000 bytes by alex", one[0])
	}
	if !one[0].At.Equal(at) {
		t.Errorf("purged at %v, want %v", one[0].At, at)
	}
}

// An empty list of events is a question with the answer "none", not an
// error and not every purge in the database.
func TestPurgedMediaOfNoEventsIsEmpty(t *testing.T) {
	f := newPurgeFixture(t)
	id := f.event(0, map[string]int64{"audio": 1000})
	if err := f.s.RecordPurge(ctx, []Purge{{EventID: id, Kind: "audio", Bytes: 1000, At: t0, By: "alex"}}); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.PurgedMedia(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("PurgedMedia(nil) = %v, want nothing", got)
	}
}

// One bad entry writes none of them. A half-written purge would say some
// files were deleted that still exist, or the other way round.
func TestRecordPurgeIsAllOrNothing(t *testing.T) {
	f := newPurgeFixture(t)
	id := f.event(0, map[string]int64{"audio": 1000})
	err := f.s.RecordPurge(ctx, []Purge{
		{EventID: id, Kind: "audio", Bytes: 1000, At: t0, By: "alex"},
		{EventID: id, Kind: "transcript", Bytes: 5, At: t0, By: "alex"},
	})
	if err == nil {
		t.Fatal("a purge of kind \"transcript\" was accepted")
	}
	var rows int
	f.s.r.QueryRowContext(ctx, `SELECT count(*) FROM media_purge`).Scan(&rows)
	if rows != 0 {
		t.Errorf("media_purge holds %d rows after a failed batch, want 0", rows)
	}
}

// A purge of an event that is not in the database is refused by the
// foreign key. Nothing may record the deletion of a recording that never
// existed.
func TestRecordPurgeRefusesAnUnknownEvent(t *testing.T) {
	f := newPurgeFixture(t)
	err := f.s.RecordPurge(ctx, []Purge{{EventID: 4242, Kind: "audio", Bytes: 1, At: t0, By: "alex"}})
	if err == nil {
		t.Fatal("a purge of event 4242 was accepted")
	}
}

// Recording the same purge twice is not an error, and does not double the
// bytes. The owner may select the same event again.
func TestRecordPurgeTwiceKeepsTheFirstRecord(t *testing.T) {
	f := newPurgeFixture(t)
	id := f.event(0, map[string]int64{"audio": 1000})
	first := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	later := first.Add(48 * time.Hour)
	if err := f.s.RecordPurge(ctx, []Purge{{EventID: id, Kind: "audio", Bytes: 1000, At: first, By: "alex"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordPurge(ctx, []Purge{{EventID: id, Kind: "audio", Bytes: 0, At: later, By: "someone"}}); err != nil {
		t.Fatalf("a second purge of the same clip failed: %v", err)
	}
	got, err := f.s.PurgedMedia(ctx, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[id]) != 1 {
		t.Fatalf("event %d has %d purge rows, want 1", id, len(got[id]))
	}
	if !got[id][0].At.Equal(first) || got[id][0].By != "alex" {
		t.Errorf("purge = %+v, want the first one, by alex", got[id][0])
	}
}

// A reader of one media row can tell a purged clip from a vanished one
// without a second query.
func TestMediaFileCarriesItsPurge(t *testing.T) {
	f := newPurgeFixture(t)
	id := f.event(0, map[string]int64{"audio": 1000, "video": 2000})
	at := time.Date(2026, 9, 12, 9, 15, 0, 0, time.UTC)
	if err := f.s.RecordPurge(ctx, []Purge{{EventID: id, Kind: "audio", Bytes: 1000, At: at, By: "alex"}}); err != nil {
		t.Fatal(err)
	}

	audio, err := f.s.MediaFile(ctx, id, "audio")
	if err != nil {
		t.Fatal(err)
	}
	if audio.Purged == nil {
		t.Fatal("the audio row does not say it was purged")
	}
	if audio.Purged.By != "alex" || !audio.Purged.At.Equal(at) {
		t.Errorf("purge = %+v, want alex at %v", *audio.Purged, at)
	}
	// The row keeps what the clip was. It is the record that it existed.
	if audio.Bytes != 1000 || audio.SHA256 != strings.Repeat("ab", 32) {
		t.Errorf("media row = %d bytes, sha %q; the purge changed it", audio.Bytes, audio.SHA256)
	}

	video, err := f.s.MediaFile(ctx, id, "video")
	if err != nil {
		t.Fatal(err)
	}
	if video.Purged != nil {
		t.Errorf("the video row says it was purged: %+v", *video.Purged)
	}

	all, err := f.s.EventMedia(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("EventMedia returned %d rows, want 2", len(all))
	}
	purged := 0
	for _, m := range all {
		if m.Purged != nil {
			purged++
			if m.Kind != "audio" {
				t.Errorf("the %s row says it was purged", m.Kind)
			}
		}
	}
	if purged != 1 {
		t.Errorf("%d of the rows say they were purged, want 1", purged)
	}
}

// The usage figures tell the owner what a purge would reclaim before they
// press anything.
func TestMediaSpaceCountsHeldPurgedAndPurgeable(t *testing.T) {
	f := newPurgeFixture(t)
	old := f.event(-72*time.Hour, map[string]int64{"audio": 1000, "video": 5_000_000})
	f.event(-1*time.Hour, map[string]int64{"audio": 2000})
	f.event(0, map[string]int64{"video": 6_000_000})

	space, err := f.s.MediaSpace(ctx, MediaFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if space.Audio.Files != 2 || space.Audio.Bytes != 3000 || space.Audio.Events != 2 {
		t.Errorf("audio = %+v, want 2 files, 3000 bytes, 2 events", space.Audio)
	}
	if space.Video.Files != 2 || space.Video.Bytes != 11_000_000 || space.Video.Events != 2 {
		t.Errorf("video = %+v, want 2 files, 11000000 bytes, 2 events", space.Video)
	}
	if space.Purged.Files != 0 || space.Purged.Bytes != 0 {
		t.Errorf("purged = %+v, want nothing", space.Purged)
	}
	if space.Purgeable.Files != 4 || space.Purgeable.Bytes != 11_003_000 || space.Purgeable.Events != 3 {
		t.Errorf("purgeable = %+v, want 4 files, 11003000 bytes, 3 events", space.Purgeable)
	}

	// Purging the old event moves its bytes out of what is held and into
	// what has been reclaimed.
	err = f.s.RecordPurge(ctx, []Purge{
		{EventID: old, Kind: "audio", Bytes: 1000, At: t0, By: "alex"},
		{EventID: old, Kind: "video", Bytes: 5_000_000, At: t0, By: "alex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	space, err = f.s.MediaSpace(ctx, MediaFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if space.Audio.Files != 1 || space.Audio.Bytes != 2000 {
		t.Errorf("audio after the purge = %+v, want 1 file and 2000 bytes", space.Audio)
	}
	if space.Video.Files != 1 || space.Video.Bytes != 6_000_000 {
		t.Errorf("video after the purge = %+v, want 1 file and 6000000 bytes", space.Video)
	}
	if space.Purged.Files != 2 || space.Purged.Bytes != 5_001_000 || space.Purged.Events != 1 {
		t.Errorf("purged = %+v, want 2 files, 5001000 bytes, 1 event", space.Purged)
	}
	if space.Purgeable.Files != 2 || space.Purgeable.Bytes != 6_002_000 {
		t.Errorf("purgeable after the purge = %+v, want 2 files and 6002000 bytes", space.Purgeable)
	}
}

// "Recordings older than 30 days" is a question about when the event
// started, and the answer has to be countable before anything is deleted.
func TestMediaSpaceNarrowsToEventsBeforeAnInstant(t *testing.T) {
	f := newPurgeFixture(t)
	f.event(-72*time.Hour, map[string]int64{"audio": 1000, "video": 5_000_000})
	f.event(-1*time.Hour, map[string]int64{"audio": 2000})
	f.event(time.Hour, map[string]int64{"audio": 4000})

	cut := t0.Add(-24 * time.Hour).UnixMilli()
	space, err := f.s.MediaSpace(ctx, MediaFilter{StartedBeforeMS: cut})
	if err != nil {
		t.Fatal(err)
	}
	if space.Purgeable.Files != 2 || space.Purgeable.Bytes != 5_001_000 || space.Purgeable.Events != 1 {
		t.Errorf("purgeable before the cut = %+v, want 2 files, 5001000 bytes, 1 event", space.Purgeable)
	}
	// The held figures are the whole store, whatever the cut is. They say
	// how much is on disk, not how much this filter would take.
	if space.Audio.Files != 3 || space.Audio.Bytes != 7000 {
		t.Errorf("audio = %+v, want 3 files and 7000 bytes", space.Audio)
	}
}

// The selection bar has to name the exact megabytes before the owner
// presses anything, so the figures narrow to the events they chose.
func TestMediaSpaceNarrowsToTheEventsChosen(t *testing.T) {
	f := newPurgeFixture(t)
	first := f.event(0, map[string]int64{"audio": 1000, "video": 5_000_000})
	f.event(time.Hour, map[string]int64{"audio": 2000})
	third := f.event(2*time.Hour, map[string]int64{"audio": 4000})

	space, err := f.s.MediaSpace(ctx, MediaFilter{EventIDs: []int64{first, third}})
	if err != nil {
		t.Fatal(err)
	}
	if space.Purgeable.Files != 3 || space.Purgeable.Bytes != 5_005_000 || space.Purgeable.Events != 2 {
		t.Errorf("purgeable = %+v, want 3 files, 5005000 bytes, 2 events", space.Purgeable)
	}
	// The held figures are still the whole store.
	if space.Audio.Files != 3 || space.Audio.Bytes != 7000 {
		t.Errorf("audio = %+v, want 3 files and 7000 bytes", space.Audio)
	}
}

// An age turns into the list a purge acts on, oldest first, so a limit
// takes the oldest recordings rather than an arbitrary handful.
func TestPurgeableEventsListsTheOldestFirst(t *testing.T) {
	f := newPurgeFixture(t)
	oldest := f.event(-72*time.Hour, map[string]int64{"audio": 1000})
	middle := f.event(-48*time.Hour, map[string]int64{"audio": 2000})
	f.event(time.Hour, map[string]int64{"audio": 4000})

	got, err := f.s.PurgeableEvents(ctx, MediaFilter{StartedBeforeMS: t0.UnixMilli()}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != oldest || got[1] != middle {
		t.Errorf("purgeable events = %v, want %d then %d", got, oldest, middle)
	}
	if got, err := f.s.PurgeableEvents(ctx, MediaFilter{StartedBeforeMS: t0.UnixMilli()}, 1, 0); err != nil {
		t.Fatal(err)
	} else if len(got) != 1 || got[0] != oldest {
		t.Errorf("with a limit of 1 = %v, want only %d", got, oldest)
	}
}

// An offset pages past events the caller has already looked at. Retention
// needs it: a file it could not delete keeps its event on this list for
// ever, and without an offset those events hold the front of every page
// and nothing newer is ever reached.
func TestPurgeableEventsPageWithAnOffset(t *testing.T) {
	f := newPurgeFixture(t)
	oldest := f.event(-72*time.Hour, map[string]int64{"audio": 1000})
	middle := f.event(-48*time.Hour, map[string]int64{"audio": 2000})
	newest := f.event(-24*time.Hour, map[string]int64{"audio": 3000})

	for _, c := range []struct {
		offset int
		want   []int64
	}{
		{0, []int64{oldest, middle}},
		{1, []int64{middle, newest}},
		{2, []int64{newest}},
		{3, nil},
		{99, nil},
	} {
		got, err := f.s.PurgeableEvents(ctx, MediaFilter{StartedBeforeMS: t0.UnixMilli()}, 2, c.offset)
		if err != nil {
			t.Fatalf("offset %d: %v", c.offset, err)
		}
		if len(got) != len(c.want) {
			t.Errorf("offset %d = %v, want %v", c.offset, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("offset %d = %v, want %v", c.offset, got, c.want)
				break
			}
		}
	}
}
