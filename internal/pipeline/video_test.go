package pipeline

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/testsignal"
	"github.com/minayousseif/stompwatch/internal/video"
)

// fakeVideo stands in for the video writer. It records what it was asked
// for and answers with what the test set.
type fakeVideo struct {
	mu    sync.Mutex
	calls []struct {
		id       int64
		from, to time.Time
	}
	clip video.Clip
	err  error
}

func (f *fakeVideo) Ready(time.Time) bool { return true }

func (f *fakeVideo) Save(_ context.Context, id int64, from, to time.Time) (video.Clip, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct {
		id       int64
		from, to time.Time
	}{id, from, to})
	if f.err != nil {
		return video.Clip{}, f.err
	}
	c := f.clip
	c.Start = from
	return c, nil
}

// runThumps runs a minute of quiet room with four jumps through the
// pipeline, which the test settings detect as one event.
func runThumps(t *testing.T, e *env) {
	t.Helper()
	x := testsignal.Pink(60*testsignal.Rate, 0.001, 7)
	testsignal.AddThumps(x, 0.5, 40, 42, 44, 46)
	wavPath := filepath.Join(e.dir, "in.wav")
	var buf bytes.Buffer
	if err := testsignal.WriteWAV24(&buf, x); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wavPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := audio.NewFileSource(wavPath, 0, t0, e.chunks)
	if err != nil {
		t.Fatal(err)
	}
	srcErr := make(chan error, 1)
	go func() { srcErr <- src.Run(context.Background()); close(e.chunks) }()
	if err := e.pipe.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := <-srcErr; err != nil {
		t.Fatalf("file source: %v", err)
	}
}

// SPEC.md section 7: on an event, the video clip spanning pre-roll to post-roll is
// written and recorded beside the audio clip.
func TestAnEventGetsAVideoClipWhenTheCameraIsOn(t *testing.T) {
	fv := &fakeVideo{clip: video.Clip{
		Path: "/video/2026/09/11/1.mp4", SHA256: "0123abcd", Bytes: 4321,
		Duration: 40 * time.Second, Truncated: false,
	}}
	e := newEnvWith(t, 16, nil)
	e.pipe.cfg.Video = fv
	runThumps(t, e)

	db := e.query(t)
	if n := count(t, db, `SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("%d events, want 1", n)
	}
	if n := count(t, db, `SELECT count(*) FROM event_media WHERE kind = 'audio'`); n != 1 {
		t.Errorf("%d audio rows, want 1", n)
	}
	var path, sum string
	var size, durMS, startMS, truncated int64
	err := db.QueryRow(`SELECT path, sha256, bytes, duration_ms, started_ms, truncated
		FROM event_media WHERE kind = 'video'`).Scan(&path, &sum, &size, &durMS, &startMS, &truncated)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("no video row was written for the event")
	}
	if err != nil {
		t.Fatal(err)
	}
	if path != "/video/2026/09/11/1.mp4" || sum != "0123abcd" || size != 4321 || durMS != 40000 || truncated != 0 {
		t.Errorf("video row: %s %s %d %d %d", path, sum, size, durMS, truncated)
	}

	var eventStart, eventEnd int64
	if err := db.QueryRow(`SELECT started_ms, ended_ms FROM events`).Scan(&eventStart, &eventEnd); err != nil {
		t.Fatal(err)
	}
	fv.mu.Lock()
	defer fv.mu.Unlock()
	if len(fv.calls) != 1 {
		t.Fatalf("Save was called %d times", len(fv.calls))
	}
	call := fv.calls[0]
	// The test settings ask for 10 s before and 5 s after.
	if got := call.from.UnixMilli(); got != eventStart-10_000 {
		t.Errorf("video from %d, want 10 s before the event start %d", got, eventStart)
	}
	if got := call.to.UnixMilli(); got != eventEnd+5_000 {
		t.Errorf("video to %d, want 5 s after the event end %d", got, eventEnd)
	}
	if startMS != call.from.UnixMilli() {
		t.Errorf("started_ms %d, want the clip start %d", startMS, call.from.UnixMilli())
	}
}

// A window the ring does not hold is said in the health log, and the
// audio clip is unaffected.
func TestNoVideoForAnEventIsRecorded(t *testing.T) {
	e := newEnvWith(t, 16, nil)
	e.pipe.cfg.Video = &fakeVideo{err: video.ErrNoVideo}
	runThumps(t, e)

	db := e.query(t)
	if n := count(t, db, `SELECT count(*) FROM event_media WHERE kind = 'audio'`); n != 1 {
		t.Errorf("%d audio rows, want 1", n)
	}
	if n := count(t, db, `SELECT count(*) FROM event_media WHERE kind = 'video'`); n != 0 {
		t.Errorf("%d video rows, want 0", n)
	}
	if n := count(t, db, `SELECT count(*) FROM system_health WHERE kind = 'clip_truncated' AND detail LIKE '%no video%'`); n != 1 {
		t.Errorf("%d health rows say there was no video, want 1", n)
	}
}

// A clip with a hole in it is stored, and the hole is in the health log.
func TestATruncatedVideoClipIsStoredAndRecorded(t *testing.T) {
	e := newEnvWith(t, 16, nil)
	e.pipe.cfg.Video = &fakeVideo{clip: video.Clip{Path: "/v/1.mp4", SHA256: "ff", Bytes: 1, Duration: time.Second, Truncated: true}}
	runThumps(t, e)

	db := e.query(t)
	if n := count(t, db, `SELECT count(*) FROM event_media WHERE kind = 'video' AND truncated = 1`); n != 1 {
		t.Errorf("%d truncated video rows, want 1", n)
	}
	if n := count(t, db, `SELECT count(*) FROM system_health WHERE kind = 'clip_truncated' AND detail LIKE '%video%'`); n != 1 {
		t.Errorf("%d health rows about the video, want 1", n)
	}
}

// The owner must find a clip that lost its audio from the instrument, not by
// running ffprobe by hand. A clip cut with camera_audio on that carries no
// audio track is a fault: it gets a health row of its own, and the row for
// the clip says the clip has no audio.
func TestAVideoClipThatLostItsAudioIsRecorded(t *testing.T) {
	e := newEnvWith(t, 16, nil)
	e.pipe.cfg.Video = &fakeVideo{clip: video.Clip{
		Path: "/v/1.mp4", SHA256: "ff", Bytes: 1, Duration: time.Second,
		CameraAudio:  false,
		AudioProblem: "camera_audio is on, and the clip carries no audio track at all",
	}}
	runThumps(t, e)

	db := e.query(t)
	if n := count(t, db, `SELECT count(*) FROM event_media WHERE kind = 'video' AND camera_audio = 0`); n != 1 {
		t.Errorf("%d video rows say the clip carries no audio, want 1", n)
	}
	if n := count(t, db, `SELECT count(*) FROM system_health
		WHERE kind = 'clip_no_audio' AND detail LIKE '%audio track%'`); n != 1 {
		t.Errorf("%d health rows about the missing audio track, want 1", n)
	}

	// A clip whose audio is there gets no row. A warning on every clip would
	// teach the owner to ignore it.
	e = newEnvWith(t, 16, nil)
	e.pipe.cfg.Video = &fakeVideo{clip: video.Clip{
		Path: "/v/2.mp4", SHA256: "ff", Bytes: 1, Duration: time.Second, CameraAudio: true,
	}}
	runThumps(t, e)
	if n := count(t, e.query(t), `SELECT count(*) FROM system_health WHERE kind = 'clip_no_audio'`); n != 0 {
		t.Errorf("%d health rows about a clip whose audio is there, want 0", n)
	}
}

// SPEC.md section 15 decision 22: the row for a video clip says whether the
// clip carries the camera's own audio. The writer knows, because the writer
// cut it; the pipeline must not read the setting again at some later moment,
// or a clip would be described by whatever the setting says then.
func TestTheVideoRowSaysWhetherTheClipCarriesCameraAudio(t *testing.T) {
	for _, audio := range []bool{false, true} {
		e := newEnvWith(t, 16, nil)
		e.pipe.cfg.Video = &fakeVideo{clip: video.Clip{
			Path: "/video/2026/09/11/1.mp4", SHA256: "0123abcd", Bytes: 4321,
			Duration: 40 * time.Second, CameraAudio: audio,
		}}
		runThumps(t, e)

		var stored int
		err := e.query(t).QueryRow(`SELECT camera_audio FROM event_media WHERE kind = 'video'`).Scan(&stored)
		if err != nil {
			t.Fatalf("audio = %v: %v", audio, err)
		}
		if (stored != 0) != audio {
			t.Errorf("a clip cut with camera audio = %v stored camera_audio = %d", audio, stored)
		}
	}
}
