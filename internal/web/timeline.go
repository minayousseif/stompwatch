package web

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/minayousseif/stompwatch/internal/store"
)

const (
	// secondRange is the widest range the seconds are read for. Beyond it the
	// minute rollup answers instead, so a wide view never scans the whole
	// second table. A day covers any one night without the resolution
	// changing in the middle of it.
	secondRange = 24 * time.Hour
	// maxPoints is how many points one timeline may carry.
	maxPoints = 5000
	// maxTimelineEvents bounds the markers on one timeline.
	maxTimelineEvents = 5000
	// maxRange is the widest range the timeline answers at all. Past a month
	// even the widest bucket stops answering the question the owner asked.
	maxRange = 31 * 24 * time.Hour
	// maxQuietRange is the widest range a quiet-hours question may cover.
	// The answer is built from one span per night, so the range bounds the
	// work. Ten years is more than the box will ever hold.
	maxQuietRange = 3660 * 24 * time.Hour
)

// bucketMinutes are the bucket widths the timeline may thin minutes into.
// They are whole divisors of an hour or whole hours, so a bucket boundary
// falls on a time a person recognizes.
var bucketMinutes = []int64{1, 2, 5, 10, 15, 30, 60, 120, 240, 360, 720, 1440}

// bucketSeconds are the bucket widths the timeline may thin seconds into.
// They are whole divisors of a minute or whole minutes, so a bucket boundary
// falls on a time a person recognizes.
var bucketSeconds = []int64{1, 2, 5, 10, 15, 30, 60, 120, 300}

// chooseBucket returns the smallest bucket, in minutes, that brings a range
// of whole minutes down to maxPoints points. It returns the widest bucket if
// none fits, which the range limit already rules out.
func chooseBucket(minutes int64) int64 { return smallestBucket(minutes, bucketMinutes) }

// chooseSecondBucket does the same for a range of whole seconds.
func chooseSecondBucket(seconds int64) int64 { return smallestBucket(seconds, bucketSeconds) }

func smallestBucket(units int64, widths []int64) int64 {
	for _, b := range widths {
		if (units+b-1)/b <= maxPoints {
			return b
		}
	}
	return widths[len(widths)-1]
}

// thin groups minute rows into buckets of bucket minutes. Inside a bucket
// laeq is the energy mean of the minute means weighted by the seconds behind
// each minute, and lamax is the loudest second of any of them.
//
// The arithmetic is done here rather than in SQL because SQLite's log and
// power functions are a compile-time option and may not be there.
//
// A bucket with no rows is left out. The trace must show a gap where the
// measurement stopped, not a straight line across it.
func thin(rows []store.MinutePoint, bucket int64) []pointJSON {
	width := bucket * 60_000
	out := []pointJSON{}
	for i := 0; i < len(rows); {
		start := floorDiv(rows[i].TMS, width) * width
		var energy, seconds float64
		lamax := math.Inf(-1)
		j := i
		for ; j < len(rows) && rows[j].TMS < start+width; j++ {
			// The stored mean is already an energy mean of its n seconds, so
			// multiplying by n gives that minute's energy back.
			n := float64(rows[j].N)
			energy += math.Pow(10, rows[j].LAeq/10) * n
			seconds += n
			lamax = math.Max(lamax, rows[j].LAmax)
		}
		i = j
		if seconds == 0 {
			continue // a minute row with no seconds behind it measured nothing
		}
		// A minute rollup keeps no baseline, and inventing one would be a
		// measurement nobody made.
		out = append(out, pointJSON{T: start, LAeq: 10 * math.Log10(energy/seconds), LAmax: lamax})
	}
	return out
}

// floorDiv rounds towards minus infinity, so a bucket before the epoch holds
// the same minutes as one after it.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// thinSeconds groups second rows into buckets of bucket seconds. Inside a
// bucket laeq and the baseline are energy means, and lamax is the loudest
// second. The baseline is averaged the same way as the level so there is one
// rule to explain; it moves slowly enough that the choice changes it by far
// less than the instrument can resolve.
//
// A bucket with no rows is left out, for the same reason a minute bucket is.
func thinSeconds(rows []store.Point, bucket int64) []pointJSON {
	if bucket == 1 {
		out := make([]pointJSON, len(rows))
		for i, pt := range rows {
			out[i] = pointJSON{T: pt.TMS, LAeq: pt.LAeq, LAmax: pt.LAmax, Baseline: pt.Baseline}
		}
		return out
	}
	width := bucket * 1000
	out := []pointJSON{}
	for i := 0; i < len(rows); {
		start := floorDiv(rows[i].TMS, width) * width
		var energy, baseEnergy float64
		var n, baseN int
		lamax := math.Inf(-1)
		j := i
		for ; j < len(rows) && rows[j].TMS < start+width; j++ {
			energy += math.Pow(10, rows[j].LAeq/10)
			lamax = math.Max(lamax, rows[j].LAmax)
			n++
			if b := rows[j].Baseline; b != nil {
				baseEnergy += math.Pow(10, *b/10)
				baseN++
			}
		}
		i = j
		// Every bucket here holds at least the row that opened it, so unlike
		// the minute rollup there is no empty bucket to leave out. A bucket
		// with nothing in it simply never starts, which is the gap.
		pt := pointJSON{T: start, LAeq: 10 * math.Log10(energy/float64(n)), LAmax: lamax}
		if baseN > 0 {
			b := 10 * math.Log10(baseEnergy/float64(baseN))
			pt.Baseline = &b
		}
		out = append(out, pt)
	}
	return out
}

type pointJSON struct {
	T        int64    `json:"t"`
	LAeq     float64  `json:"laeq"`
	LAmax    float64  `json:"lamax"`
	Baseline *float64 `json:"baseline"`
}

type markerJSON struct {
	ID        int64   `json:"id"`
	StartedMS int64   `json:"started_ms"`
	EndedMS   int64   `json:"ended_ms"`
	LAmax     float64 `json:"lamax"`
	Class     string  `json:"class"`
	Status    string  `json:"status"`
	Muted     bool    `json:"muted"`
}

type spanJSON struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

type timelineJSON struct {
	Resolution string       `json:"resolution"`
	BucketMS   int64        `json:"bucket_ms"`
	From       int64        `json:"from"`
	To         int64        `json:"to"`
	Points     []pointJSON  `json:"points"`
	Events     []markerJSON `json:"events"`
	Quiet      []spanJSON   `json:"quiet"`
}

// rangeSpan is the length of [from, to], both in epoch milliseconds. It
// saturates rather than wraps, so an absurd range reads as a long one and is
// refused. The caller has already checked that to is not before from.
func rangeSpan(from, to int64) time.Duration {
	const most = math.MaxInt64 / int64(time.Millisecond)
	d := to - from
	if d < 0 || d > most { // d < 0 only when the subtraction wrapped
		return math.MaxInt64
	}
	return time.Duration(d) * time.Millisecond
}

// quietRangeFits refuses a quiet-hours question over more than ten years.
// Without it a range of millions of days would build millions of spans.
func quietRangeFits(w http.ResponseWriter, from, to int64) bool {
	if rangeSpan(from, to) > maxQuietRange {
		fail(w, http.StatusBadRequest,
			"a quiet-hours question covers at most 3660 days. Ask for a narrower range.")
		return false
	}
	return true
}

// rangeFits refuses a range wider than a month and says so. The PNG export
// asks the same question, so the answer lives in one place.
func (s *Server) rangeFits(w http.ResponseWriter, span time.Duration) bool {
	if span > maxRange {
		fail(w, http.StatusBadRequest,
			"that range is longer than 31 days. Ask for a narrower one.")
		return false
	}
	return true
}

// timelinePoints reads a range and returns its points and the width of one
// point in milliseconds. minute reads the rollup and thins it; otherwise the
// raw seconds answer one for one.
func (s *Server) timelinePoints(ctx context.Context, from, to int64, minute bool) ([]pointJSON, int64, error) {
	if !minute {
		rows, err := s.cfg.Store.Timeline(ctx, from, to, false)
		if err != nil {
			return nil, 0, err
		}
		seconds := floorDiv(to, 1000) - floorDiv(from, 1000) + 1
		bucket := chooseSecondBucket(seconds)
		return thinSeconds(rows, bucket), bucket * 1000, nil
	}
	rows, err := s.cfg.Store.MinutePoints(ctx, from, to)
	if err != nil {
		return nil, 0, err
	}
	// The bucket follows the range asked for, not the rows that came back, so
	// a gap in the data does not change how wide a point is.
	minutes := floorDiv(to, 60_000) - floorDiv(from, 60_000) + 1
	bucket := chooseBucket(minutes)
	return thin(rows, bucket), bucket * 60_000, nil
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "from", "to", "res")
	from, to := p.requireMS("from"), p.requireMS("to")
	res := p.one("res", "auto", "auto", "1s", "1m")
	if !p.ok(w) {
		return
	}
	if to < from {
		fail(w, http.StatusBadRequest, "to is before from. Give the range the other way round.")
		return
	}

	span := rangeSpan(from, to)
	minute := res == "1m" || (res == "auto" && span > secondRange)
	if !minute && span > secondRange {
		fail(w, http.StatusBadRequest,
			"res=1s covers at most 24 hours. Ask for a shorter range, or for res=1m.")
		return
	}
	if !s.rangeFits(w, span) {
		return
	}

	ctx := r.Context()
	points, bucketMS, err := s.timelinePoints(ctx, from, to, minute)
	if err != nil {
		s.serverError(w, "reading the timeline", err)
		return
	}

	events, _, err := s.cfg.Store.ListEvents(ctx, store.EventFilter{
		FromMS: from, ToMS: to, Limit: maxTimelineEvents,
	})
	if err != nil {
		s.serverError(w, "reading the events of the timeline", err)
		return
	}

	out := timelineJSON{
		Resolution: "1s", BucketMS: bucketMS, From: from, To: to,
		Points: points,
		Events: make([]markerJSON, len(events)),
		Quiet:  []spanJSON{},
	}
	if minute {
		out.Resolution = "1m"
	}
	for i, e := range events {
		m := markerJSON{ID: e.ID, StartedMS: e.StartedMS, EndedMS: e.EndedMS,
			LAmax: e.LAmax, Class: e.Class, Muted: e.Muted}
		if e.Review != nil {
			m.Status = e.Review.Status
		}
		out.Events[i] = m
	}

	// The quiet spans are wall-clock nights, so they are worked out in the
	// server's own time zone.
	quiet := s.cfg.Settings.Current().Quiet
	for _, sp := range quiet.Spans(time.UnixMilli(from).In(s.loc), time.UnixMilli(to).In(s.loc)) {
		out.Quiet = append(out.Quiet, spanJSON{From: sp.From.UnixMilli(), To: sp.To.UnixMilli()})
	}
	writeJSON(w, http.StatusOK, out)
}
