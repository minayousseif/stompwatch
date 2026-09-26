package web

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/video"
)

// streamInfo is the stream line a working camera makes ffmpeg print.
const streamInfo = "Input #0, rtsp\n  Stream #0:0: Video: h264 (Main), " +
	"yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn\n"

// withProbe is a camera with the test binary standing in for ffmpeg. works
// is the text a URL must hold for the fake to succeed.
func withProbe(t *testing.T, works string, extra map[string]string,
	change ...func(*Config, *config.Config)) *env {
	t.Helper()
	env := map[string]string{
		"FAKE_PROBE_OK":   works,
		"FAKE_PROBE_INFO": streamInfo,
		"FAKE_STDERR":     "401 Unauthorized",
	}
	for k, v := range extra {
		env[k] = v
	}
	return withCamera(t, append([]func(*Config, *config.Config){func(c *Config, f *config.Config) {
		// No path is configured, so the probe tries the defaults in order.
		f.CameraRTSPPath = ""
		// Nothing listens here, so the camera clock reading is refused at
		// once rather than waiting out its timeout on every test.
		f.CameraHost = "127.0.0.1"
		c.FFmpeg, c.FFmpegEnv = fakeFFmpegCommand(t, env)
	}}, change...)...)
}

// The dashboard's camera test reports each path it tried and why each
// failure failed, then names the one that works.
func TestProbeReportsEachPathAndTheOneThatWorks(t *testing.T) {
	e := withProbe(t, "h264Preview_01_", nil)

	w := e.do(http.MethodPost, "/api/camera/probe", nil)
	if w.Code != 200 {
		t.Fatalf("POST = %d, want 200: %s", w.Code, w.Body.String())
	}
	m := decode(t, w)

	tried := list(t, m, "tried")
	if len(tried) != 3 {
		t.Fatalf("tried %d streams, want two sub paths and one main: %v", len(tried), tried)
	}
	if got := str(t, tried[0], "path"); got != "Preview_01_sub" {
		t.Errorf("tried[0] path = %q", got)
	}
	if ok, _ := tried[0]["ok"].(bool); ok {
		t.Errorf("tried[0] ok = true, want false")
	}
	if got := str(t, tried[0], "detail"); !contains(got, "401 Unauthorized") {
		t.Errorf("tried[0] detail = %q, want ffmpeg's reason", got)
	}
	if got := str(t, tried[1], "path"); got != "h264Preview_01_sub" {
		t.Errorf("tried[1] path = %q", got)
	}
	if ok, _ := tried[1]["ok"].(bool); !ok {
		t.Errorf("tried[1] ok = false, want true")
	}
	if got := str(t, tried[1], "detail"); got != "640x360 at 15 fps (h264), no audio" {
		t.Errorf("tried[1] detail = %q, want the negotiated stream", got)
	}
	if got := str(t, tried[2], "path"); got != "h264Preview_01_main" {
		t.Errorf("tried[2] path = %q, want the main stream", got)
	}

	if got := str(t, m, "sub_path"); got != "h264Preview_01_sub" {
		t.Errorf("sub_path = %q", got)
	}
	if got := str(t, m, "main_path"); got != "h264Preview_01_main" {
		t.Errorf("main_path = %q", got)
	}
	// There is no camera to read a clock from, so it is not readable and
	// the drift is zero rather than a made-up number.
	if readable, _ := m["clock_readable"].(bool); readable {
		t.Errorf("clock_readable = true with no camera answering")
	}
	if got := num(t, m, "clock_drift_ms"); got != 0 {
		t.Errorf("clock_drift_ms = %v, want 0 when the clock is not readable", got)
	}
}

// ffmpeg repeats the URL it was given in its own error output, so a detail
// is the one place a password could reach the browser.
func TestProbeDetailsNeverHoldThePassword(t *testing.T) {
	e := withProbe(t, "", nil)

	w := e.do(http.MethodPost, "/api/camera/probe", nil)
	if w.Code != 200 {
		t.Fatalf("POST = %d: %s", w.Code, w.Body.String())
	}
	noPassword(t, "the probe result", w.Body.String())

	// The fake names the URL it was given, so the masked form must be in
	// the detail: an empty detail would pass the check above by saying
	// nothing at all.
	// Nothing answers, so every known camera's sub path was tried: seven.
	tried := list(t, decode(t, w), "tried")
	if len(tried) != 7 {
		t.Fatalf("tried %d streams, want seven", len(tried))
	}
	detail := str(t, tried[0], "detail")
	if !contains(detail, "rtsp://admin:***@127.0.0.1:554/Preview_01_sub") {
		t.Fatalf("detail does not name the masked stream it tried: %q", detail)
	}
}

// With no path working the clock was never asked, so the result must say
// it was not read rather than report a drift of zero that nobody measured.
func TestProbeSaysTheClockWasNotReadWhenNothingAnswered(t *testing.T) {
	e := withProbe(t, "", nil)

	m := decode(t, e.do(http.MethodPost, "/api/camera/probe", nil))
	if got := str(t, m, "sub_path"); got != "" {
		t.Fatalf("sub_path = %q, want none: this test needs a failing probe", got)
	}
	if readable, _ := m["clock_readable"].(bool); readable {
		t.Error("clock_readable = true, but no stream answered and the clock was never asked")
	}
	if got := num(t, m, "clock_drift_ms"); got != 0 {
		t.Errorf("clock_drift_ms = %v, want 0", got)
	}
}

// The camera test reports. It must not write a setting as a side effect:
// the owner presses save.
func TestProbeChangesNoSetting(t *testing.T) {
	e := withProbe(t, "h264Preview_01_", nil)
	before := e.live.Current()

	if w := e.do(http.MethodPost, "/api/camera/probe", nil); w.Code != 200 {
		t.Fatalf("POST = %d: %s", w.Code, w.Body.String())
	}

	if got := e.live.Current(); !sameConfig(got, before) {
		t.Errorf("the probe changed the settings in force: %q -> %q",
			before.CameraRTSPPath, got.CameraRTSPPath)
	}
	rows, err := e.store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("the probe wrote %v to the config table", rows)
	}
}

// A probe makes the box connect out to the camera, so two at once would be
// two sets of connections and two sets of ffmpeg processes.
func TestOnlyOneProbeRunsAtATime(t *testing.T) {
	e := withProbe(t, "", map[string]string{"FAKE_SLEEP_MS": "800"})

	var wg sync.WaitGroup
	codes := make([]int, 2)
	bodies := make([]string, 2)
	start := make(chan struct{})
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// The second request is sent a moment later, so the two are not
			// racing for which one gets there first.
			if i == 1 {
				time.Sleep(150 * time.Millisecond)
			}
			w := e.do(http.MethodPost, "/api/camera/probe", nil)
			codes[i], bodies[i] = w.Code, w.Body.String()
		}()
	}
	close(start)
	wg.Wait()

	if codes[0] != 200 {
		t.Fatalf("the first probe = %d, want 200: %s", codes[0], bodies[0])
	}
	if codes[1] != 409 {
		t.Fatalf("the second probe = %d, want 409: %s", codes[1], bodies[1])
	}
	if !contains(bodies[1], "already") {
		t.Errorf("the 409 does not say a test is already running: %s", bodies[1])
	}

	// And the lock is released, so the next one runs.
	if w := e.do(http.MethodPost, "/api/camera/probe", nil); w.Code != 200 {
		t.Errorf("the probe after the first finished = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// A probe that runs long answers 504 in a plain sentence rather than
// holding the request open until the whole thing times out.
func TestProbeGivesUpAtItsDeadline(t *testing.T) {
	e := withProbe(t, "", map[string]string{"FAKE_SLEEP_MS": "1000"})
	e.srv.probeDeadline = 150 * time.Millisecond

	w := e.do(http.MethodPost, "/api/camera/probe", nil)
	if w.Code != 504 {
		t.Fatalf("POST = %d, want 504: %s", w.Code, w.Body.String())
	}
	wantError(t, w, 504, "camera")
	noPassword(t, "the deadline message", w.Body.String())
}

// The probe needs a host and a login, and says which is missing.
func TestProbeNeedsAHostAndALogin(t *testing.T) {
	noHost := withProbe(t, "", nil, func(_ *Config, f *config.Config) { f.CameraHost = "" })
	wantError(t, noHost.do(http.MethodPost, "/api/camera/probe", nil), 400, "camera address")

	noLogin := withProbe(t, "", nil, func(c *Config, _ *config.Config) {
		c.CameraLogin = func() video.Login { return video.Login{Source: "none"} }
	})
	w := noLogin.do(http.MethodPost, "/api/camera/probe", nil)
	wantError(t, w, 400, "CAMERA_USER")
}

// Without ffmpeg there is nothing to probe with, and the message says how
// to install it.
func TestProbeSaysWhenFFmpegIsMissing(t *testing.T) {
	e := withCamera(t, func(c *Config, _ *config.Config) {
		c.FFmpeg = video.Command{"no-such-ffmpeg-anywhere"}
		c.FFmpegVersion = ""
	})
	w := e.do(http.MethodPost, "/api/camera/probe", nil)
	wantError(t, w, 400, "apt install ffmpeg")
}

// It is a POST because it makes the box connect out to the camera.
func TestProbeRefusesAGet(t *testing.T) {
	e := withProbe(t, "", nil)
	w := e.get("/api/camera/probe")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Allow"); !strings.Contains(got, "POST") {
		t.Errorf("Allow = %q", got)
	}
}

// audioStreamInfo is what a camera with a microphone makes ffmpeg print.
const audioStreamInfo = "Input #0, rtsp\n  Stream #0:0: Video: h264 (Main), " +
	"yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn\n" +
	"  Stream #0:1: Audio: aac (LC), 16000 Hz, mono, fltp\n"

// camera_audio does nothing on a camera that sends no audio, so the test
// says which kind the camera is (SPEC.md section 15 decision 22).
func TestProbeSaysWhetherTheCameraSendsAudio(t *testing.T) {
	e := withProbe(t, "h264Preview_01_", map[string]string{"FAKE_PROBE_INFO": audioStreamInfo})
	audio := object(t, decode(t, e.do(http.MethodPost, "/api/camera/probe", nil)), "audio")
	if known, _ := audio["known"].(bool); !known {
		t.Error("audio.known = false after a stream answered")
	}
	if present, _ := audio["present"].(bool); !present {
		t.Error("audio.present = false, but the stream carries an aac track")
	}
	if got := str(t, audio, "codec"); got != "aac" {
		t.Errorf("audio.codec = %q, want aac", got)
	}
	if got := num(t, audio, "rate_hz"); got != 16000 {
		t.Errorf("audio.rate_hz = %v, want 16000", got)
	}
}

// A camera with no microphone must be reported as having none, so the owner
// is not left to wonder why camera_audio changed nothing.
func TestProbeSaysWhenTheCameraSendsNoAudio(t *testing.T) {
	e := withProbe(t, "h264Preview_01_", nil) // streamInfo has a video line only
	audio := object(t, decode(t, e.do(http.MethodPost, "/api/camera/probe", nil)), "audio")
	if known, _ := audio["known"].(bool); !known {
		t.Error("audio.known = false after a stream answered")
	}
	if present, _ := audio["present"].(bool); present {
		t.Error("audio.present = true, but the stream has no audio line")
	}
	if got := str(t, audio, "codec"); got != "" {
		t.Errorf("audio.codec = %q, want empty when there is no track", got)
	}
}

// With no path answering, nothing about audio was measured. Reporting
// "no audio" then would be a reading nobody made.
func TestProbeDoesNotClaimToKnowAboutAudioWhenNothingAnswered(t *testing.T) {
	e := withProbe(t, "", nil)
	audio := object(t, decode(t, e.do(http.MethodPost, "/api/camera/probe", nil)), "audio")
	if known, _ := audio["known"].(bool); known {
		t.Error("audio.known = true, but no stream answered")
	}
	if present, _ := audio["present"].(bool); present {
		t.Error("audio.present = true, but no stream answered")
	}
}
