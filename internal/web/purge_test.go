package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/store"
)

const purgeURL = "/api/media/purge"

// purgeBody is the request body, built here rather than by the code under
// test, so a change to the contract shows up as a failing test.
func purgeBody(ids []int64, kinds []string) map[string]any {
	body := map[string]any{"event_ids": ids, "confirm": true}
	if kinds != nil {
		body["kinds"] = kinds
	}
	return body
}

// purgeAs is the login the tests purge under, so the record can be checked
// for naming a person. It is the header the identity middleware reads.
var purgeAs = []string{"Tailscale-User-Login", "alex@example.com"}

// purge sends a purge request as alex@example.com.
func (e *env) purge(body map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, purgeURL, body, purgeAs...)
}

// clipWithVideo stores an event with an audio clip and a video clip, and
// returns the event number and the two paths.
func (e *env) clipWithVideo(videoDir string, offset time.Duration) (int64, string, string) {
	e.t.Helper()
	id := e.addEvent(offset, 2*time.Second, 62, detect.Jumping)
	e.addClip(id, 1000, []int16{10, -20, 30, -40})
	audioPath := filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")
	videoPath := filepath.Join(videoDir, strconv.FormatInt(id, 10)+".mp4")
	e.addVideo(id, videoPath, []byte("a small mp4 that stands in for the camera's"), 0)
	return id, audioPath, videoPath
}

// purgeCounts reads the totals out of a decoded purge reply.
func purgeCounts(t *testing.T, m map[string]any) (files int, bytes int64) {
	t.Helper()
	return int(num(t, m, "purged_files")), int64(num(t, m, "purged_bytes"))
}

// outcomes maps kind to outcome for one event in a purge reply.
func outcomes(t *testing.T, m map[string]any, eventID int64) map[string]string {
	t.Helper()
	for _, one := range list(t, m, "events") {
		if int64(num(t, one, "event_id")) != eventID {
			continue
		}
		out := map[string]string{}
		for _, r := range list(t, one, "results") {
			out[str(t, r, "kind")] = str(t, r, "outcome")
		}
		return out
	}
	t.Fatalf("event %d is not in the reply: %v", eventID, m)
	return nil
}

// The whole point of the feature, and the whole risk of it: the files go,
// and every row stays (SPEC.md section 3 rule 3, section 15 decision 21).
func TestPurgeDeletesTheFilesAndKeepsEveryRow(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	e.addBins(bin(0, 41, 50, 33), bin(1, 42, 51, 33), bin(2, 43, 52, 33))
	id, audioPath, videoPath := e.clipWithVideo(videoDir, 0)
	e.addReview(id, "verified", "the kids again", "alex")

	before, err := e.store.EventMedia(t.Context(), id)
	if err != nil || len(before) != 2 {
		t.Fatalf("the event has %d media rows before the purge, %v", len(before), err)
	}

	w := e.purge(purgeBody([]int64{id}, nil))
	if w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	reply := decode(t, w)
	if files, _ := purgeCounts(t, reply); files != 2 {
		t.Errorf("purged_files = %d, want 2", files)
	}
	got := outcomes(t, reply, id)
	if got["audio"] != "purged" || got["video"] != "purged" {
		t.Errorf("outcomes = %v, want both purged", got)
	}

	// The files are gone.
	for _, path := range []string{audioPath, videoPath} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s is still on disk", filepath.Base(path))
		}
	}

	// The event row is still there, with its level and its class.
	if _, err := e.store.Event(t.Context(), id); err != nil {
		t.Errorf("the event row is gone: %v", err)
	}
	// The seconds around it are still there.
	points, err := e.store.Samples(t.Context(), ms(0), ms(3*time.Second))
	if err != nil || len(points) != 3 {
		t.Errorf("samples = %d rows, %v; want 3", len(points), err)
	}
	// The review and its note are still there.
	rows, _, err := e.store.ListEvents(t.Context(), store.EventFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Review == nil || rows[0].Review.Note != "the kids again" {
		t.Errorf("the review is gone: %v, %v", rows, err)
	}
	// The media rows are still there, with the size and the hash the
	// collector wrote. They are the record that the clips existed.
	after, err := e.store.EventMedia(t.Context(), id)
	if err != nil || len(after) != 2 {
		t.Fatalf("the event has %d media rows after the purge, %v", len(after), err)
	}
	for i := range after {
		if after[i].Bytes != before[i].Bytes || after[i].SHA256 != before[i].SHA256 ||
			after[i].Duration != before[i].Duration {
			t.Errorf("the %s row changed: %+v, was %+v", after[i].Kind, after[i], before[i])
		}
		if after[i].Purged == nil {
			t.Errorf("the %s row does not say it was purged", after[i].Kind)
		}
	}
}

// Rule 3 says every deletion is logged. One row per request, naming the
// events and the bytes, so the health log reads as a sentence rather than
// as one line per file.
func TestPurgeIsWrittenToTheHealthLogOncePerRequest(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	first, _, _ := e.clipWithVideo(videoDir, 0)
	second, _, _ := e.clipWithVideo(videoDir, time.Minute)

	type record struct{ kind, detail string }
	var records []record
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, detail string, _ time.Duration) {
		records = append(records, record{kind, detail})
	}

	w := e.purge(purgeBody([]int64{first, second}, nil))
	if w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	if len(records) != 1 {
		t.Fatalf("health records = %v, want exactly one", records)
	}
	if records[0].kind != "media_purge" {
		t.Errorf("health kind = %q, want media_purge", records[0].kind)
	}
	for _, want := range []string{strconv.FormatInt(first, 10), strconv.FormatInt(second, 10), "alex@example.com"} {
		if !contains(records[0].detail, want) {
			t.Errorf("the health detail %q does not name %q", records[0].detail, want)
		}
	}
}

// Deleting a recording cannot be undone, so the request has to say it
// meant it. Without the confirmation nothing is deleted.
func TestPurgeWithoutConfirmationDeletesNothing(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, _ := e.clipWithVideo(videoDir, 0)

	for _, body := range []map[string]any{
		{"event_ids": []int64{id}},
		{"event_ids": []int64{id}, "confirm": false},
	} {
		w := e.purge(body)
		wantError(t, w, http.StatusBadRequest, "confirm")
	}
	if _, err := os.Stat(audioPath); err != nil {
		t.Errorf("the clip was deleted without a confirmation: %v", err)
	}
	purged, err := e.store.PurgedMedia(t.Context(), []int64{id})
	if err != nil || len(purged) != 0 {
		t.Errorf("purges = %v, %v; want none", purged, err)
	}
}

// A path that leads outside its media root is refused, never deleted. Only
// the collector writes these rows, so this is the second line of defense a
// restored or imported database needs.
func TestPurgeRefusesAPathOutsideTheMediaRoot(t *testing.T) {
	e, videoDir := videoEnv(t, nil)

	outsideClips := filepath.Join(filepath.Dir(e.clipDir), "secret.wav")
	if err := os.WriteFile(outsideClips, []byte("not for the browser"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideVideo := filepath.Join(filepath.Dir(videoDir), "secret.mp4")
	if err := os.WriteFile(outsideVideo, []byte("not for the browser either"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Two shapes of escape, because they fail differently: a relative path
	// that climbs out with "..", and an absolute path that was never under
	// the root at all. The absolute one really points at the file on disk,
	// so a check that is not there deletes it.
	for i, stored := range [][2]string{
		{filepath.Join("..", "secret.wav"), filepath.Join("..", "secret.mp4")},
		{outsideClips, outsideVideo},
	} {
		id := e.addEvent(time.Duration(i)*time.Minute, time.Second, 60, detect.Running)
		err := e.store.InsertMedia(t.Context(), store.Media{
			EventID: id, Kind: store.KindAudio, Path: stored[0],
			Bytes: 19, Duration: time.Second, SHA256: "abc",
		})
		if err != nil {
			t.Fatal(err)
		}
		err = e.store.InsertMedia(t.Context(), store.Media{
			EventID: id, Kind: store.KindVideo, Path: stored[1],
			Bytes: 26, Duration: time.Second, SHA256: "def",
		})
		if err != nil {
			t.Fatal(err)
		}

		w := e.purge(purgeBody([]int64{id}, nil))
		if w.Code != 200 {
			t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
		}
		reply := decode(t, w)
		got := outcomes(t, reply, id)
		if got["audio"] != "refused" || got["video"] != "refused" {
			t.Errorf("%v: outcomes = %v, want both refused", stored, got)
		}
		if files, bytes := purgeCounts(t, reply); files != 0 || bytes != 0 {
			t.Errorf("%v: purged %d files and %d bytes, want none", stored, files, bytes)
		}
		for _, path := range []string{outsideClips, outsideVideo} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%v: %s was deleted from outside the media root",
					stored, filepath.Base(path))
			}
		}
		if contains(w.Body.String(), "secret") {
			t.Errorf("%v: the answer names the file: %s", stored, w.Body.String())
		}
		// A refusal is not a purge. Recording one would claim the owner
		// deleted a file that is still there.
		purged, err := e.store.PurgedMedia(t.Context(), []int64{id})
		if err != nil || len(purged) != 0 {
			t.Errorf("%v: purges = %v, %v; want none", stored, purged, err)
		}
	}
}

// An id that is not an event must never reach a filesystem path, and must
// not cost the other events in the list.
func TestPurgeOfAnUnknownEventDeletesNothingForIt(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, _ := e.clipWithVideo(videoDir, 0)

	// On its own, an unknown id purges nothing at all.
	w := e.purge(purgeBody([]int64{id + 500}, nil))
	if w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	reply := decode(t, w)
	if files, bytes := purgeCounts(t, reply); files != 0 || bytes != 0 {
		t.Errorf("purged %d files and %d bytes for an unknown event", files, bytes)
	}
	unknown, ok := reply["unknown_events"].([]any)
	if !ok || len(unknown) != 1 || unknown[0] != float64(id+500) {
		t.Errorf("unknown_events = %v, want only %d", reply["unknown_events"], id+500)
	}
	if len(list(t, reply, "events")) != 0 {
		t.Errorf("an unknown event has a per-event result: %v", reply["events"])
	}
	purged, err := e.store.PurgedMedia(t.Context(), []int64{id, id + 500})
	if err != nil || len(purged) != 0 {
		t.Errorf("purges = %v, %v; want none", purged, err)
	}

	// Beside a real event, the real one is still purged.
	w = e.purge(purgeBody([]int64{id + 500, id}, nil))
	if w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(audioPath); err == nil {
		t.Error("the real event's clip was not purged")
	}
}

// A file that is already gone is not an error. The purge is recorded with
// zero bytes, so the record still says the clip was deliberately removed.
func TestPurgeOfAFileThatIsAlreadyGoneIsRecorded(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, _ := e.clipWithVideo(videoDir, 0)
	if err := os.Remove(audioPath); err != nil {
		t.Fatal(err)
	}

	w := e.purge(purgeBody([]int64{id}, []string{"audio"}))
	if w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	reply := decode(t, w)
	if got := outcomes(t, reply, id); got["audio"] != "already_gone" {
		t.Errorf("outcomes = %v, want audio already_gone", got)
	}
	if _, bytes := purgeCounts(t, reply); bytes != 0 {
		t.Errorf("purged_bytes = %d, want 0 for a file that was not there", bytes)
	}
	purged, err := e.store.PurgedMedia(t.Context(), []int64{id})
	if err != nil || len(purged[id]) != 1 || purged[id][0].Bytes != 0 {
		t.Errorf("purges = %v, %v; want one audio purge of 0 bytes", purged, err)
	}
}

// Asking twice is safe. The owner may select the same event again.
func TestPurgingTwiceReportsAlreadyPurged(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, _, _ := e.clipWithVideo(videoDir, 0)

	if w := e.purge(purgeBody([]int64{id}, []string{"audio"})); w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	w := e.purge(purgeBody([]int64{id}, []string{"audio"}))
	if w.Code != 200 {
		t.Fatalf("second POST purge = %d: %s", w.Code, w.Body.String())
	}
	reply := decode(t, w)
	if got := outcomes(t, reply, id); got["audio"] != "already_purged" {
		t.Errorf("outcomes = %v, want audio already_purged", got)
	}
	if files, _ := purgeCounts(t, reply); files != 0 {
		t.Errorf("purged_files = %d on a second purge, want 0", files)
	}
}

// Only the kinds asked for go. The owner may want the video off the disk
// and the audio kept, because the video is a hundred times the size.
func TestPurgeTakesOnlyTheKindsAsked(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, videoPath := e.clipWithVideo(videoDir, 0)

	w := e.purge(purgeBody([]int64{id}, []string{"video"}))
	if w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(videoPath); err == nil {
		t.Error("the video is still on disk")
	}
	if _, err := os.Stat(audioPath); err != nil {
		t.Errorf("the audio clip was deleted although only video was asked for: %v", err)
	}
	got := outcomes(t, decode(t, w), id)
	if _, asked := got["audio"]; asked {
		t.Errorf("the reply reports an audio result: %v", got)
	}
}

// An event with no clip of a kind is not an error and writes nothing.
func TestPurgeOfAnEventWithNoClipWritesNothing(t *testing.T) {
	e, _ := videoEnv(t, nil)
	id := e.addEvent(0, time.Second, 60, detect.Running)

	w := e.purge(purgeBody([]int64{id}, nil))
	if w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	got := outcomes(t, decode(t, w), id)
	if got["audio"] != "no_media" || got["video"] != "no_media" {
		t.Errorf("outcomes = %v, want both no_media", got)
	}
	purged, err := e.store.PurgedMedia(t.Context(), []int64{id})
	if err != nil || len(purged) != 0 {
		t.Errorf("purges = %v, %v; want none", purged, err)
	}
}

func TestPurgeRejectsABadRequest(t *testing.T) {
	e, _ := videoEnv(t, nil)
	many := make([]int64, 501)
	for i := range many {
		many[i] = int64(i + 1)
	}
	for _, tc := range []struct {
		what string
		body map[string]any
		says string
	}{
		{"no events", purgeBody(nil, nil), "event"},
		{"an empty list", purgeBody([]int64{}, nil), "event"},
		{"too many events", purgeBody(many, nil), "500"},
		{"a zero id", purgeBody([]int64{0}, nil), "whole number"},
		{"a negative id", purgeBody([]int64{-3}, nil), "whole number"},
		{"an unknown kind", purgeBody([]int64{1}, []string{"transcript"}), "audio"},
		{"an empty list of kinds", purgeBody([]int64{1}, []string{}), "audio"},
	} {
		w := e.purge(tc.body)
		wantError(t, w, http.StatusBadRequest, tc.says)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", tc.what, w.Code)
		}
	}
}

// --- serving a purged clip ---

// A purged clip is not a fault. It answers 410 Gone, which says "it was
// here and it is not any more", and writes no health alarm about
// something the owner did on purpose.
func TestAPurgedClipAnswers410AndWritesNoHealthRow(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, _, _ := e.clipWithVideo(videoDir, 0)
	if w := e.purge(purgeBody([]int64{id}, nil)); w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}

	var kinds []string
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, _ string, _ time.Duration) {
		kinds = append(kinds, kind)
	}

	text := strconv.FormatInt(id, 10)
	for _, target := range []string{
		"/api/events/" + text + "/audio",
		"/api/events/" + text + "/audio/original",
		"/api/events/" + text + "/waveform",
		"/api/events/" + text + "/video",
	} {
		w := e.get(target)
		if w.Code != http.StatusGone {
			t.Fatalf("GET %s = %d, want 410: %s", target, w.Code, w.Body.String())
		}
		wantError(t, w, http.StatusGone, "deleted")
	}
	// Reading the detail must not raise an alarm either.
	e.getJSON("/api/events/" + text)
	if len(kinds) != 0 {
		t.Errorf("health records = %v, want none for a purged clip", kinds)
	}
}

// Retention records its purges under the name "retention", so a clip it
// took answers 410 Gone naming retention and the date, and raises no alarm
// about a missing file. This is what tells a retained-away clip from a
// lost one on the dashboard.
func TestAClipRetentionTookAnswers410NamingRetention(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, videoPath := e.clipWithVideo(videoDir, 0)
	for _, path := range []string{audioPath, videoPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	err := e.store.RecordPurge(t.Context(), []store.Purge{
		{EventID: id, Kind: store.KindAudio, Bytes: 1000, At: t0, By: "retention"},
		{EventID: id, Kind: store.KindVideo, Bytes: 2000, At: t0, By: "retention"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var kinds []string
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, _ string, _ time.Duration) {
		kinds = append(kinds, kind)
	}

	text := strconv.FormatInt(id, 10)
	for _, target := range []string{"/api/events/" + text + "/audio", "/api/events/" + text + "/video"} {
		w := e.get(target)
		if w.Code != http.StatusGone {
			t.Fatalf("GET %s = %d, want 410: %s", target, w.Code, w.Body.String())
		}
		wantError(t, w, http.StatusGone, "retention")
		wantError(t, w, http.StatusGone, "11 September 2026")
	}
	detail := e.getJSON("/api/events/" + text)
	for _, m := range list(t, detail, "media") {
		if got := str(t, m, "purged_by"); got != "retention" {
			t.Errorf("the %s media says purged_by = %q, want retention", str(t, m, "kind"), got)
		}
	}
	if len(kinds) != 0 {
		t.Errorf("health records = %v, want none for a clip retention took", kinds)
	}
}

// The detail view has to be able to say the recording was deleted on a
// date by a person, and that is different from "the file has vanished".
func TestEventDetailSaysWhenAndByWhomAClipWasPurged(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, _, _ := e.clipWithVideo(videoDir, 0)
	if w := e.purge(purgeBody([]int64{id}, []string{"audio"})); w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}

	m := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
	var audio, video map[string]any
	for _, one := range list(t, m, "media") {
		switch str(t, one, "kind") {
		case "audio":
			audio = one
		case "video":
			video = one
		}
	}
	if audio == nil || video == nil {
		t.Fatalf("media = %v, want an audio and a video entry", m["media"])
	}
	if audio["purged_ms"] == nil {
		t.Errorf("purged_ms is null for a purged clip: %v", audio)
	}
	if audio["purged_by"] != "alex@example.com" {
		t.Errorf("purged_by = %v, want alex@example.com", audio["purged_by"])
	}
	// A purge is not a vanishing. Reporting it as missing would call the
	// owner's own decision a fault.
	if audio["missing"] != false {
		t.Errorf("missing = %v for a purged clip, want false", audio["missing"])
	}
	if video["purged_ms"] != nil || video["purged_by"] != nil {
		t.Errorf("the video entry claims a purge: %v", video)
	}
}

// A clip that vanished on its own still reads as a fault. The purge must
// not swallow the case it was built to be told apart from.
func TestAVanishedClipIsStillReportedAsMissing(t *testing.T) {
	e := newEnv(t)
	var kinds []string
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, _ string, _ time.Duration) {
		kinds = append(kinds, kind)
	}
	id := e.clipEvent([]int16{0, 800})
	if err := os.Remove(filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")); err != nil {
		t.Fatal(err)
	}
	w := e.get("/api/events/" + strconv.FormatInt(id, 10) + "/audio")
	if w.Code != http.StatusNotFound {
		t.Errorf("a vanished clip answered %d, want 404", w.Code)
	}
	if len(kinds) != 1 || kinds[0] != "media_missing" {
		t.Errorf("health records = %v, want one media_missing", kinds)
	}
}

// --- usage ---

func TestMediaUsageSaysWhatIsHeldAndWhatCanBeReclaimed(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	old, _, _ := e.clipWithVideo(videoDir, -72*time.Hour)
	e.clipWithVideo(videoDir, -time.Hour)

	m := e.getJSON("/api/media/usage")
	audio := object(t, m, "audio")
	video := object(t, m, "video")
	if num(t, audio, "files") != 2 || num(t, video, "files") != 2 {
		t.Errorf("files = %v audio, %v video; want 2 each", audio["files"], video["files"])
	}
	if num(t, audio, "bytes") <= 0 || num(t, video, "bytes") <= 0 {
		t.Errorf("bytes = %v audio, %v video; want the stored sizes", audio["bytes"], video["bytes"])
	}
	purgeable := object(t, m, "purgeable")
	if num(t, purgeable, "files") != 4 || num(t, purgeable, "events") != 2 {
		t.Errorf("purgeable = %v, want 4 files across 2 events", purgeable)
	}
	if num(t, purgeable, "bytes") != num(t, audio, "bytes")+num(t, video, "bytes") {
		t.Errorf("purgeable bytes = %v, want the audio and video held", purgeable["bytes"])
	}
	if num(t, object(t, m, "purged"), "files") != 0 {
		t.Errorf("purged = %v, want nothing yet", m["purged"])
	}

	// An age narrows what is purgeable without changing what is held.
	cut := strconv.FormatInt(ms(-24*time.Hour), 10)
	narrowed := e.getJSON("/api/media/usage?started_before=" + cut)
	if got := object(t, narrowed, "purgeable"); num(t, got, "files") != 2 || num(t, got, "events") != 1 {
		t.Errorf("purgeable before the cut = %v, want 2 files across 1 event", got)
	}
	if got := object(t, narrowed, "audio"); num(t, got, "files") != 2 {
		t.Errorf("audio held = %v, want 2 files whatever the cut is", got)
	}

	// A purge moves bytes from held to reclaimed.
	if w := e.purge(purgeBody([]int64{old}, nil)); w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	after := e.getJSON("/api/media/usage")
	if got := object(t, after, "purged"); num(t, got, "files") != 2 || num(t, got, "events") != 1 {
		t.Errorf("purged = %v, want 2 files across 1 event", got)
	}
	if got := object(t, after, "audio"); num(t, got, "files") != 1 {
		t.Errorf("audio held after the purge = %v, want 1 file", got)
	}
}

func TestMediaUsageRejectsABadParameter(t *testing.T) {
	e, _ := videoEnv(t, nil)
	wantError(t, e.get("/api/media/usage?older=30"), http.StatusBadRequest, "older")
	wantError(t, e.get("/api/media/usage?started_before=soon"), http.StatusBadRequest, "started_before")
}

// The age control needs the list a purge will act on, not only its size.
// Purging by age is two steps: read what matches, then delete exactly
// those events, so nothing is deleted that the owner was not shown.
func TestPurgeableListsTheOldestEventsThatStillHoldAClip(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	oldest, _, _ := e.clipWithVideo(videoDir, -72*time.Hour)
	middle, _, _ := e.clipWithVideo(videoDir, -48*time.Hour)
	e.clipWithVideo(videoDir, time.Hour)
	// An event with no clip is not purgeable, however old it is.
	e.addEvent(-96*time.Hour, time.Second, 60, detect.Running)

	cut := strconv.FormatInt(ms(0), 10)
	m := e.getJSON("/api/media/purgeable?started_before=" + cut)
	ids := m["event_ids"]
	want := []any{float64(oldest), float64(middle)}
	if got, ok := ids.([]any); !ok || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("event_ids = %v, want %v, oldest first", ids, want)
	}
	if m["more"] != false {
		t.Errorf("more = %v, want false", m["more"])
	}

	// A limit says so, rather than quietly returning part of the answer.
	one := e.getJSON("/api/media/purgeable?started_before=" + cut + "&limit=1")
	if got, ok := one["event_ids"].([]any); !ok || len(got) != 1 || got[0] != want[0] {
		t.Errorf("with a limit of 1, event_ids = %v, want only %d", one["event_ids"], oldest)
	}
	if one["more"] != true {
		t.Errorf("more = %v with a limit of 1, want true", one["more"])
	}

	// A purged event drops out of the list.
	if w := e.purge(purgeBody([]int64{oldest}, nil)); w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	after := e.getJSON("/api/media/purgeable?started_before=" + cut)
	if got, ok := after["event_ids"].([]any); !ok || len(got) != 1 || got[0] != float64(middle) {
		t.Errorf("after the purge, event_ids = %v, want only %d", after["event_ids"], middle)
	}
}

func TestPurgeableRejectsABadParameter(t *testing.T) {
	e, _ := videoEnv(t, nil)
	wantError(t, e.get("/api/media/purgeable?days=30"), http.StatusBadRequest, "days")
	wantError(t, e.get("/api/media/purgeable?limit=0"), http.StatusBadRequest, "limit")
	wantError(t, e.get("/api/media/purgeable?limit=501"), http.StatusBadRequest, "limit")
}

// The date in the message is the day the owner deleted it, on their own
// clock. A server that answers in UTC tells somebody in New York at 8pm
// that they deleted it tomorrow.
func TestThePurgeDateIsTheOwnersDayNotUTC(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	// t0 is 03:00 UTC on 11 September, which is 23:00 on 10 September in
	// New York. The two calendar days differ, so the answer cannot pass by
	// accident.
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone database on this machine: %v", err)
	}
	e.srv.loc = zone

	id, _, _ := e.clipWithVideo(videoDir, 0)
	if w := e.purge(purgeBody([]int64{id}, []string{"audio"})); w.Code != 200 {
		t.Fatalf("POST purge = %d: %s", w.Code, w.Body.String())
	}
	w := e.get("/api/events/" + strconv.FormatInt(id, 10) + "/audio")
	if w.Code != http.StatusGone {
		t.Fatalf("GET audio = %d, want 410", w.Code)
	}
	body := w.Body.String()
	if !contains(body, "10 September 2026") {
		t.Errorf("the message does not name the owner's day: %s", body)
	}
	if contains(body, "11 September 2026") {
		t.Errorf("the message names the UTC day instead of the owner's: %s", body)
	}
}
