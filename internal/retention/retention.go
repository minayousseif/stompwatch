package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/minayousseif/stompwatch/internal/media"
	"github.com/minayousseif/stompwatch/internal/store"
)

// By is what a purge row says when retention deleted the file. It is not
// a login: nobody asked for this one file, the owner set a number of days.
const By = "retention"

// batchSize is how many events one query asks for. A run pages through
// the list rather than taking the front of it once, because an event whose
// file could not be deleted keeps no purge row and so stays on the list.
// Those events are the oldest, so they hold the front of every page; a run
// that stopped at one page would work through the same stuck events every
// day and never reach anything newer.
const batchSize = 500

// maxFilesPerRun is as many files as one run deletes. A run that reaches
// the limit says so and leaves the rest for the next one, which is the day
// after. On a box that has never run retention, that is a few thousand
// files a day until it catches up.
const maxFilesPerRun = 5000

// maxEventsPerRun is as many events as one run looks at, the stuck ones
// counted. It bounds the work when a great many rows cannot be deleted, so
// a run stays a run and not a morning of failing deletes.
const maxEventsPerRun = 50000

// maxReportsPerRun is how many refusals, failures or skipped rows one run
// spells out in the log before it stops repeating itself. The totals are
// in the line at the end of the run either way.
const maxReportsPerRun = 5

// recordBudget is how long the job gives itself to record the purges once
// the files are gone. It runs on a context of its own, because a shutdown
// in the middle of a run must not leave deleted files with no record of
// the deletion, which is the one outcome rule 3 forbids.
const recordBudget = 30 * time.Second

// Job is one retention pass, built once and run on a schedule.
type Job struct {
	Store *store.Store
	// ClipDir and VideoDir are the roots the audio and the video clips live
	// under. A stored path outside its root is refused, not deleted. An
	// empty VideoDir means video is off; then a video row is left alone,
	// not resolved against nothing.
	ClipDir  string
	VideoDir string
	// Days reads retention_days from the settings in force. It is read at
	// each run, so a change from the dashboard applies at the next run with
	// no restart. 0 or less means keep every recording forever, and then
	// Run does nothing at all.
	Days func() int
	Log  *slog.Logger     // nil means slog.Default()
	Now  func() time.Time // nil means time.Now
}

// Result is what one run did, for the log and for tests.
type Result struct {
	Days   int
	Cutoff time.Time
	// Files and Bytes are what was recorded as purged. A file that was
	// already gone counts as a file with no bytes, as in the owner's purge:
	// what is recorded is the decision, not the free space.
	Files int
	Bytes int64
	// Refused counts stored paths that lead outside their media root.
	// Failed counts files the operating system would not delete. Skipped
	// counts video rows left alone because there is no video root. None of
	// the three is recorded as a purge, because the file is still there.
	Refused int
	Failed  int
	Skipped int
	// More is true when more events were past the cutoff than one run
	// takes. The next run continues.
	More bool
}

func (j *Job) log() *slog.Logger {
	if j.Log == nil {
		return slog.Default()
	}
	return j.Log
}

func (j *Job) now() time.Time {
	if j.Now == nil {
		return time.Now()
	}
	return j.Now()
}

// Run does one pass. It never returns an error and never panics on a file:
// retention must not take the collector down. What went wrong is in the
// log and in the Result.
func (j *Job) Run(ctx context.Context) Result {
	days := j.Days()
	if days <= 0 {
		return Result{Days: days}
	}
	now := j.now()
	out := Result{Days: days, Cutoff: now.Add(-time.Duration(days) * 24 * time.Hour)}
	cutoffMS := out.Cutoff.UnixMilli()
	if cutoffMS <= 0 {
		// A zero StartedBeforeMS is no filter at all, which would take
		// every recording there is. A cutoff at or before the epoch cannot
		// happen with a sane clock; if it does, do nothing and say so.
		j.log().Error("retention did not run: the clock gives a cutoff before 1970", "now", now)
		return out
	}

	// The files go first, with no transaction open. The purges are
	// recorded after, in one transaction, on a context that a shutdown
	// cannot cancel: the files are already gone by then.
	//
	// The list is read a page at a time, oldest first. Nothing gains a
	// purge row until the run is over, so the page after the one just read
	// starts where that one ended, stuck events and all.
	var record []store.Purge
	seen := 0
	for out.Files < maxFilesPerRun && seen < maxEventsPerRun {
		ids, err := j.Store.PurgeableEvents(ctx,
			store.MediaFilter{StartedBeforeMS: cutoffMS}, batchSize, seen)
		if err != nil {
			if ctx.Err() != nil {
				// The collector is shutting down. That is not a fault, and
				// an error line at every stop teaches somebody to stop
				// reading the log.
				j.log().Info("retention stopped because the collector is shutting down")
				out.More = true
				break
			}
			j.log().Error("retention could not list the recordings past the cutoff", "err", err)
			break
		}
		if len(ids) == 0 {
			break // the end of the list
		}
		stopped := false
		for _, id := range ids {
			seen++
			if ctx.Err() != nil {
				j.log().Info("retention stopped because the collector is shutting down",
					"files_deleted", out.Files)
				out.More, stopped = true, true
				break
			}
			j.event(ctx, id, now, &record, &out)
			if out.Files >= maxFilesPerRun {
				out.More, stopped = true, true
				break
			}
		}
		if stopped || len(ids) < batchSize {
			break
		}
	}
	if seen >= maxEventsPerRun {
		out.More = true
	}

	if len(record) > 0 {
		j.record(ctx, record, out)
	}
	if out.Skipped > 0 {
		j.log().Warn("retention left video recordings alone because there is no video directory to check them against",
			"files", out.Skipped)
	}
	if out.Refused > 0 || out.Failed > 0 {
		j.log().Warn("retention could not delete every recording past the cutoff; "+
			"those events stay on the list and are tried again at the next run",
			"refused", out.Refused, "failed", out.Failed, "events_looked_at", seen)
	}
	if out.More {
		j.log().Info("more recordings are past the cutoff than one run takes; the next run continues",
			"events_looked_at", seen, "files_deleted", out.Files)
	}
	return out
}

// event takes the files of one event, adding what it deleted to record and
// counting what it could not in out.
func (j *Job) event(ctx context.Context, id int64, now time.Time, record *[]store.Purge, out *Result) {
	rows, err := j.Store.EventMedia(ctx, id)
	if err != nil {
		if ctx.Err() == nil {
			j.log().Error("retention could not read the clips of an event", "event", id, "err", err)
		}
		out.Failed++
		return
	}
	for _, m := range rows {
		if m.Purged != nil {
			continue
		}
		root := j.root(m.Kind)
		if root == "" {
			out.Skipped++
			continue
		}
		got, err := media.Remove(root, m.Path)
		switch {
		case errors.Is(err, media.ErrOutsideRoot):
			out.Refused++
			if out.Refused <= maxReportsPerRun {
				j.log().Error("a media row names a path outside its directory, so retention did not delete it",
					"event", id, "kind", m.Kind, "err", err)
			}
			continue
		case err != nil:
			out.Failed++
			if out.Failed <= maxReportsPerRun {
				j.log().Error("retention could not delete a clip file", "event", id, "kind", m.Kind, "err", err)
			}
			continue
		}
		*record = append(*record, store.Purge{
			EventID: id, Kind: m.Kind, Bytes: got.Bytes, At: now, By: By,
		})
		out.Files++
		out.Bytes += got.Bytes
	}
}

// root is the directory a kind of media lives under, or empty when there
// is none.
func (j *Job) root(kind string) string {
	if kind == store.KindVideo {
		return j.VideoDir
	}
	return j.ClipDir
}

// record writes the purge rows and the one health row for the run, on a
// context that a shutdown cannot cancel: the files are already gone by the
// time it runs. A record that cannot be written is the loudest thing this
// package can say: the files are gone and the database still claims them.
func (j *Job) record(ctx context.Context, record []store.Purge, out Result) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordBudget)
	defer cancel()
	who := fmt.Sprintf("%s at %d days", By, out.Days)
	detail := media.PurgeDetail(who, record, out.Bytes)
	if err := j.Store.RecordPurge(rctx, record); err != nil {
		j.log().Error("retention deleted clip files but could not record the purge; "+
			"the database still claims files that are gone",
			"files", out.Files, "bytes", out.Bytes, "err", err)
		j.health(rctx, store.HealthWriteError,
			"retention could not record a purge: "+detail+": "+err.Error())
		return
	}
	j.log().Info("retention deleted clip files", "days", out.Days, "cutoff", out.Cutoff,
		"files", out.Files, "bytes", out.Bytes, "refused", out.Refused, "failed", out.Failed)
	j.health(rctx, store.HealthMediaPurge, detail)
}

// health writes one system_health row. It goes into the database here
// rather than through the pipeline, on the same context the purge rows
// use: a run can outlive the pipeline by a few seconds at shutdown, and a
// deletion whose only record is a log line is not the logged deletion
// rule 3 of SPEC.md section 3 asks for.
func (j *Job) health(ctx context.Context, kind, detail string) {
	if err := j.Store.AddHealth(ctx, j.now(), kind, detail, 0); err != nil {
		j.log().Error("retention could not write its health record", "kind", kind, "err", err)
	}
}
