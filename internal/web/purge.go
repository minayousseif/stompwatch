package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/minayousseif/stompwatch/internal/media"
	"github.com/minayousseif/stompwatch/internal/store"
)

// maxPurgeEvents is as many events as one purge may name. A request that
// takes minutes and cannot be described in one sentence is not a request
// the owner meant to make.
const maxPurgeEvents = 500

// purgeableKinds are the kinds of media a purge may remove.
var purgeableKinds = []string{store.KindAudio, store.KindVideo}

// What happened to one file.
const (
	// outcomePurged: the file was deleted and the purge recorded.
	outcomePurged = "purged"
	// outcomeAlreadyGone: there was a media row but no file. The purge is
	// recorded with 0 bytes, so the record still says the clip is
	// deliberately gone rather than lost.
	outcomeAlreadyGone = "already_gone"
	// outcomeAlreadyPurged: a purge row was already there. Nothing is
	// written twice, and the first record of the deletion stands.
	outcomeAlreadyPurged = "already_purged"
	// outcomeNoMedia: the event has no clip of that kind.
	outcomeNoMedia = "no_media"
	// outcomeRefused: the stored path leads outside the media root for its
	// kind, so the file is not deleted.
	outcomeRefused = "refused"
)

// escapedRootReason is what the caller is told about a path that leads out
// of its media root. It never names the path.
const escapedRootReason = "the stored path leads outside the directory this kind of media is " +
	"served from, so the file was not deleted."

type purgeResultJSON struct {
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
	Bytes   int64  `json:"bytes"`
	Reason  string `json:"reason,omitempty"`
}

type purgeEventJSON struct {
	EventID int64             `json:"event_id"`
	Bytes   int64             `json:"bytes"`
	Results []purgeResultJSON `json:"results"`
}

type purgeReplyJSON struct {
	PurgedFiles int              `json:"purged_files"`
	PurgedBytes int64            `json:"purged_bytes"`
	Events      []purgeEventJSON `json:"events"`
	// UnknownEvents are ids with no event row. Nothing is deleted,
	// resolved, or recorded for them.
	UnknownEvents []int64 `json:"unknown_events"`
}

// handlePurgeMedia deletes clip files and nothing else.
//
// This is the only endpoint in the program that removes anything, and what
// it removes is always a file. The retention job is the other thing that
// removes one, and it deletes through the same media package. The event row, its seconds, its review, and
// its event_media row with the size, duration and SHA-256 all stay: that
// row is the record that the clip existed and what it was (SPEC.md section 3
// rule 3 and section 15 decision 21). Every purge is recorded in
// media_purge and the request writes one system_health row.
func (s *Server) handlePurgeMedia(w http.ResponseWriter, r *http.Request) {
	if p := parseQuery(r); !p.ok(w) {
		return
	}
	var body struct {
		EventIDs []int64 `json:"event_ids"`
		// Kinds is a pointer so "absent" and "an empty list" are different
		// questions. Absent means both kinds; an empty list names nothing
		// and is a mistake.
		Kinds   *[]string `json:"kinds"`
		Confirm bool      `json:"confirm"`
	}
	if !readJSON(w, r, &body) {
		return
	}

	// The confirmation lives in the request, not only in the interface, so
	// a stray POST from a script or a mistyped fetch cannot delete a
	// recording. There is no undo.
	if !body.Confirm {
		fail(w, http.StatusBadRequest,
			"set confirm to true to delete recordings. Deleting them cannot be undone, "+
				"so the request has to say it means it.")
		return
	}
	ids, ok := purgeIDs(w, body.EventIDs)
	if !ok {
		return
	}
	kinds, ok := purgeKinds(w, body.Kinds)
	if !ok {
		return
	}

	ctx := r.Context()
	exists, err := s.cfg.Store.EventsExist(ctx, ids)
	if err != nil {
		s.serverError(w, "checking which events exist", err)
		return
	}

	login, _ := Identity(r)
	out := purgeReplyJSON{Events: []purgeEventJSON{}, UnknownEvents: []int64{}}
	var record []store.Purge
	now := s.now()

	for _, id := range ids {
		// An id that is not an event must never reach a filesystem path.
		if !exists[id] {
			out.UnknownEvents = append(out.UnknownEvents, id)
			continue
		}
		media, err := s.cfg.Store.EventMedia(ctx, id)
		if err != nil {
			s.serverError(w, "reading the clips of an event", err)
			return
		}
		byKind := make(map[string]store.Media, len(media))
		for _, m := range media {
			byKind[m.Kind] = m
		}

		one := purgeEventJSON{EventID: id, Results: []purgeResultJSON{}}
		for _, kind := range kinds {
			result := s.purgeOne(byKind, id, kind)
			one.Results = append(one.Results, result)
			switch result.Outcome {
			case outcomePurged, outcomeAlreadyGone:
				record = append(record, store.Purge{
					EventID: id, Kind: kind, Bytes: result.Bytes, At: now, By: login,
				})
				one.Bytes += result.Bytes
				out.PurgedFiles++
				out.PurgedBytes += result.Bytes
			}
		}
		out.Events = append(out.Events, one)
	}

	if len(record) > 0 {
		if err := s.cfg.Store.RecordPurge(ctx, record); err != nil {
			// The files are already gone. Failing silently here would leave
			// the database claiming clips that no longer exist, with no
			// record of who removed them, which is the one outcome rule 3
			// forbids.
			s.serverError(w, "recording a purge", err)
			return
		}
		detail := media.PurgeDetail(login, record, out.PurgedBytes)
		s.log.Info("clip files were purged", "by", login,
			"files", out.PurgedFiles, "bytes", out.PurgedBytes)
		s.cfg.RecordHealth(now, store.HealthMediaPurge, detail, 0)
	}

	writeJSON(w, http.StatusOK, out)
}

// purgeOne deletes the file of one clip and says what happened. It never
// touches a row.
func (s *Server) purgeOne(byKind map[string]store.Media, id int64, kind string) purgeResultJSON {
	m, ok := byKind[kind]
	if !ok {
		return purgeResultJSON{Kind: kind, Outcome: outcomeNoMedia}
	}
	if m.Purged != nil {
		return purgeResultJSON{Kind: kind, Outcome: outcomeAlreadyPurged}
	}
	// The same containment check the clip handlers and retention make,
	// against this kind's own root. A row whose path escapes its root is
	// refused, not deleted.
	got, err := media.Remove(s.mediaRoot(kind), m.Path)
	switch {
	case errors.Is(err, media.ErrOutsideRoot):
		s.log.Error("a media row names a path outside its directory, so it was not deleted",
			"event", id, "kind", kind, "err", err)
		return purgeResultJSON{Kind: kind, Outcome: outcomeRefused, Reason: escapedRootReason}
	case err != nil:
		s.log.Error("deleting a clip file failed", "event", id, "kind", kind, "err", err)
		return purgeResultJSON{Kind: kind, Outcome: outcomeRefused,
			Reason: "the file could not be deleted. The detail is in the log."}
	case !got.Existed:
		// Already gone. Record the purge anyway, with no bytes: what is
		// recorded is the owner's decision, not the free space.
		return purgeResultJSON{Kind: kind, Outcome: outcomeAlreadyGone}
	}
	return purgeResultJSON{Kind: kind, Outcome: outcomePurged, Bytes: got.Bytes}
}

// purgeIDs checks the event numbers and drops repeats, keeping the order
// the caller gave.
func purgeIDs(w http.ResponseWriter, given []int64) ([]int64, bool) {
	if len(given) == 0 {
		fail(w, http.StatusBadRequest, "name at least one event to delete the recordings of.")
		return nil, false
	}
	if len(given) > maxPurgeEvents {
		fail(w, http.StatusBadRequest, fmt.Sprintf(
			"one request may name at most %d events, and this one names %d. Split it up.",
			maxPurgeEvents, len(given)))
		return nil, false
	}
	seen := make(map[int64]bool, len(given))
	out := make([]int64, 0, len(given))
	for _, id := range given {
		if id <= 0 {
			fail(w, http.StatusBadRequest,
				"every event number must be a whole number above zero.")
			return nil, false
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, true
}

// purgeKinds reads the kinds to delete. Absent means both.
func purgeKinds(w http.ResponseWriter, given *[]string) ([]string, bool) {
	if given == nil {
		return purgeableKinds, true
	}
	out := make([]string, 0, len(*given))
	for _, kind := range *given {
		if !slices.Contains(purgeableKinds, kind) {
			fail(w, http.StatusBadRequest, "kinds may name audio, video, or both.")
			return nil, false
		}
		if !slices.Contains(out, kind) {
			out = append(out, kind)
		}
	}
	if len(out) == 0 {
		fail(w, http.StatusBadRequest, "kinds may name audio, video, or both.")
		return nil, false
	}
	return out, true
}

// --- usage ---

type mediaUsageJSON struct {
	Files  int   `json:"files"`
	Events int   `json:"events"`
	Bytes  int64 `json:"bytes"`
}

type mediaSpaceJSON struct {
	Audio     mediaUsageJSON `json:"audio"`
	Video     mediaUsageJSON `json:"video"`
	Purged    mediaUsageJSON `json:"purged"`
	Purgeable mediaUsageJSON `json:"purgeable"`
	// StartedBefore repeats the filter the caller asked for, so a reply the
	// interface holds on to says what question it answers.
	StartedBefore int64 `json:"started_before"`
}

// handleMediaUsage says how much space the clip files take and how much a
// purge would reclaim, so the interface can state the cost before the owner
// presses anything.
func (s *Server) handleMediaUsage(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "started_before", "event_id")
	before := p.ms("started_before")
	ids := p.manyIDs("event_id", maxPurgeEvents)
	if !p.ok(w) {
		return
	}
	space, err := s.cfg.Store.MediaSpace(r.Context(),
		store.MediaFilter{StartedBeforeMS: before, EventIDs: ids})
	if err != nil {
		s.serverError(w, "measuring the clip files", err)
		return
	}
	writeJSON(w, http.StatusOK, mediaSpaceJSON{
		Audio:         toUsageJSON(space.Audio),
		Video:         toUsageJSON(space.Video),
		Purged:        toUsageJSON(space.Purged),
		Purgeable:     toUsageJSON(space.Purgeable),
		StartedBefore: before,
	})
}

func toUsageJSON(u store.MediaUsage) mediaUsageJSON {
	return mediaUsageJSON{Files: u.Files, Events: u.Events, Bytes: u.Bytes}
}

// --- the list a purge by age will act on ---

type purgeableJSON struct {
	EventIDs []int64 `json:"event_ids"`
	// More is true when the filter matches more events than the limit
	// returned. The interface says so rather than quietly deleting part of
	// what the owner was shown.
	More bool `json:"more"`
}

// handlePurgeable lists the events that still hold a clip and started
// before an instant, oldest first.
//
// Purging by age is two steps on purpose: read what matches, then delete
// exactly those events. Nothing is ever deleted that the owner was not
// shown the count of first, and the age is resolved once rather than twice,
// so an event that arrives between the two steps cannot be swept up.
func (s *Server) handlePurgeable(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "started_before", "limit")
	before := p.ms("started_before")
	limit := p.intIn("limit", maxPurgeEvents, 1, maxPurgeEvents)
	if !p.ok(w) {
		return
	}
	// One more than asked for, so the answer can say there are more without
	// counting the whole table again.
	ids, err := s.cfg.Store.PurgeableEvents(r.Context(),
		store.MediaFilter{StartedBeforeMS: before}, limit+1, 0)
	if err != nil {
		s.serverError(w, "listing the recordings that can be deleted", err)
		return
	}
	out := purgeableJSON{EventIDs: []int64{}}
	if len(ids) > limit {
		out.More, ids = true, ids[:limit]
	}
	out.EventIDs = append(out.EventIDs, ids...)
	writeJSON(w, http.StatusOK, out)
}
