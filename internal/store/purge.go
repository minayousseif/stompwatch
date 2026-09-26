package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Purge is the record of one media file that was deliberately deleted.
//
// It is not a deletion of anything in the database. The event row, the
// seconds around it, the review, and the event_media row with its size,
// duration and SHA-256 all stay exactly as they were: event_media is the
// record that the clip existed and what it was (SPEC.md section 3 rule 3
// and section 15 decision 21).
type Purge struct {
	EventID int64
	Kind    string
	// Bytes is what the file took on disk when it was deleted. It is 0 for
	// a file that was already gone. The purge is recorded anyway, because
	// what is recorded is the owner's decision, not the free space.
	Bytes int64
	At    time.Time
	// By is the login of whoever asked, from the request identity. It is
	// never read from a request body.
	By string
}

// RecordPurge stores one row per deleted file, in one transaction. One bad
// entry writes none of them: a half-written purge would say some files were
// deleted that still exist, or the other way round.
//
// A clip that is already recorded as purged is left as it is, so the first
// record of a deletion is the one that stands. Asking twice is not an error;
// the owner may select the same event again.
func (s *Store) RecordPurge(ctx context.Context, purges []Purge) error {
	if len(purges) == 0 {
		return nil
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO media_purge
			(event_id, kind, bytes, purged_ms, purged_by) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (event_id, kind) DO NOTHING`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, p := range purges {
			if _, err := stmt.ExecContext(ctx, p.EventID, p.Kind, p.Bytes, p.At.UnixMilli(), p.By); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: recording %d purges: %w", len(purges), err)
	}
	return nil
}

// PurgedMedia returns the purges of the events named, keyed by event. An
// event with nothing purged is left out of the map rather than given an
// empty list, so a caller can ask "is there one" with a lookup.
//
// An empty list of events is the question "which of none", and the answer
// is none. It is never every purge in the database.
func (s *Store) PurgedMedia(ctx context.Context, eventIDs []int64) (map[int64][]Purge, error) {
	out := make(map[int64][]Purge)
	if len(eventIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	q := `SELECT event_id, kind, bytes, purged_ms, purged_by FROM media_purge
		WHERE event_id IN (` + placeholders(len(eventIDs)) + `) ORDER BY event_id, kind`
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading purges: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p Purge
		var at int64
		if err := rows.Scan(&p.EventID, &p.Kind, &p.Bytes, &at, &p.By); err != nil {
			return nil, fmt.Errorf("store: reading purges: %w", err)
		}
		p.At = time.UnixMilli(at).UTC()
		out[p.EventID] = append(out[p.EventID], p)
	}
	return out, rowsErr(rows, "reading purges")
}

// MediaUsage is how much space a set of clip files takes. Events is how many
// distinct events the files belong to, because the owner deletes by event
// and the interface counts them that way.
type MediaUsage struct {
	Files  int
	Events int
	Bytes  int64
}

// MediaSpace is what the clip files add up to. Audio and Video are the files
// still held; Purged is what has already been reclaimed; Purgeable is what a
// purge would reclaim now, narrowed by the caller's age filter.
//
// The figures come from event_media.bytes, which is what the collector wrote
// when it stored each clip, not from a walk of the disk. The database is
// what the interface can promise; a walk would also count the segment ring
// and anything else that happens to be under the directory.
type MediaSpace struct {
	Audio     MediaUsage
	Video     MediaUsage
	Purged    MediaUsage
	Purgeable MediaUsage
}

// notPurged is the join that keeps only the media rows still on disk.
const notPurged = ` LEFT JOIN media_purge p ON p.event_id = m.event_id AND p.kind = m.kind`

// MediaFilter narrows what MediaSpace counts as purgeable. A zero field is
// no filter. StartedBeforeMS keeps events that started before that epoch
// millisecond, which is what "recordings older than 30 days" asks.
// EventIDs keeps exactly those events, which is what a selection asks.
type MediaFilter struct {
	StartedBeforeMS int64
	EventIDs        []int64
}

// where writes the conditions of a filter, with a leading AND for each.
func (f MediaFilter) where() (string, []any) {
	var q string
	var args []any
	if f.StartedBeforeMS != 0 {
		q += ` AND e.started_ms < ?`
		args = append(args, f.StartedBeforeMS)
	}
	if len(f.EventIDs) > 0 {
		q += ` AND m.event_id IN (` + placeholders(len(f.EventIDs)) + `)`
		for _, id := range f.EventIDs {
			args = append(args, id)
		}
	}
	return q, args
}

// MediaSpace measures the clip files. The filter narrows Purgeable only: the
// held and purged figures are always the whole store, because they say how
// much is on disk, not how much one filter would take.
func (s *Store) MediaSpace(ctx context.Context, f MediaFilter) (MediaSpace, error) {
	var out MediaSpace

	rows, err := s.r.QueryContext(ctx, `SELECT m.kind, count(*), count(DISTINCT m.event_id),
		COALESCE(sum(m.bytes), 0) FROM event_media m`+notPurged+`
		WHERE p.event_id IS NULL GROUP BY m.kind`)
	if err != nil {
		return out, fmt.Errorf("store: measuring the clip files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var u MediaUsage
		if err := rows.Scan(&kind, &u.Files, &u.Events, &u.Bytes); err != nil {
			return out, fmt.Errorf("store: measuring the clip files: %w", err)
		}
		switch kind {
		case KindAudio:
			out.Audio = u
		case KindVideo:
			out.Video = u
		}
	}
	if err := rowsErr(rows, "measuring the clip files"); err != nil {
		return out, err
	}

	err = s.r.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT event_id),
		COALESCE(sum(bytes), 0) FROM media_purge`).
		Scan(&out.Purged.Files, &out.Purged.Events, &out.Purged.Bytes)
	if err != nil {
		return out, fmt.Errorf("store: measuring what has been purged: %w", err)
	}

	narrow, args := f.where()
	q := `SELECT count(*), count(DISTINCT m.event_id), COALESCE(sum(m.bytes), 0)
		FROM event_media m JOIN events e ON e.id = m.event_id` + notPurged + `
		WHERE p.event_id IS NULL` + narrow
	err = s.r.QueryRowContext(ctx, q, args...).
		Scan(&out.Purgeable.Files, &out.Purgeable.Events, &out.Purgeable.Bytes)
	if err != nil {
		return out, fmt.Errorf("store: measuring what can be purged: %w", err)
	}
	return out, nil
}

// PurgeableEvents returns the events that still hold a clip and started
// before an instant, oldest first. It is how the age control turns
// "recordings older than 30 days" into the list a purge acts on, and the
// oldest order means a limit takes the oldest recordings rather than an
// arbitrary handful.
//
// offset pages past events the caller has already dealt with. Retention
// needs it: an event whose file could not be deleted keeps no purge row,
// so it stays on this list forever and would otherwise hold the front of
// every page while nothing newer was ever reached. The order is by start
// and then by event, so a page boundary never falls in the middle of two
// events that started in the same millisecond.
func (s *Store) PurgeableEvents(ctx context.Context, f MediaFilter, limit, offset int) ([]int64, error) {
	narrow, args := f.where()
	q := `SELECT DISTINCT m.event_id, e.started_ms
		FROM event_media m JOIN events e ON e.id = m.event_id` +
		notPurged + ` WHERE p.event_id IS NULL` + narrow +
		` ORDER BY e.started_ms, m.event_id LIMIT ? OFFSET ?`
	args = append(args, limit, max(offset, 0))

	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: listing what can be purged: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id, startedMS int64
		if err := rows.Scan(&id, &startedMS); err != nil {
			return nil, fmt.Errorf("store: listing what can be purged: %w", err)
		}
		out = append(out, id)
	}
	return out, rowsErr(rows, "listing what can be purged")
}

// EventsExist returns the ids that are real events, out of the ids given. A
// number that is not an event must never reach a filesystem path.
func (s *Store) EventsExist(ctx context.Context, eventIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool)
	if len(eventIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT id FROM events WHERE id IN (`+placeholders(len(eventIDs))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: checking which events exist: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: checking which events exist: %w", err)
		}
		out[id] = true
	}
	return out, rowsErr(rows, "checking which events exist")
}
