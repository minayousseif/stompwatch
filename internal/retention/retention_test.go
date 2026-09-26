package retention

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/store"
)

// now is the instant every test runs at. The cutoff for 90 days is
// 2026-06-15 03:00 UTC, and the tests place events around it.
var now = time.Date(2026, 9, 13, 3, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

type health struct{ kind, detail string }

// fixture is a store on a temporary directory with a clip root for each
// kind, and a job that reads them.
type fixture struct {
	t        *testing.T
	s        *store.Store
	dbPath   string
	clipDir  string
	videoDir string
	days     int
	job      *Job
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		t: t, dbPath: filepath.Join(dir, "noise.db"),
		clipDir: filepath.Join(dir, "clips", "audio"), videoDir: filepath.Join(dir, "clips", "video"),
		days: 90,
	}
	for _, d := range []string{f.clipDir, f.videoDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if f.s, err = store.Open(f.dbPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.s.Close() })
	f.job = &Job{
		Store: f.s, ClipDir: f.clipDir, VideoDir: f.videoDir,
		Days: func() int { return f.days },
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:  func() time.Time { return now },
	}
	return f
}

// event stores an event that started at now plus offset, with one clip
// file on disk per kind, and returns the event number and the paths. The
// stored path is absolute, as the collector writes it.
func (f *fixture) event(offset time.Duration, kinds ...string) (int64, map[string]string) {
	f.t.Helper()
	start := now.Add(offset)
	ev := detect.Event{
		Start: start, End: start.Add(2 * time.Second),
		LAeq: 60, LAmax: 65, BaselineAtTrigger: 33,
		LowBand: 55, HighBand: 40, LowHighRatioDB: 15,
		Class: detect.Jumping, Confidence: 0.8, Envelope: []float64{0.25, 0.5, 1},
	}
	id, err := f.s.InsertEvent(context.Background(), ev, start)
	if err != nil {
		f.t.Fatal(err)
	}
	paths := make(map[string]string, len(kinds))
	for _, kind := range kinds {
		root, ext, body := f.clipDir, ".wav", "nineteen bytes long"
		if kind == store.KindVideo {
			root, ext, body = f.videoDir, ".mp4", "a video clip that stands in for the camera"
		}
		path := filepath.Join(root, start.Format("2006/01/02"), strconv.FormatInt(id, 10)+ext)
		f.write(path, body)
		f.media(id, kind, path, int64(len(body)))
		paths[kind] = path
	}
	return id, paths
}

func (f *fixture) write(path, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) media(id int64, kind, path string, bytes int64) {
	f.t.Helper()
	err := f.s.InsertMedia(context.Background(), store.Media{
		EventID: id, Kind: kind, Path: path, Bytes: bytes,
		Duration: 10 * time.Second, SHA256: strings.Repeat("ab", 32),
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// count reads a row count through a read-only handle of its own, so the
// test does not depend on the store's own queries to say what it holds.
func (f *fixture) count(query string) int {
	f.t.Helper()
	db, err := sql.Open("sqlite", "file:"+f.dbPath+"?mode=ro")
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// records reads the health rows the run wrote, through a read-only handle
// of its own, so a test does not depend on the store's own queries to say
// what it holds.
func (f *fixture) records() []health {
	f.t.Helper()
	db, err := sql.Open("sqlite", "file:"+f.dbPath+"?mode=ro")
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT kind, detail FROM system_health ORDER BY id`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []health
	for rows.Next() {
		var h health
		if err := rows.Scan(&h.kind, &h.detail); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// The whole point of the job, and the whole risk of it: the files of old
// events go, and every row stays (SPEC.md section 3 rule 3, section 6).
func TestRunDeletesTheFilesAndKeepsEveryRow(t *testing.T) {
	f := newFixture(t)
	old, oldPaths := f.event(-120*day, store.KindAudio, store.KindVideo)
	older, olderPaths := f.event(-200*day, store.KindAudio)
	young, youngPaths := f.event(-10*day, store.KindAudio, store.KindVideo)
	f.s.SetReview(context.Background(), store.Review{
		EventID: old, Status: store.StatusVerified, Note: "heard it", Reviewer: "alex", At: now,
	})
	bins := []meter.Bin{}
	for sec := 0; sec < 5; sec++ {
		bins = append(bins, meter.Bin{
			Start: now.Add(-120*day + time.Duration(sec)*time.Second), Samples: 48000,
			LAeq: 60, LAmax: 65, LowBand: 55, HighBand: 40, Baseline: 33,
		})
	}
	if _, err := f.s.InsertBins(context.Background(), bins); err != nil {
		t.Fatal(err)
	}
	// The segment ring lives under video_dir and is not a clip. Retention
	// must never reach it.
	ring := filepath.Join(f.videoDir, "ring", "seg-000.ts")
	f.write(ring, "a ring segment")

	before := map[string]int{}
	for _, tbl := range []string{"events", "samples_1s", "event_media", "event_review"} {
		before[tbl] = f.count("SELECT count(*) FROM " + tbl)
	}
	if before["events"] != 3 || before["samples_1s"] != 5 || before["event_media"] != 5 || before["event_review"] != 1 {
		t.Fatalf("the fixture holds %v rows; the counts below would prove nothing", before)
	}

	got := f.job.Run(context.Background())

	if got.Files != 3 || got.Bytes != 19+42+19 {
		t.Errorf("Run = %+v, want 3 files and 80 bytes", got)
	}
	for kind, path := range oldPaths {
		if exists(path) {
			t.Errorf("the %s file of the 120-day-old event is still on disk", kind)
		}
	}
	if exists(olderPaths[store.KindAudio]) {
		t.Error("the audio file of the 200-day-old event is still on disk")
	}
	for kind, path := range youngPaths {
		if !exists(path) {
			t.Errorf("the %s file of the 10-day-old event was deleted", kind)
		}
	}
	if !exists(ring) {
		t.Error("the ring segment was deleted")
	}

	for tbl, want := range before {
		if n := f.count("SELECT count(*) FROM " + tbl); n != want {
			t.Errorf("%s holds %d rows after the run, had %d", tbl, n, want)
		}
	}
	if n := f.count(`SELECT count(*) FROM media_purge WHERE purged_by = 'retention'`); n != 3 {
		t.Errorf("media_purge holds %d rows by retention, want 3", n)
	}
	if n := f.count(`SELECT count(*) FROM media_purge WHERE event_id = ` + strconv.FormatInt(young, 10)); n != 0 {
		t.Errorf("the 10-day-old event has %d purge rows, want none", n)
	}
	// The store's own view agrees: the media rows are still there with
	// what the collector wrote, and each says it was purged, by retention,
	// at the instant of the run.
	rows, err := f.s.EventMedia(context.Background(), old)
	if err != nil || len(rows) != 2 {
		t.Fatalf("EventMedia = %d rows, %v; want 2", len(rows), err)
	}
	for _, m := range rows {
		if m.Purged == nil {
			t.Errorf("the %s row does not say it was purged", m.Kind)
			continue
		}
		if m.Purged.By != "retention" || !m.Purged.At.Equal(now) {
			t.Errorf("the %s row was purged by %q at %v, want retention at %v", m.Kind, m.Purged.By, m.Purged.At, now)
		}
		if m.SHA256 != strings.Repeat("ab", 32) {
			t.Errorf("the %s row lost its hash", m.Kind)
		}
	}

	// One health row for the run, naming retention, the events and the
	// bytes (SPEC.md section 3 rule 3).
	recs := f.records()
	if len(recs) != 1 {
		t.Fatalf("health records = %v, want exactly one", recs)
	}
	if recs[0].kind != "media_purge" {
		t.Errorf("health kind = %q, want media_purge", recs[0].kind)
	}
	for _, want := range []string{"retention", "90 days", strconv.FormatInt(old, 10), strconv.FormatInt(older, 10), "80 bytes"} {
		if !strings.Contains(recs[0].detail, want) {
			t.Errorf("the health detail %q does not say %q", recs[0].detail, want)
		}
	}
	// The event numbers are the list between "from events" and the bytes.
	// "3 recordings" also holds a 3, so the list is read on its own.
	named := namedEvents(t, recs[0].detail)
	for _, id := range []int64{old, older} {
		if !named[strconv.FormatInt(id, 10)] {
			t.Errorf("the health detail %q does not list event %d", recs[0].detail, id)
		}
	}
	if named[strconv.FormatInt(young, 10)] {
		t.Errorf("the health detail %q lists the event that was kept", recs[0].detail)
	}
}

// namedEvents reads the event numbers out of a purge detail.
func namedEvents(t *testing.T, detail string) map[string]bool {
	t.Helper()
	_, rest, ok := strings.Cut(detail, "from events ")
	if !ok {
		_, rest, ok = strings.Cut(detail, "from event ")
	}
	list, _, ok2 := strings.Cut(rest, ", reclaiming")
	if !ok || !ok2 {
		t.Fatalf("the detail %q does not list events", detail)
	}
	out := map[string]bool{}
	for _, name := range strings.Split(list, ", ") {
		out[name] = true
	}
	return out
}

// retention_days = 0 keeps every recording forever. The job must not
// even compute a cutoff: a cutoff of the epoch would match everything.
func TestRunWithZeroDaysDeletesNothing(t *testing.T) {
	f := newFixture(t)
	f.days = 0
	_, paths := f.event(-3000*day, store.KindAudio, store.KindVideo)
	_, more := f.event(-1*day, store.KindAudio)

	got := f.job.Run(context.Background())

	if got.Files != 0 || got.Bytes != 0 {
		t.Errorf("Run = %+v, want nothing deleted", got)
	}
	for kind, path := range paths {
		if !exists(path) {
			t.Errorf("the %s file of the 3000-day-old event was deleted with retention off", kind)
		}
	}
	if !exists(more[store.KindAudio]) {
		t.Error("the audio file of the 1-day-old event was deleted with retention off")
	}
	if n := f.count(`SELECT count(*) FROM media_purge`); n != 0 {
		t.Errorf("media_purge holds %d rows, want none", n)
	}
	if recs := f.records(); len(recs) != 0 {
		t.Errorf("health records = %v, want none", recs)
	}
}

// The cutoff is now minus retention_days. An event that started before it
// loses its files; one that started at it, or after it, keeps them. The
// boundary is tested to the millisecond because started_ms is stored to
// the millisecond.
func TestRunKeepsEveryEventInsideTheWindow(t *testing.T) {
	f := newFixture(t)
	window := -90 * day
	before, beforePaths := f.event(window-time.Millisecond, store.KindAudio)
	at, atPaths := f.event(window, store.KindAudio)
	after, afterPaths := f.event(window+time.Millisecond, store.KindAudio)

	got := f.job.Run(context.Background())

	if got.Files != 1 {
		t.Errorf("Run = %+v, want exactly 1 file", got)
	}
	if exists(beforePaths[store.KindAudio]) {
		t.Errorf("event %d started a millisecond before the cutoff and kept its file", before)
	}
	if !exists(atPaths[store.KindAudio]) {
		t.Errorf("event %d started exactly at the cutoff and lost its file", at)
	}
	if !exists(afterPaths[store.KindAudio]) {
		t.Errorf("event %d started a millisecond after the cutoff and lost its file", after)
	}
	for _, id := range []int64{at, after} {
		if n := f.count(`SELECT count(*) FROM media_purge WHERE event_id = ` + strconv.FormatInt(id, 10)); n != 0 {
			t.Errorf("event %d inside the window has %d purge rows", id, n)
		}
	}
}

// A stored path that leads outside its media root is refused, never
// deleted. Only the collector writes these rows, so this is the second
// line of defense a restored or imported database needs, and for a job
// that runs unattended it is the one that matters most.
func TestRunRefusesAPathOutsideTheMediaRoot(t *testing.T) {
	f := newFixture(t)
	outsideClips := filepath.Join(filepath.Dir(f.clipDir), "secret.wav")
	f.write(outsideClips, "not for the job")
	outsideVideo := filepath.Join(filepath.Dir(f.videoDir), "secret.mp4")
	f.write(outsideVideo, "not for the job either")

	// Two shapes of escape, because they fail differently: a relative path
	// that climbs out with .., and an absolute path that was never under
	// the root at all. The absolute one really points at the file on disk,
	// so a check that is not there deletes it.
	var ids []int64
	for i, stored := range [][2]string{
		{filepath.Join("..", "secret.wav"), filepath.Join("..", "secret.mp4")},
		{outsideClips, outsideVideo},
	} {
		id, _ := f.event(-100*day - time.Duration(i)*time.Minute)
		f.media(id, store.KindAudio, stored[0], 15)
		f.media(id, store.KindVideo, stored[1], 22)
		ids = append(ids, id)
	}
	// A well-formed old event beside them, so the test shows the job
	// carried on past the refusals.
	fine, finePaths := f.event(-100*day, store.KindAudio)

	got := f.job.Run(context.Background())

	if got.Refused != 4 {
		t.Errorf("Run = %+v, want 4 refused", got)
	}
	for _, path := range []string{outsideClips, outsideVideo} {
		if !exists(path) {
			t.Errorf("%s was deleted from outside the media root", filepath.Base(path))
		}
	}
	for _, id := range ids {
		if n := f.count(`SELECT count(*) FROM media_purge WHERE event_id = ` + strconv.FormatInt(id, 10)); n != 0 {
			t.Errorf("event %d has %d purge rows; a refusal must not be recorded as a purge", id, n)
		}
	}
	if exists(finePaths[store.KindAudio]) {
		t.Errorf("event %d was inside the root and old, and kept its file", fine)
	}
	if got.Files != 1 {
		t.Errorf("Run = %+v, want 1 file", got)
	}
}

// A run that deletes nothing writes nothing: a daily "deleted 0 files" row
// is how somebody learns to stop reading the health log. A clip already
// recorded as purged is not recorded again either.
func TestRunWritesNoHealthRowWhenNothingIsDeleted(t *testing.T) {
	f := newFixture(t)
	f.event(-10*day, store.KindAudio)
	id, paths := f.event(-100*day, store.KindAudio)
	if err := os.Remove(paths[store.KindAudio]); err != nil {
		t.Fatal(err)
	}
	err := f.s.RecordPurge(context.Background(), []store.Purge{
		{EventID: id, Kind: store.KindAudio, Bytes: 19, At: now.Add(-day), By: "alex"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := f.job.Run(context.Background())

	if got.Files != 0 {
		t.Errorf("Run = %+v, want nothing deleted", got)
	}
	if recs := f.records(); len(recs) != 0 {
		t.Errorf("health records = %v, want none", recs)
	}
	rows, err := f.s.EventMedia(context.Background(), id)
	if err != nil || len(rows) != 1 || rows[0].Purged == nil || rows[0].Purged.By != "alex" {
		t.Errorf("the owner's purge record was replaced: %+v, %v", rows, err)
	}
}

// A file that will not delete is one logged line and a skipped file, not
// a crash and not a purge record. The run carries on to the next file.
func TestRunSkipsAFileThatWillNotDelete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root deletes from a read-only directory, so the failure cannot be staged")
	}
	f := newFixture(t)
	// The two events are on different days, so they live in different
	// directories and only one directory is made read-only.
	stuck, stuckPaths := f.event(-100*day, store.KindAudio)
	_, finePaths := f.event(-99*day, store.KindAudio)
	locked := filepath.Dir(stuckPaths[store.KindAudio])
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o750) })

	got := f.job.Run(context.Background())

	if got.Failed != 1 || got.Files != 1 {
		t.Errorf("Run = %+v, want 1 failed and 1 deleted", got)
	}
	if !exists(stuckPaths[store.KindAudio]) {
		t.Fatal("the file in the read-only directory is gone; the failure was not staged")
	}
	if n := f.count(`SELECT count(*) FROM media_purge WHERE event_id = ` + strconv.FormatInt(stuck, 10)); n != 0 {
		t.Errorf("the file that would not delete has %d purge rows", n)
	}
	if exists(finePaths[store.KindAudio]) {
		t.Error("the file after the failure was not deleted")
	}
}

// With video off there is no video root, and a video row is left alone
// rather than resolved against nothing. The audio still goes.
func TestRunLeavesVideoAloneWithNoVideoDir(t *testing.T) {
	f := newFixture(t)
	f.job.VideoDir = ""
	id, paths := f.event(-100*day, store.KindAudio, store.KindVideo)

	got := f.job.Run(context.Background())

	if got.Files != 1 || got.Skipped != 1 {
		t.Errorf("Run = %+v, want 1 file and 1 skipped", got)
	}
	if exists(paths[store.KindAudio]) {
		t.Error("the audio file is still on disk")
	}
	if !exists(paths[store.KindVideo]) {
		t.Error("the video file was deleted with no video root to check it against")
	}
	if n := f.count(`SELECT count(*) FROM media_purge WHERE event_id = ` + strconv.FormatInt(id, 10)); n != 1 {
		t.Errorf("event %d has %d purge rows, want 1 for the audio", id, n)
	}
}

// refused stores an event with a media row whose path is outside the clip
// root and no file on disk. Retention refuses it and records no purge, so
// it stays on the purgeable list forever. A box gets rows like these by
// moving its clips to a new disk and changing clip_dir.
func (f *fixture) refused(offset time.Duration) int64 {
	f.t.Helper()
	start := now.Add(offset)
	ev := detect.Event{
		Start: start, End: start.Add(time.Second),
		LAeq: 60, LAmax: 65, BaselineAtTrigger: 33,
		LowBand: 55, HighBand: 40, LowHighRatioDB: 15,
		Class: detect.Jumping, Confidence: 0.8, Envelope: []float64{1},
	}
	id, err := f.s.InsertEvent(context.Background(), ev, start)
	if err != nil {
		f.t.Fatal(err)
	}
	f.media(id, store.KindAudio, filepath.Join(f.t.TempDir(), "elsewhere.wav"), 19)
	return id
}

// A file retention cannot delete keeps its event on the purgeable list for
// ever. Those events are the oldest, so they hold the front of the list.
// A run that took one page of that list would work through the same stuck
// events every day and never reach anything newer, and the disk would fill
// while the log said the next run continues.
func TestRunReachesPastEventsItCannotDelete(t *testing.T) {
	f := newFixture(t)
	const stuck = 600
	if batchSize >= stuck {
		t.Fatalf("one query asks for %d events; this test must hold more than one page", batchSize)
	}
	for i := 0; i < stuck; i++ {
		f.refused(-300 * day)
	}
	good, paths := f.event(-120*day, store.KindAudio, store.KindVideo)

	got := f.job.Run(context.Background())

	if got.Refused != stuck {
		t.Errorf("Refused = %d, want %d", got.Refused, stuck)
	}
	if got.Files != 2 {
		t.Fatalf("Run = %+v, want the 2 files of the one event it can delete", got)
	}
	for kind, path := range paths {
		if exists(path) {
			t.Errorf("the %s file of event %d was not deleted", kind, good)
		}
	}
	if got.More {
		t.Error("More is true, but the run reached the end of the list")
	}
}

// The health row goes into the database itself, in the same breath as the
// purge rows. Handing it to the pipeline would not do: a run can outlive
// the pipeline by a few seconds at shutdown, and then rule 3's "every
// deletion is logged" would rest on a log line that rotates away.
func TestTheHealthRowIsWrittenToTheDatabase(t *testing.T) {
	f := newFixture(t)
	f.event(-120*day, store.KindAudio)

	f.job.Run(context.Background())

	if got := f.count("SELECT count(*) FROM system_health WHERE kind = 'media_purge'"); got != 1 {
		t.Errorf("media_purge rows in system_health = %d, want 1", got)
	}
}

// The purge rows and the health row are written on a context a shutdown
// cannot cancel. By the time record runs the files are already gone, and a
// deleted file with no purge row is the one outcome rule 3 of SPEC.md
// section 3 forbids: the clip would then read as lost rather than deleted.
func TestTheRecordIsWrittenEvenWhenTheContextIsAlreadyCancelled(t *testing.T) {
	f := newFixture(t)
	id, _ := f.event(-120*day, store.KindAudio)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f.job.record(ctx, []store.Purge{{
		EventID: id, Kind: store.KindAudio, Bytes: 19, At: now, By: By,
	}}, Result{Days: 90, Files: 1, Bytes: 19})

	if got := f.count("SELECT count(*) FROM media_purge"); got != 1 {
		t.Errorf("media_purge holds %d rows, want 1", got)
	}
	recs := f.records()
	if len(recs) != 1 || recs[0].kind != "media_purge" {
		t.Errorf("health records = %v, want one media_purge row", recs)
	}
}

// The next run reaches what the last one could not delete, and past it.
// A stuck event is tried again, in case whatever stopped it was fixed, and
// it must not stop the run a second time either.
func TestTheNextRunReachesPastTheSameStuckEvents(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		f.refused(-300 * day)
	}
	first, firstPaths := f.event(-120*day, store.KindAudio)
	second, secondPaths := f.event(-91*day, store.KindAudio)

	one := f.job.Run(context.Background())
	if one.Files != 2 || one.Refused != 3 {
		t.Fatalf("the first run = %+v, want 2 files and 3 refused", one)
	}

	two := f.job.Run(context.Background())

	if two.Refused != 3 {
		t.Errorf("the second run refused %d, want the same 3", two.Refused)
	}
	if two.Files != 0 {
		t.Errorf("the second run deleted %d files, want none left to delete", two.Files)
	}
	if exists(firstPaths[store.KindAudio]) || exists(secondPaths[store.KindAudio]) {
		t.Errorf("events %d and %d still have their files", first, second)
	}
	if got := f.count("SELECT count(*) FROM media_purge"); got != 2 {
		t.Errorf("media_purge holds %d rows, want 2; a second run must not record a file twice", got)
	}
}

// A cutoff of exactly zero is no filter at all in the store, so it would
// take every recording there is, whatever its age. Only a clock reading
// 1970 can land there, and then the answer is to do nothing: 90 days
// before this instant is the epoch to the millisecond.
func TestARunWithACutoffAtTheEpochDeletesNothing(t *testing.T) {
	f := newFixture(t)
	_, paths := f.event(-300*day, store.KindAudio)
	epochPlus90 := time.Unix(0, 0).UTC().Add(90 * day)
	f.job.Now = func() time.Time { return epochPlus90 }

	got := f.job.Run(context.Background())

	if got.Files != 0 {
		t.Errorf("Run = %+v, want nothing deleted", got)
	}
	if !exists(paths[store.KindAudio]) {
		t.Error("the clip was deleted with a cutoff before the epoch")
	}
	if n := f.count("SELECT count(*) FROM media_purge"); n != 0 {
		t.Errorf("media_purge holds %d rows, want none", n)
	}
}

// retention_days is read at each run, so a change from the dashboard
// applies at the next run with no restart.
func TestTheNumberOfDaysIsReadAtEveryRun(t *testing.T) {
	f := newFixture(t)
	_, paths := f.event(-40*day, store.KindAudio)

	if got := f.job.Run(context.Background()); got.Files != 0 {
		t.Fatalf("at 90 days the run = %+v, want nothing deleted", got)
	}
	f.days = 30

	if got := f.job.Run(context.Background()); got.Files != 1 {
		t.Errorf("at 30 days the run = %+v, want the one clip deleted", got)
	}
	if exists(paths[store.KindAudio]) {
		t.Error("the 40-day-old clip is still there at 30 days")
	}
}
