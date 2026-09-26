package web

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/video"
)

// --- the health log ---

func (e *env) addHealth(at time.Duration, kind, detail string, d time.Duration) {
	e.t.Helper()
	if err := e.store.AddHealth(e.t.Context(), t0.Add(at), kind, detail, d); err != nil {
		e.t.Fatal(err)
	}
}

func TestHealthListIsNewestFirst(t *testing.T) {
	e := newEnv(t)
	e.addHealth(0, store.HealthCaptureGap, "the microphone went quiet", 1200*time.Millisecond)
	e.addHealth(time.Minute, store.HealthDiskLow, "free space is low", 0)
	e.addHealth(2*time.Minute, store.HealthWriteError, "a write failed", 0)

	m := e.getJSON("/api/health")
	if got := num(t, m, "total"); got != 3 {
		t.Fatalf("total = %v, want 3", got)
	}
	recs := list(t, m, "records")
	if got := str(t, recs[0], "kind"); got != "write_error" {
		t.Errorf("the newest record is %q, want \"write_error\"", got)
	}
	last := recs[2]
	if got := str(t, last, "kind"); got != "capture_gap" {
		t.Errorf("the oldest record is %q, want \"capture_gap\"", got)
	}
	if got := str(t, last, "detail"); got != "the microphone went quiet" {
		t.Errorf("detail = %q", got)
	}
	if got := num(t, last, "duration_ms"); got != 1200 {
		t.Errorf("duration_ms = %v, want 1200", got)
	}
	if got := num(t, last, "ts_ms"); int64(got) != ms(0) {
		t.Errorf("ts_ms = %v, want %d", got, ms(0))
	}
}

func TestHealthFiltersByKindAndRange(t *testing.T) {
	e := newEnv(t)
	e.addHealth(0, store.HealthCaptureGap, "one", 0)
	e.addHealth(time.Minute, store.HealthDiskLow, "two", 0)
	e.addHealth(2*time.Minute, store.HealthCaptureGap, "three", 0)

	m := e.getJSON("/api/health?kind=capture_gap")
	if got := num(t, m, "total"); got != 2 {
		t.Errorf("kind=capture_gap total = %v, want 2", got)
	}
	m = e.getJSON("/api/health?kind=capture_gap&kind=disk_low")
	if got := num(t, m, "total"); got != 3 {
		t.Errorf("two kinds total = %v, want 3", got)
	}
	m = e.getJSON("/api/health?from=" + strconv.FormatInt(ms(time.Minute), 10))
	if got := num(t, m, "total"); got != 2 {
		t.Errorf("from the first minute total = %v, want 2", got)
	}
	m = e.getJSON("/api/health?limit=1")
	if got := len(list(t, m, "records")); got != 1 {
		t.Errorf("limit=1 returned %d records", got)
	}
	if got := num(t, m, "total"); got != 3 {
		t.Errorf("limit=1 total = %v, want 3, the count before the limit", got)
	}
}

// The dashboard must be able to filter for every kind the instrument
// writes. A kind missing from the filter list is rejected as a typo, so the
// owner cannot read back the record of a reset at all.
func TestHealthFiltersForTheResetRecord(t *testing.T) {
	e := newEnv(t)
	e.addHealth(0, store.HealthDataReset, "the data was reset by alex", 0)

	m := e.getJSON("/api/health?kind=data_reset")
	if got := num(t, m, "total"); got != 1 {
		t.Fatalf("kind=data_reset total = %v, want 1", got)
	}
	if got := str(t, list(t, m, "records")[0], "detail"); got != "the data was reset by alex" {
		t.Errorf("detail = %q", got)
	}
}

func TestHealthRejectsBadParameters(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ query, names string }{
		{"?kind=explosion", "kind"},
		{"?limit=501", "limit"},
		{"?limit=0", "limit"},
		{"?since=1", "since"},
	} {
		wantError(t, e.get("/api/health"+c.query), 400, c.names)
	}
}

// --- the system page ---

func TestSystemReportsTheStateOfTheInstrument(t *testing.T) {
	e := newEnv(t)
	e.stats = Stats{Chunks: 10, DroppedSamples: 4800, BinsDropped: 1,
		EventsDropped: 2, WriteFailures: 3, LoopRestarts: 4}
	e.status.AudioArrived(t0.Add(-2 * time.Second))
	e.status.BinsCommitted(t0.Add(-3 * time.Second))

	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	e.addEvent(-4*time.Hour, time.Second, 61, detect.Running) // 23:00 yesterday
	e.addReview(id, "verified", "", "me@example.com")
	e.addBins(bin(0, 40, 45, 30), bin(1, 41, 46, 31))
	if err := os.WriteFile(filepath.Join(e.clipDir, "one.wav"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}

	m := e.getJSON("/api/system", "Tailscale-User-Login", "me@example.com",
		"Tailscale-User-Name", "Me")

	if got := num(t, m, "now_ms"); int64(got) != ms(0) {
		t.Errorf("now_ms = %v, want %d", got, ms(0))
	}
	if got := num(t, m, "started_ms"); int64(got) != ms(-time.Hour) {
		t.Errorf("started_ms = %v, want %d", got, ms(-time.Hour))
	}
	if got := num(t, m, "last_audio_ms"); int64(got) != ms(-2*time.Second) {
		t.Errorf("last_audio_ms = %v, want %d", got, ms(-2*time.Second))
	}
	if got := num(t, m, "last_commit_ms"); int64(got) != ms(-3*time.Second) {
		t.Errorf("last_commit_ms = %v, want %d", got, ms(-3*time.Second))
	}
	if got, ok := m["collecting"].(bool); !ok || !got {
		t.Errorf("collecting = %v, want true", m["collecting"])
	}
	if got := num(t, m, "schema_version"); got != 7 {
		t.Errorf("schema_version = %v, want 7", got)
	}
	if got := num(t, m, "db_bytes"); got <= 0 {
		t.Errorf("db_bytes = %v, want the size of the file", got)
	}

	counts := object(t, m, "counts")
	if got := num(t, counts, "events_total"); got != 2 {
		t.Errorf("events_total = %v, want 2", got)
	}
	// Today started at midnight UTC, so only the 03:00 event counts.
	if got := num(t, counts, "events_today"); got != 1 {
		t.Errorf("events_today = %v, want 1", got)
	}
	if got := num(t, counts, "unreviewed"); got != 1 {
		t.Errorf("unreviewed = %v, want 1", got)
	}
	if got := num(t, counts, "samples_today"); got != 2 {
		t.Errorf("samples_today = %v, want 2", got)
	}

	clips := object(t, m, "clips")
	if got := num(t, clips, "count"); got != 1 {
		t.Errorf("clips count = %v, want 1", got)
	}
	if got := num(t, clips, "bytes"); got != 100 {
		t.Errorf("clips bytes = %v, want 100", got)
	}

	cap_ := object(t, m, "capture")
	if got := str(t, cap_, "device"); got != "hw:EM01,0" {
		t.Errorf("device = %q", got)
	}
	if got := str(t, cap_, "gain"); got != "no capture control" {
		t.Errorf("gain = %q", got)
	}
	if got := num(t, cap_, "sensitivity_dbfs"); got != -13 {
		t.Errorf("sensitivity_dbfs = %v, want -13", got)
	}

	pipe := object(t, m, "pipeline")
	for k, want := range map[string]float64{
		"chunks": 10, "dropped_samples": 4800, "bins_dropped": 1,
		"events_dropped": 2, "write_failures": 3, "loop_restarts": 4,
	} {
		if got := num(t, pipe, k); got != want {
			t.Errorf("pipeline %s = %v, want %v", k, got, want)
		}
	}

	hb := object(t, m, "heartbeat")
	if got, ok := hb["configured"].(bool); !ok || got {
		t.Errorf("heartbeat configured = %v, want false", hb["configured"])
	}
	if got := num(t, hb, "interval_ms"); got != 300000 {
		t.Errorf("heartbeat interval_ms = %v, want 300000", got)
	}

	auth := object(t, m, "auth")
	if got := str(t, auth, "mode"); got != "tailscale" {
		t.Errorf("auth mode = %q, want \"tailscale\"", got)
	}
	if got := str(t, auth, "login"); got != "me@example.com" {
		t.Errorf("auth login = %q", got)
	}
	if got := str(t, auth, "name"); got != "Me" {
		t.Errorf("auth name = %q", got)
	}

	disk := list(t, m, "disk")
	if len(disk) != 2 {
		t.Fatalf("disk = %d entries, want one for the database and one for the clips", len(disk))
	}
	for _, d := range disk {
		if num(t, d, "free_mb") <= 0 {
			t.Errorf("free_mb = %v on %v", d["free_mb"], d)
		}
		// A response never says where a file lives.
		for k, v := range d {
			if s, ok := v.(string); ok && contains(s, "/") {
				t.Errorf("disk %q = %q, which names a place on disk", k, s)
			}
		}
	}
}

// The instrument is not collecting when no audio has arrived for 10 seconds.
func TestSystemSaysWhenItIsNotCollecting(t *testing.T) {
	e := newEnv(t)
	e.status.AudioArrived(t0.Add(-time.Minute))
	e.status.BinsCommitted(t0)

	m := e.getJSON("/api/system")
	if got, ok := m["collecting"].(bool); !ok || got {
		t.Errorf("collecting = %v after a minute of silence, want false", m["collecting"])
	}
}

func TestSystemReportsTheHeartbeatWhenItIsConfigured(t *testing.T) {
	e := newEnv(t, func(_ *Config, f *config.Config) {
		f.HeartbeatURL = "https://hc.example.com/ping/abc"
		f.HeartbeatInterval = 2 * time.Minute
	})
	hb := object(t, e.getJSON("/api/system"), "heartbeat")
	if got, ok := hb["configured"].(bool); !ok || !got {
		t.Errorf("configured = %v, want true", hb["configured"])
	}
	if got := num(t, hb, "interval_ms"); got != 120000 {
		t.Errorf("interval_ms = %v, want 120000", got)
	}
	// The ping URL is a secret. It must not be in the response.
	if contains(e.get("/api/system").Body.String(), "hc.example.com") {
		t.Error("the system page gave away the heartbeat URL")
	}
}

// The camera config block says which mode the box is in and what the camera
// actually sends, so the screen can say "this camera sends no audio" rather
// than leaving the owner to wonder (SPEC.md section 15 decision 22).
func TestSystemSaysWhetherTheCameraRecordsAudioAndSendsAny(t *testing.T) {
	e := withCamera(t, func(c *Config, f *config.Config) {
		f.CameraAudio = true
		c.Camera = func() CameraStatus {
			return CameraStatus{
				Enabled: true, Connected: true, SubPath: "Preview_01_sub",
				AudioKnown: true,
				Audio:      video.AudioInfo{Present: true, Codec: "aac", RateHz: 16000},
			}
		}
	})
	cfg := object(t, object(t, decode(t, e.get("/api/system")), "camera"), "config")
	if on, _ := cfg["audio"].(bool); !on {
		t.Error("camera.config.audio = false, but camera_audio is on")
	}
	track := object(t, cfg, "audio_track")
	if known, _ := track["known"].(bool); !known {
		t.Error("audio_track.known = false, but the camera was asked")
	}
	if present, _ := track["present"].(bool); !present {
		t.Error("audio_track.present = false, but the camera sends an aac track")
	}
	if got := str(t, track, "codec"); got != "aac" {
		t.Errorf("audio_track.codec = %q, want aac", got)
	}
	if got := num(t, track, "rate_hz"); got != 16000 {
		t.Errorf("audio_track.rate_hz = %v, want 16000", got)
	}
}

// With camera_audio off nothing asks the camera, so the response must say
// the audio track is not known rather than claim the camera is silent. And
// the setting must read as off.
func TestSystemDoesNotClaimToKnowAboutAudioThatWasNeverAsked(t *testing.T) {
	e := withCamera(t)
	cfg := object(t, object(t, decode(t, e.get("/api/system")), "camera"), "config")
	if on, _ := cfg["audio"].(bool); on {
		t.Error("camera.config.audio = true on a default install")
	}
	track := object(t, cfg, "audio_track")
	if known, _ := track["known"].(bool); known {
		t.Error("audio_track.known = true, but nothing asked the camera")
	}
	if present, _ := track["present"].(bool); present {
		t.Error("audio_track.present = true, but nothing asked the camera")
	}
}

// The System screen says when the daily recording pause is on, so a stretch
// with no events is explained rather than taken for a broken detector. t0 is
// 03:00, so a pause from 03:00 to 04:00 is on and one from 05:00 is not.
func TestSystemSaysWhetherTheRecordingPauseIsOn(t *testing.T) {
	for _, c := range []struct {
		name   string
		pause  config.PauseWindows
		span   string
		active bool
		until  string
	}{
		{"inside the pause", config.PauseWindows{{Start: 3 * time.Hour, End: 4 * time.Hour}}, "03:00-04:00", true, "04:00"},
		{"outside the pause", config.PauseWindows{{Start: 5 * time.Hour, End: 6 * time.Hour}}, "05:00-06:00", false, ""},
		{"no pause", nil, "", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, func(_ *Config, file *config.Config) { file.RecordingPause = c.pause })
			pause := object(t, e.getJSON("/api/system"), "recording_pause")
			if got := str(t, pause, "span"); got != c.span {
				t.Errorf("span = %q, want %q", got, c.span)
			}
			if got, ok := pause["active"].(bool); !ok || got != c.active {
				t.Errorf("active = %v, want %v", pause["active"], c.active)
			}
			if got := str(t, pause, "until"); got != c.until {
				t.Errorf("until = %q, want %q", got, c.until)
			}
		})
	}
}
