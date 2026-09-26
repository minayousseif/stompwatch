package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/store"
)

// videoEnv is a server with a video directory beside the clip directory.
func videoEnv(t *testing.T, camera func() CameraStatus) (*env, string) {
	t.Helper()
	var videoDir string
	e := newEnv(t, func(c *Config, file *config.Config) {
		videoDir = filepath.Join(filepath.Dir(file.ClipDir), "video")
		c.VideoDir = videoDir
		c.Camera = camera
		file.CameraHost = "cam.local"
		file.VideoDir = videoDir
	})
	if err := os.MkdirAll(videoDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return e, videoDir
}

// addVideo stores a video row for an event, with the file at path when
// body is not nil. The clip carries no camera audio, as one cut with
// camera_audio off does.
func (e *env) addVideo(id int64, path string, body []byte, started time.Duration) {
	e.t.Helper()
	e.addVideoWithAudio(id, path, body, started, false)
}

// addVideoWithAudio is the same, saying whether the clip carries the
// camera's own audio track.
func (e *env) addVideoWithAudio(id int64, path string, body []byte, started time.Duration, audio bool) {
	e.t.Helper()
	if body != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o440); err != nil {
			e.t.Fatal(err)
		}
	}
	err := e.store.InsertMedia(e.t.Context(), store.Media{
		EventID: id, Kind: store.KindVideo, Path: path, Bytes: int64(len(body)),
		Duration: 40 * time.Second, SHA256: "feed", Started: t0.Add(started),
		CameraAudio: audio,
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func videoURL(id int64) string { return "/api/events/" + strconv.FormatInt(id, 10) + "/video" }

// The clip is served as MP4, byte for byte, and a range request gets the
// range: a browser plays a video by asking for it in pieces.
func TestVideoIsServedWithRangeRequests(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id := e.addEvent(0, 5*time.Second, 60, detect.Jumping)
	body := []byte("0123456789abcdef")
	// The clip starts on the segment edge before the 10 s pre-roll.
	e.addVideo(id, filepath.Join(videoDir, "2026", "09", "11", "1.mp4"), body, -12*time.Second)

	w := e.get(videoURL(id))
	if w.Code != 200 {
		t.Fatalf("GET video = %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4", ct)
	}
	if w.Body.String() != string(body) {
		t.Errorf("body = %q, want the file", w.Body.String())
	}
	if w.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", w.Header().Get("Accept-Ranges"))
	}

	w = e.get(videoURL(id), "Range", "bytes=2-5")
	if w.Code != http.StatusPartialContent {
		t.Fatalf("range GET = %d, want 206", w.Code)
	}
	if w.Body.String() != "2345" {
		t.Errorf("range body = %q, want 2345", w.Body.String())
	}

	// The detail names the clip, with the audio-only fields null.
	m := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
	media := list(t, m, "media")
	if len(media) != 1 || str(t, media[0], "kind") != "video" {
		t.Fatalf("media = %v", media)
	}
	v := media[0]
	if num(t, v, "bytes") != 16 || num(t, v, "duration_ms") != 40000 || str(t, v, "sha256") != "feed" {
		t.Errorf("video entry = %v", v)
	}
	if got := num(t, v, "start_ms"); int64(got) != ms(-12*time.Second) {
		t.Errorf("start_ms = %v, want %d", got, ms(-12*time.Second))
	}
	if got := num(t, v, "missing_head_ms"); got != 0 {
		t.Errorf("missing_head_ms = %v, want 0 for a clip padded before the pre-roll", got)
	}
	for _, key := range []string{"rate_hz", "peak_dbfs", "playback_gain_db"} {
		if v[key] != nil {
			t.Errorf("%s = %v, want null for video", key, v[key])
		}
	}
	if v["missing"] != false {
		t.Errorf("missing = %v", v["missing"])
	}
	if _, ok := v["path"]; ok {
		t.Error("the media entry exposes the path")
	}
}

// A row whose path leads outside video_dir is refused. The clip directory
// is not the video directory: a video row must not be able to read an
// audio clip, and neither may read the rest of the disk.
func TestVideoOutsideTheVideoDirIsRefused(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	secret := filepath.Join(filepath.Dir(videoDir), "secret.txt")
	if err := os.WriteFile(secret, []byte("not for the browser"), 0o600); err != nil {
		t.Fatal(err)
	}
	inClips := filepath.Join(e.clipDir, "2026", "09", "11", "3.wav")
	if err := os.MkdirAll(filepath.Dir(inClips), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inClips, []byte("audio bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, stored := range []string{
		secret,
		"../secret.txt",
		filepath.Join(videoDir, "..", "secret.txt"),
		inClips,
	} {
		id := e.addEvent(time.Duration(i)*time.Minute, 5*time.Second, 60, detect.Jumping)
		e.addVideo(id, stored, nil, 0)
		w := e.get(videoURL(id))
		if w.Code == 200 || w.Code == 206 {
			t.Errorf("%q was served: %d %q", stored, w.Code, w.Body.String())
			continue
		}
		if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), videoDir) {
			t.Errorf("%q: the answer names the path: %s", stored, w.Body.String())
		}
	}
}

func TestMissingVideoIs404AndRecordedOnce(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	var details []string
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, detail string, _ time.Duration) {
		details = append(details, kind+": "+detail)
	}
	id := e.addEvent(0, 5*time.Second, 60, detect.Jumping)
	e.addVideo(id, filepath.Join(videoDir, "2026", "09", "11", "1.mp4"), nil, 0)

	for range 2 {
		w := e.get(videoURL(id))
		wantError(t, w, http.StatusNotFound, "no longer stored")
	}
	if len(details) != 1 || !strings.HasPrefix(details[0], "media_missing: ") || !strings.Contains(details[0], "video") {
		t.Fatalf("health records = %v, want one media_missing that names the video", details)
	}
	// The detail says the file is gone too.
	d := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
	if v := list(t, d, "media"); len(v) != 1 || v[0]["missing"] != true {
		t.Errorf("media = %v, want missing true", v)
	}
}

func TestNoVideoIs404(t *testing.T) {
	e, _ := videoEnv(t, nil)
	id := e.addEvent(0, 5*time.Second, 60, detect.Jumping)
	wantError(t, e.get(videoURL(id)), http.StatusNotFound, "no video")
	wantError(t, e.get(videoURL(99)), http.StatusNotFound, "no video")
}

// The system page says whether the camera is delivering, for how long it
// has, and how often it dropped (SPEC.md section 7).
func TestSystemReportsTheCamera(t *testing.T) {
	status := CameraStatus{
		Enabled: true, Connected: true, ConnectedSince: t0.Add(-30 * time.Minute),
		Uptime: 55 * time.Minute, Disconnects: 3, LastSegment: t0.Add(-4 * time.Second),
		RingSegments: 60, RingBytes: 90_000_000,
	}
	e, videoDir := videoEnv(t, func() CameraStatus { return status })
	id := e.addEvent(0, 5*time.Second, 60, detect.Jumping)
	e.addVideo(id, filepath.Join(videoDir, "2026", "09", "11", "1.mp4"), []byte("0123456789"), 0)

	m := e.getJSON("/api/system")
	cam := object(t, m, "camera")
	if cam["enabled"] != true || cam["connected"] != true {
		t.Errorf("camera = %v", cam)
	}
	if got := num(t, cam, "uptime_ms"); got != 55*60*1000 {
		t.Errorf("uptime_ms = %v", got)
	}
	if got := num(t, cam, "disconnects"); got != 3 {
		t.Errorf("disconnects = %v", got)
	}
	if got := num(t, cam, "connected_since_ms"); int64(got) != ms(-30*time.Minute) {
		t.Errorf("connected_since_ms = %v", got)
	}
	if got := num(t, cam, "last_segment_ms"); int64(got) != ms(-4*time.Second) {
		t.Errorf("last_segment_ms = %v", got)
	}
	ring := object(t, cam, "ring")
	if num(t, ring, "segments") != 60 || num(t, ring, "bytes") != 90_000_000 {
		t.Errorf("ring = %v", ring)
	}
	clips := object(t, cam, "clips")
	if num(t, clips, "count") != 1 || num(t, clips, "bytes") != 10 {
		t.Errorf("clips = %v", clips)
	}
	var names []string
	for _, d := range list(t, m, "disk") {
		names = append(names, str(t, d, "name"))
	}
	if strings.Join(names, ",") != "database,clips,video" {
		t.Errorf("disk stores = %v", names)
	}

	// With no camera the object is there and says so, so the interface
	// never has to guess from a missing key.
	e2 := newEnv(t)
	cam = object(t, e2.getJSON("/api/system"), "camera")
	if cam["enabled"] != false || cam["connected"] != false || num(t, cam, "disconnects") != 0 {
		t.Errorf("camera with video off = %v", cam)
	}
}

// A video that suddenly speaks is a surprise worth removing, so the clip
// says whether it carries the camera's audio. The answer is the clip's own,
// not the setting in force now (SPEC.md section 15 decision 22).
func TestTheVideoEntrySaysWhetherTheClipHasSound(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	quiet := e.addEvent(0, 5*time.Second, 60, detect.Jumping)
	e.addVideo(quiet, filepath.Join(videoDir, "2026", "09", "11", "1.mp4"), []byte("q"), -12*time.Second)
	loud := e.addEvent(time.Minute, 5*time.Second, 60, detect.Jumping)
	e.addVideoWithAudio(loud, filepath.Join(videoDir, "2026", "09", "11", "2.mp4"),
		[]byte("l"), -12*time.Second+time.Minute, true)

	for _, tc := range []struct {
		id   int64
		want bool
	}{{quiet, false}, {loud, true}} {
		m := e.getJSON("/api/events/" + strconv.FormatInt(tc.id, 10))
		media := list(t, m, "media")
		if len(media) != 1 {
			t.Fatalf("event %d: media = %v", tc.id, media)
		}
		got, ok := media[0]["camera_audio"].(bool)
		if !ok {
			t.Fatalf("event %d: camera_audio = %v, want a boolean", tc.id, media[0]["camera_audio"])
		}
		if got != tc.want {
			t.Errorf("event %d: camera_audio = %v, want %v", tc.id, got, tc.want)
		}
	}

	// An audio clip is the measuring microphone's, and is filtered. It never
	// carries camera audio, whatever the setting says.
	withAudio := e.addEvent(2*time.Minute, 5*time.Second, 60, detect.Jumping)
	e.addClip(withAudio, 1000, []int16{1, 2, 3, 4})
	m := e.getJSON("/api/events/" + strconv.FormatInt(withAudio, 10))
	for _, one := range list(t, m, "media") {
		if str(t, one, "kind") != "audio" {
			continue
		}
		if got, _ := one["camera_audio"].(bool); got {
			t.Error("an audio clip reports camera_audio = true")
		}
	}
}
