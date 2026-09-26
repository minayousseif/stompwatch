package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
)

// ErrNotFound means the row the caller asked for is not in the database.
var ErrNotFound = errors.New("store: not found")

// Every query here reads through the read-only pool. A read must never take a
// lock that the collector's writer is waiting for.

// Point is one place on the timeline. Baseline is nil for minute rows,
// which do not store one.
type Point struct {
	TMS      int64
	LAeq     float64
	LAmax    float64
	Baseline *float64
}

// Timeline returns points between fromMS and toMS. minute chooses the
// samples_1m rollup instead of samples_1s.
//
// Both bounds are inclusive, and each one keeps the row it falls inside: a
// fromMS half way through a second still returns that second, because that
// second holds sound from the range the caller asked about.
func (s *Store) Timeline(ctx context.Context, fromMS, toMS int64, minute bool) ([]Point, error) {
	if minute {
		return s.minutePoints(ctx, floorDiv(fromMS, 60_000)*60, floorDiv(toMS, 60_000)*60)
	}
	return s.secondPoints(ctx, floorDiv(fromMS, 1000), floorDiv(toMS, 1000))
}

// Samples returns samples_1s rows between two epoch-millisecond bounds.
func (s *Store) Samples(ctx context.Context, fromMS, toMS int64) ([]Point, error) {
	return s.secondPoints(ctx, floorDiv(fromMS, 1000), floorDiv(toMS, 1000))
}

func (s *Store) secondPoints(ctx context.Context, fromSec, toSec int64) ([]Point, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT ts, laeq, lamax, baseline FROM samples_1s
		WHERE ts >= ? AND ts <= ? ORDER BY ts`, fromSec, toSec)
	if err != nil {
		return nil, fmt.Errorf("store: reading seconds: %w", err)
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var ts int64
		var p Point
		var base float64
		if err := rows.Scan(&ts, &p.LAeq, &p.LAmax, &base); err != nil {
			return nil, fmt.Errorf("store: reading seconds: %w", err)
		}
		p.TMS, p.Baseline = ts*1000, &base
		out = append(out, p)
	}
	return out, rowsErr(rows, "reading seconds")
}

func (s *Store) minutePoints(ctx context.Context, fromSec, toSec int64) ([]Point, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT ts, laeq_mean, lamax_max FROM samples_1m
		WHERE ts >= ? AND ts <= ? ORDER BY ts`, fromSec, toSec)
	if err != nil {
		return nil, fmt.Errorf("store: reading minutes: %w", err)
	}
	defer rows.Close()
	var out []Point
	for rows.Next() {
		var ts int64
		var p Point
		if err := rows.Scan(&ts, &p.LAeq, &p.LAmax); err != nil {
			return nil, fmt.Errorf("store: reading minutes: %w", err)
		}
		p.TMS = ts * 1000
		out = append(out, p)
	}
	return out, rowsErr(rows, "reading minutes")
}

// MinutePoint is one samples_1m row together with N, the number of seconds
// folded into it. A caller that groups minutes into wider buckets needs N to
// weight each minute, because a minute cut short by a restart carries less
// measurement than a full one.
type MinutePoint struct {
	TMS   int64
	LAeq  float64 // laeq_mean, already an energy mean of its N seconds
	LAmax float64 // lamax_max
	N     int64
}

// MinutePoints returns the minute rollup between two epoch-millisecond
// bounds, with the second count of each row. Both bounds keep the minute
// they fall inside, the same rule Timeline uses.
func (s *Store) MinutePoints(ctx context.Context, fromMS, toMS int64) ([]MinutePoint, error) {
	fromSec, toSec := floorDiv(fromMS, 60_000)*60, floorDiv(toMS, 60_000)*60
	rows, err := s.r.QueryContext(ctx, `SELECT ts, laeq_mean, lamax_max, n FROM samples_1m
		WHERE ts >= ? AND ts <= ? ORDER BY ts`, fromSec, toSec)
	if err != nil {
		return nil, fmt.Errorf("store: reading minutes: %w", err)
	}
	defer rows.Close()
	var out []MinutePoint
	for rows.Next() {
		var ts int64
		var p MinutePoint
		if err := rows.Scan(&ts, &p.LAeq, &p.LAmax, &p.N); err != nil {
			return nil, fmt.Errorf("store: reading minutes: %w", err)
		}
		p.TMS = ts * 1000
		out = append(out, p)
	}
	return out, rowsErr(rows, "reading minutes")
}

// EventRow is an event with everything the dashboard shows about it.
type EventRow struct {
	ID                int64
	StartedMS         int64
	EndedMS           int64
	DurationMS        int64
	LAeq              float64
	LAmax             float64
	BaselineAtTrigger float64
	LowEnergy         float64
	HighEnergy        float64
	LowHighRatio      float64
	Class             string
	Confidence        float64
	Forced            bool
	// JumpDB is LAmax over the median level of the 30 s before the event, and
	// RiseDB is the largest step between two consecutive one-second means.
	// Together they separate a hit through the ceiling from a fan starting.
	// Both are invalid for an event recorded before schema 7 and for one with
	// less than 10 s of levels before it: nothing measured them, which is not
	// the same as measuring no rise.
	JumpDB    sql.NullFloat64
	RiseDB    sql.NullFloat64
	CreatedMS int64
	HasAudio  bool
	HasVideo  bool
	// Muted is true when the event overlaps a mute window. The event was
	// still measured and stored; the window only records that the owner can
	// explain it.
	Muted  bool
	Review *Review // nil when unreviewed
}

// EventFilter narrows a list of events. A zero field means no filter.
type EventFilter struct {
	FromMS, ToMS  int64 // 0 means unbounded; both bounds are inclusive
	Classes       []string
	Statuses      []string // "none" selects events with no review row
	MinLAmax      *float64
	MaxLAmax      *float64
	MinDurationMS int64
	Quiet         *config.QuietHours // non-nil keeps only events starting in quiet hours
	Loc           *time.Location     // nil means time.Local; used with Quiet
	// Muted keeps only muted events when true and only unmuted ones when
	// false. nil keeps every event.
	Muted         *bool
	Note          string // case-insensitive substring of the review note
	Sort          string // started_ms (default), lamax, duration_ms, class
	Desc          bool
	Limit, Offset int
}

// sortColumns is the whole set of orders a caller may ask for. A sort key
// names a column here; the caller's own text never reaches the SQL.
var sortColumns = map[string]string{
	"":            "e.started_ms",
	"started_ms":  "e.started_ms",
	"lamax":       "e.lamax",
	"duration_ms": "e.duration_ms",
	"class":       "e.class",
}

// mutedExpr is true when the event's span touches a mute window. Both edges
// count: an event that ends exactly when a window opens is inside it.
//
// EXISTS asks the question once per event, so an event that overlaps two
// windows is still one row. A join would return it twice and make the count,
// the limit and the offset disagree with the rows.
//
// The windows are read as part of each query, never cached, because the
// owner adds and removes them while the server runs.
const mutedExpr = `EXISTS(SELECT 1 FROM mute_windows w
		WHERE e.started_ms <= w.end_ms AND e.ended_ms >= w.start_ms)`

// eventColumns is shared by every query that returns an EventRow, so the
// column order and scanEvent cannot drift apart.
const eventColumns = `e.id, e.started_ms, e.ended_ms, e.duration_ms, e.laeq, e.lamax,
	e.baseline_at_trigger, e.low_energy, e.high_energy, e.low_high_ratio, e.class, e.confidence,
	e.forced, e.created_ms, e.jump_db, e.rise_db,
	EXISTS(SELECT 1 FROM event_media m WHERE m.event_id = e.id AND m.kind = 'audio'),
	EXISTS(SELECT 1 FROM event_media m WHERE m.event_id = e.id AND m.kind = 'video'),
	` + mutedExpr + `,
	r.status, r.note, r.reviewer, r.reviewed_ms`

const eventFrom = ` FROM events e LEFT JOIN event_review r ON r.event_id = e.id`

type scanner interface{ Scan(...any) error }

func scanEvent(sc scanner) (EventRow, error) {
	var e EventRow
	var forced int
	var status, note, reviewer sql.NullString
	var reviewedMS sql.NullInt64
	err := sc.Scan(&e.ID, &e.StartedMS, &e.EndedMS, &e.DurationMS, &e.LAeq, &e.LAmax,
		&e.BaselineAtTrigger, &e.LowEnergy, &e.HighEnergy, &e.LowHighRatio, &e.Class, &e.Confidence,
		&forced, &e.CreatedMS, &e.JumpDB, &e.RiseDB, &e.HasAudio, &e.HasVideo, &e.Muted,
		&status, &note, &reviewer, &reviewedMS)
	if err != nil {
		return EventRow{}, err
	}
	e.Forced = forced != 0
	if status.Valid {
		e.Review = &Review{EventID: e.ID, Status: status.String, Note: note.String,
			Reviewer: reviewer.String, At: time.UnixMilli(reviewedMS.Int64)}
	}
	return e, nil
}

// ListEvents returns matching events and the total number of matches before
// Limit and Offset. Set Desc for newest first, which is what the dashboard
// asks for. The rows and the count use the same WHERE clause.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]EventRow, int, error) {
	col, ok := sortColumns[f.Sort]
	if !ok {
		return nil, 0, fmt.Errorf("store: sort %q must be started_ms, lamax, duration_ms, or class", f.Sort)
	}
	where, args, err := s.eventWhere(ctx, f)
	if err != nil {
		return nil, 0, err
	}

	total, err := s.countEvents(ctx, where, args)
	if err != nil {
		return nil, 0, err
	}

	dir := " ASC"
	if f.Desc {
		dir = " DESC"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = -1 // SQLite reads a negative limit as no limit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + eventColumns + eventFrom + where +
		` ORDER BY ` + col + dir + `, e.id` + dir + ` LIMIT ? OFFSET ?`
	rows, err := s.r.QueryContext(ctx, q, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: listing events: %w", err)
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: listing events: %w", err)
		}
		out = append(out, e)
	}
	return out, total, rowsErr(rows, "listing events")
}

func (s *Store) countEvents(ctx context.Context, where string, args []any) (int, error) {
	var total int
	err := s.r.QueryRowContext(ctx, `SELECT count(*)`+eventFrom+where, args...).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("store: counting events: %w", err)
	}
	return total, nil
}

// eventWhere builds the WHERE clause for both the rows and the count. Every
// value is a bound parameter.
func (s *Store) eventWhere(ctx context.Context, f EventFilter) (string, []any, error) {
	var conds []string
	var args []any
	add := func(cond string, vals ...any) {
		conds = append(conds, cond)
		args = append(args, vals...)
	}

	if f.FromMS != 0 {
		add("e.started_ms >= ?", f.FromMS)
	}
	if f.ToMS != 0 {
		add("e.started_ms <= ?", f.ToMS)
	}
	if len(f.Classes) > 0 {
		add("e.class IN ("+placeholders(len(f.Classes))+")", toAny(f.Classes)...)
	}
	if cond, vals := statusCondition(f.Statuses); cond != "" {
		add(cond, vals...)
	}
	if f.MinLAmax != nil {
		add("e.lamax >= ?", *f.MinLAmax)
	}
	if f.MaxLAmax != nil {
		add("e.lamax <= ?", *f.MaxLAmax)
	}
	if f.MinDurationMS > 0 {
		add("e.duration_ms >= ?", f.MinDurationMS)
	}
	if f.Note != "" {
		// SQLite folds only ASCII letters, so this matches the case of an
		// English note. The search text is a parameter, not SQL.
		add(`LOWER(r.note) LIKE ? ESCAPE '\'`, "%"+likeEscape(strings.ToLower(f.Note))+"%")
	}
	if f.Muted != nil {
		// The database decides, so the rows and the count agree. Filtering
		// in Go afterwards would leave total, limit and offset describing a
		// different list from the one that came back.
		if *f.Muted {
			add(mutedExpr)
		} else {
			add("NOT " + mutedExpr)
		}
	}
	if f.Quiet != nil {
		cond, vals, err := s.quietCondition(ctx, f)
		if err != nil {
			return "", nil, err
		}
		add(cond, vals...)
	}

	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// statusCondition turns the wanted review statuses into one condition. The
// status "none" means the event has no review row at all.
func statusCondition(statuses []string) (string, []any) {
	var named []string
	var none bool
	for _, st := range statuses {
		if st == "none" {
			none = true
			continue
		}
		named = append(named, st)
	}
	var parts []string
	var args []any
	if len(named) > 0 {
		parts = append(parts, "r.status IN ("+placeholders(len(named))+")")
		args = append(args, toAny(named)...)
	}
	if none {
		parts = append(parts, "r.status IS NULL")
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// quietCondition keeps only events that start inside quiet hours. The spans
// are worked out in Go and become an OR group of ranges, so the database does
// the filtering and the total, the limit and the offset all agree.
func (s *Store) quietCondition(ctx context.Context, f EventFilter) (string, []any, error) {
	loc := f.Loc
	if loc == nil {
		loc = time.Local
	}
	// The range is narrowed to the oldest and newest event there is, so the
	// span list stays as short as the data allows. An unbounded side, or one
	// set far past the data, would otherwise ask for a span per night of a
	// range the events do not reach.
	var lo, hi sql.NullInt64
	err := s.r.QueryRowContext(ctx, `SELECT MIN(started_ms), MAX(started_ms) FROM events`).Scan(&lo, &hi)
	if err != nil {
		return "", nil, fmt.Errorf("store: reading the event time range: %w", err)
	}
	if !lo.Valid {
		return "0", nil, nil // no events at all, so nothing is in quiet hours
	}
	fromMS, toMS := f.FromMS, f.ToMS
	if fromMS == 0 || fromMS < lo.Int64 {
		fromMS = lo.Int64
	}
	if toMS == 0 || toMS > hi.Int64 {
		toMS = hi.Int64
	}
	if toMS < fromMS {
		return "0", nil, nil // the range holds no event at all
	}
	// The filter's end is inclusive but a span is half open, so ask for one
	// millisecond more.
	spans := f.Quiet.Spans(time.UnixMilli(fromMS).In(loc), time.UnixMilli(toMS+1).In(loc))
	if len(spans) == 0 {
		return "0", nil, nil
	}
	parts := make([]string, len(spans))
	args := make([]any, 0, 2*len(spans))
	for i, sp := range spans {
		// Half open, to agree with QuietHours.Contains: an event that starts
		// exactly at quiet_end is already out of quiet hours.
		parts[i] = "(e.started_ms >= ? AND e.started_ms < ?)"
		args = append(args, sp.From.UnixMilli(), sp.To.UnixMilli())
	}
	return orTree(parts), args, nil
}

// orTree joins conditions with OR as a balanced tree. SQLite refuses an
// expression more than 1000 deep, and a flat chain of one range per night
// is as deep as it is long, so quiet_only over three years failed. A
// balanced tree of ten years of nights is 12 deep. The order of the
// conditions, and so of their arguments, is kept.
func orTree(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	mid := len(parts) / 2
	return "(" + orTree(parts[:mid]) + " OR " + orTree(parts[mid:]) + ")"
}

// Event returns one event with its review.
func (s *Store) Event(ctx context.Context, id int64) (EventRow, error) {
	row := s.r.QueryRowContext(ctx, `SELECT `+eventColumns+eventFrom+` WHERE e.id = ?`, id)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EventRow{}, fmt.Errorf("store: event %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return EventRow{}, fmt.Errorf("store: reading event %d: %w", id, err)
	}
	return e, nil
}

// EventEnvelope returns the stored envelope of an event, in linear units.
func (s *Store) EventEnvelope(ctx context.Context, id int64) ([]float64, error) {
	var blob []byte
	err := s.r.QueryRowContext(ctx, `SELECT envelope FROM events WHERE id = ?`, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: event %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: reading the envelope of event %d: %w", id, err)
	}
	return DecodeEnvelope(blob)
}

// mediaColumns reads a clip row together with its purge, if it has one, so
// a caller can tell a deliberately deleted clip from a vanished one without
// a second query. The purge columns are NULL for a clip that is still there.
const mediaColumns = `m.event_id, m.kind, m.path, m.bytes, m.duration_ms, m.sha256,
	m.started_ms, m.clipped, m.truncated, m.camera_audio, p.bytes, p.purged_ms, p.purged_by`

const mediaFrom = ` FROM event_media m
	LEFT JOIN media_purge p ON p.event_id = m.event_id AND p.kind = m.kind`

func scanMedia(sc scanner) (Media, error) {
	var m Media
	var durMS, startedMS int64
	var truncated, cameraAudio int
	var purgedBytes, purgedMS sql.NullInt64
	var purgedBy sql.NullString
	err := sc.Scan(&m.EventID, &m.Kind, &m.Path, &m.Bytes, &durMS, &m.SHA256, &startedMS,
		&m.Clipped, &truncated, &cameraAudio, &purgedBytes, &purgedMS, &purgedBy)
	if err != nil {
		return Media{}, err
	}
	m.Duration = time.Duration(durMS) * time.Millisecond
	// A 0 means the clip predates the column. Leave the zero time, so a
	// caller cannot mistake it for a clip recorded at the epoch.
	if startedMS != 0 {
		m.Started = time.UnixMilli(startedMS).UTC()
	}
	m.Truncated = truncated != 0
	m.CameraAudio = cameraAudio != 0
	if purgedMS.Valid {
		m.Purged = &Purge{
			EventID: m.EventID, Kind: m.Kind, Bytes: purgedBytes.Int64,
			At: time.UnixMilli(purgedMS.Int64).UTC(), By: purgedBy.String,
		}
	}
	return m, nil
}

// EventMedia returns the clip files of an event. An event with no clip gives
// an empty list, not an error.
func (s *Store) EventMedia(ctx context.Context, id int64) ([]Media, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+mediaColumns+mediaFrom+`
		WHERE m.event_id = ? ORDER BY m.kind`, id)
	if err != nil {
		return nil, fmt.Errorf("store: reading the media of event %d: %w", id, err)
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading the media of event %d: %w", id, err)
		}
		out = append(out, m)
	}
	return out, rowsErr(rows, "reading media")
}

// MediaFile returns one media row, for the clip handlers.
func (s *Store) MediaFile(ctx context.Context, id int64, kind string) (Media, error) {
	row := s.r.QueryRowContext(ctx, `SELECT `+mediaColumns+mediaFrom+`
		WHERE m.event_id = ? AND m.kind = ?`, id, kind)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, fmt.Errorf("store: %s for event %d: %w", kind, id, ErrNotFound)
	}
	if err != nil {
		return Media{}, fmt.Errorf("store: reading the %s of event %d: %w", kind, id, err)
	}
	return m, nil
}

// DeleteReview removes the review of an event. event_review is the one
// table that is not immutable. An event that has no review is already in the
// state the caller wants, so that is not an error.
func (s *Store) DeleteReview(ctx context.Context, id int64) error {
	err := s.retryBusy(ctx, func() error {
		_, err := s.w.ExecContext(ctx, `DELETE FROM event_review WHERE event_id = ?`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: deleting the review of event %d: %w", id, err)
	}
	return nil
}

// HealthRow is one entry in the health log.
type HealthRow struct {
	ID         int64
	TSMS       int64
	Kind       string
	Detail     string
	DurationMS int64
}

// HealthFilter narrows the health log. A zero field means no filter.
type HealthFilter struct {
	FromMS, ToMS  int64
	Kinds         []string
	Limit, Offset int
}

// ListHealth returns health records newest first and the total number of
// matches before Limit and Offset.
func (s *Store) ListHealth(ctx context.Context, f HealthFilter) ([]HealthRow, int, error) {
	var conds []string
	var args []any
	if f.FromMS != 0 {
		conds = append(conds, "ts_ms >= ?")
		args = append(args, f.FromMS)
	}
	if f.ToMS != 0 {
		conds = append(conds, "ts_ms <= ?")
		args = append(args, f.ToMS)
	}
	if len(f.Kinds) > 0 {
		conds = append(conds, "kind IN ("+placeholders(len(f.Kinds))+")")
		args = append(args, toAny(f.Kinds)...)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM system_health`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: counting health records: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = -1
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := s.r.QueryContext(ctx, `SELECT id, ts_ms, kind, detail, COALESCE(duration_ms, 0)
		FROM system_health`+where+` ORDER BY ts_ms DESC, id DESC LIMIT ? OFFSET ?`,
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: listing health records: %w", err)
	}
	defer rows.Close()
	var out []HealthRow
	for rows.Next() {
		var h HealthRow
		if err := rows.Scan(&h.ID, &h.TSMS, &h.Kind, &h.Detail, &h.DurationMS); err != nil {
			return nil, 0, fmt.Errorf("store: listing health records: %w", err)
		}
		out = append(out, h)
	}
	return out, total, rowsErr(rows, "listing health records")
}

// Summary is the headline for a time range. Every count but Muted is over
// the events the owner has not already explained.
type Summary struct {
	Events, Reviewed, Verified, QuietHourEvents int
	Muted                                       int     // events in the range that a mute window covers
	LAeq, Baseline                              float64 // energy mean and median of samples_1s
	HaveLevels                                  bool    // false when the range has no samples
	Loudest                                     *EventRow
}

// Summary counts the events in a range and measures its level. LAeq is the
// energy mean of the seconds, not the mean of their dB numbers, because dB is
// a logarithm and averaging it would understate every loud moment.
func (s *Store) Summary(ctx context.Context, fromMS, toMS int64, q config.QuietHours, loc *time.Location) (Summary, error) {
	var sum Summary
	// A muted event was still measured and stored. It is left out of the
	// headline and counted on its own, so the number is visible rather than
	// silently dropped.
	err := s.r.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(NOT muted), 0), COALESCE(SUM(reviewed AND NOT muted), 0),
			COALESCE(SUM(verified AND NOT muted), 0), COALESCE(SUM(muted), 0)
		FROM (SELECT `+mutedExpr+` AS muted, r.event_id IS NOT NULL AS reviewed,
				r.status = 'verified' AS verified`+
		eventFrom+` WHERE e.started_ms >= ? AND e.started_ms <= ?)`, fromMS, toMS).
		Scan(&sum.Events, &sum.Reviewed, &sum.Verified, &sum.Muted)
	if err != nil {
		return Summary{}, fmt.Errorf("store: summarizing events: %w", err)
	}

	unmuted := false
	f := EventFilter{FromMS: fromMS, ToMS: toMS, Quiet: &q, Loc: loc, Muted: &unmuted}
	where, args, err := s.eventWhere(ctx, f)
	if err != nil {
		return Summary{}, err
	}
	if sum.QuietHourEvents, err = s.countEvents(ctx, where, args); err != nil {
		return Summary{}, err
	}

	if sum.Events > 0 {
		row := s.r.QueryRowContext(ctx, `SELECT `+eventColumns+eventFrom+
			` WHERE e.started_ms >= ? AND e.started_ms <= ? AND NOT `+mutedExpr+
			` ORDER BY e.lamax DESC, e.id LIMIT 1`, fromMS, toMS)
		loudest, err := scanEvent(row)
		if err != nil {
			return Summary{}, fmt.Errorf("store: reading the loudest event: %w", err)
		}
		sum.Loudest = &loudest
	}

	fromSec, toSec := floorDiv(fromMS, 1000), floorDiv(toMS, 1000)
	var n int
	var laeq sql.NullFloat64
	err = s.r.QueryRowContext(ctx, `SELECT count(*), 10 * log10(avg(power(10, laeq / 10.0)))
		FROM samples_1s WHERE ts >= ? AND ts <= ?`, fromSec, toSec).Scan(&n, &laeq)
	if err != nil {
		return Summary{}, fmt.Errorf("store: measuring the range: %w", err)
	}
	if n == 0 {
		// No samples means no level. A zero here would draw as silence.
		return sum, nil
	}
	sum.HaveLevels, sum.LAeq = true, laeq.Float64

	// The median is the middle baseline, or the mean of the two middle ones
	// when the count is even.
	limit, offset := 1, n/2
	if n%2 == 0 {
		limit, offset = 2, n/2-1
	}
	err = s.r.QueryRowContext(ctx, `SELECT avg(baseline) FROM
		(SELECT baseline FROM samples_1s WHERE ts >= ? AND ts <= ? ORDER BY baseline LIMIT ? OFFSET ?)`,
		fromSec, toSec, limit, offset).Scan(&sum.Baseline)
	if err != nil {
		return Summary{}, fmt.Errorf("store: reading the median baseline: %w", err)
	}
	return sum, nil
}

// Counts is what the system page shows about how much has been recorded.
type Counts struct{ EventsTotal, Unreviewed, EventsSince, SamplesSince int }

// CountsSince counts events and seconds recorded at or after sinceMS, plus
// the all-time totals. A sinceMS part way through a second counts that
// second, the same rule the timeline uses.
func (s *Store) CountsSince(ctx context.Context, sinceMS int64) (Counts, error) {
	var c Counts
	err := s.r.QueryRowContext(ctx, `SELECT count(*),
			COALESCE(SUM(r.event_id IS NULL), 0), COALESCE(SUM(e.started_ms >= ?), 0)`+eventFrom,
		sinceMS).Scan(&c.EventsTotal, &c.Unreviewed, &c.EventsSince)
	if err != nil {
		return Counts{}, fmt.Errorf("store: counting events: %w", err)
	}
	err = s.r.QueryRowContext(ctx, `SELECT count(*) FROM samples_1s WHERE ts >= ?`,
		floorDiv(sinceMS, 1000)).Scan(&c.SamplesSince)
	if err != nil {
		return Counts{}, fmt.Errorf("store: counting samples: %w", err)
	}
	return c, nil
}

// Totals is how many rows of each kind the whole database holds. Seconds is
// the number of measured seconds, one row of samples_1s each.
type Totals struct {
	Events     int
	Seconds    int
	AudioClips int
	VideoClips int
	Reviews    int
	HealthRows int
}

// Totals counts every kind of row in the database. It is what stompwatch reset
// prints before it removes anything, so the owner can read how much is about
// to go and stop if it is more than test data.
func (s *Store) Totals(ctx context.Context) (Totals, error) {
	return countRows(ctx, s.r)
}

// TotalsOf counts what the database at path holds without opening it for
// writing. A read-write open applies any pending migration and folds the
// write-ahead log into the database as it closes, and the preview of a reset
// must change nothing at all.
//
// It cannot always be done: a database left by a crash needs its write-ahead
// log recovered, and only a writer may do that. The caller decides what to do
// when that happens.
func TotalsOf(ctx context.Context, path string) (Totals, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return Totals{}, fmt.Errorf("store: invalid database path %q", path)
	}
	db, err := sql.Open(driverName, "file:"+path+"?"+readerParams)
	if err != nil {
		return Totals{}, fmt.Errorf("store: %w", err)
	}
	defer db.Close()
	return countRows(ctx, db)
}

// countRows is the counting itself, over a read handle of either kind.
func countRows(ctx context.Context, r *sql.DB) (Totals, error) {
	var t Totals
	err := r.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM events),
			(SELECT count(*) FROM samples_1s),
			(SELECT COALESCE(SUM(kind = 'audio'), 0) FROM event_media),
			(SELECT COALESCE(SUM(kind = 'video'), 0) FROM event_media),
			(SELECT count(*) FROM event_review),
			(SELECT count(*) FROM system_health)`).
		Scan(&t.Events, &t.Seconds, &t.AudioClips, &t.VideoClips, &t.Reviews, &t.HealthRows)
	if err != nil {
		return Totals{}, fmt.Errorf("store: counting what the database holds: %w", err)
	}
	return t, nil
}

// Settings reads the config table, which overrides the config file.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	return readSettings(ctx, s.r)
}

// readSettings is the reading itself, over a read handle of either kind.
func readSettings(ctx context.Context, r *sql.DB) (map[string]string, error) {
	rows, err := r.QueryContext(ctx, `SELECT key, value FROM config`)
	if err != nil {
		return nil, fmt.Errorf("store: reading settings: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: reading settings: %w", err)
		}
		out[k] = v
	}
	return out, rowsErr(rows, "reading settings")
}

// SettingsOf reads the config table of the database at path without
// opening it for writing, for the reason TotalsOf gives: stompwatch reset
// prints the saved settings in its preview, and a preview must change
// nothing. It fails, as TotalsOf does, on a database whose write-ahead log
// needs recovering.
func SettingsOf(ctx context.Context, path string) (map[string]string, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: invalid database path %q", path)
	}
	db, err := sql.Open(driverName, "file:"+path+"?"+readerParams)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer db.Close()
	return readSettings(ctx, db)
}

// PutSettings writes every entry in one transaction. A nil value deletes
// the row so the config file default applies again. Every key is checked
// before anything is written, so a bad request changes nothing.
func (s *Store) PutSettings(ctx context.Context, values map[string]*string, at time.Time) error {
	for k := range values {
		if k == "" {
			return errors.New("store: a setting key must not be empty")
		}
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for k, v := range values {
			var err error
			if v == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, k)
			} else {
				_, err = tx.ExecContext(ctx, `INSERT INTO config (key, value, updated_ms) VALUES (?, ?, ?)
					ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_ms = excluded.updated_ms`,
					k, *v, at.UnixMilli())
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: writing %d settings: %w", len(values), err)
	}
	return nil
}

// MuteWindow is a span the owner asked not to be alerted about.
type MuteWindow struct {
	ID, StartMS, EndMS int64
	Reason             string
}

// MuteWindows returns every mute window, oldest first.
func (s *Store) MuteWindows(ctx context.Context) ([]MuteWindow, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, start_ms, end_ms, reason FROM mute_windows
		ORDER BY start_ms, id`)
	if err != nil {
		return nil, fmt.Errorf("store: reading mute windows: %w", err)
	}
	defer rows.Close()
	var out []MuteWindow
	for rows.Next() {
		var w MuteWindow
		if err := rows.Scan(&w.ID, &w.StartMS, &w.EndMS, &w.Reason); err != nil {
			return nil, fmt.Errorf("store: reading mute windows: %w", err)
		}
		out = append(out, w)
	}
	return out, rowsErr(rows, "reading mute windows")
}

// AddMuteWindow stores a mute window and returns its id.
func (s *Store) AddMuteWindow(ctx context.Context, w MuteWindow) (int64, error) {
	var id int64
	err := s.retryBusy(ctx, func() error {
		res, err := s.w.ExecContext(ctx, `INSERT INTO mute_windows (start_ms, end_ms, reason) VALUES (?, ?, ?)`,
			w.StartMS, w.EndMS, w.Reason)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: adding a mute window: %w", err)
	}
	return id, nil
}

// DeleteMuteWindow removes one mute window.
func (s *Store) DeleteMuteWindow(ctx context.Context, id int64) error {
	var affected int64
	err := s.retryBusy(ctx, func() error {
		res, err := s.w.ExecContext(ctx, `DELETE FROM mute_windows WHERE id = ?`, id)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("store: deleting mute window %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("store: mute window %d: %w", id, ErrNotFound)
	}
	return nil
}

// DBSize returns the size of the database file and its WAL in bytes. A
// missing WAL file is 0: SQLite removes it on a clean close.
func (s *Store) DBSize() (main, wal int64, err error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		return 0, 0, fmt.Errorf("store: %w", err)
	}
	wal, err = s.WALSize()
	if err != nil {
		return 0, 0, err
	}
	return fi.Size(), wal, nil
}

// floorDiv divides and rounds towards minus infinity, so the second or minute
// that a millisecond bound falls inside is the one it names.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// likeEscape makes the wildcards of LIKE ordinary characters, so a search for
// "50%" finds a note holding a per cent sign.
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func rowsErr(rows *sql.Rows, what string) error {
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	return nil
}
