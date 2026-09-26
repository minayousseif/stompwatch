package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/video"
)

// camPass is the camera password the tests configure. It holds the
// characters an RTSP URL escapes, so both the plain and the escaped form
// can be looked for.
const camPass = "p@ss:w/rd#1"

// camPassEscaped is the same password as a URL carries it. It is written out
// rather than computed, so the check cannot pass by agreeing with the code
// it is checking.
const camPassEscaped = "p%40ss:w%2Frd%231"

// withCamera configures a camera with a real password, loaded from a 0600
// file. change may adjust the config further.
func withCamera(t *testing.T, change ...func(*Config, *config.Config)) *env {
	t.Helper()
	return newEnv(t, append([]func(*Config, *config.Config){func(c *Config, f *config.Config) {
		f.CameraHost = "192.168.1.40"
		f.CameraRTSPPath = "Preview_01_sub"
		f.VideoDir = filepath.Join(t.TempDir(), "video")
		c.VideoDir = f.VideoDir
		c.Camera = func() CameraStatus {
			return CameraStatus{Enabled: true, Connected: true, SubPath: "Preview_01_sub"}
		}
		c.CameraLogin = func() video.Login {
			return video.Login{Source: "file", Creds: video.Credentials{User: "admin", Pass: camPass}}
		}
		c.FFmpegVersion = "ffmpeg version 7.1.1-1 Copyright (c) 2000-2025 the FFmpeg developers"
	}}, change...)...)
}

// noPassword fails the test if either form of the password is in text.
func noPassword(t *testing.T, what, text string) {
	t.Helper()
	if contains(text, camPass) || contains(text, camPassEscaped) || contains(text, "p%40ss") {
		t.Fatalf("%s holds the camera password:\n%s", what, text)
	}
}

// The line that must never be crossed: with a real password configured, no
// response of the web interface carries it, in any form, anywhere.
func TestThePasswordNeverReachesTheBrowser(t *testing.T) {
	e := withCamera(t)

	for _, target := range []string{"/api/system", "/api/settings"} {
		w := e.get(target)
		if w.Code != 200 {
			t.Fatalf("GET %s = %d: %s", target, w.Code, w.Body.String())
		}
		noPassword(t, "GET "+target, w.Body.String())
	}

	// The camera test repeats what ffmpeg said, so both of its outcomes are
	// checked: the one where every path fails and ffmpeg echoes the URL it
	// was given, and the one where a path works.
	for _, works := range []string{"", "Preview"} {
		p := withCameraTest(t, works, nil)
		w := p.do(http.MethodPost, "/api/camera/test", nil)
		if w.Code != 200 {
			t.Fatalf("POST /api/camera/test = %d: %s", w.Code, w.Body.String())
		}
		noPassword(t, "POST /api/camera/test", w.Body.String())
	}

	// The credentials object must carry no password field at all: not
	// empty, not masked, not present.
	creds := object(t, object(t, object(t, e.getJSON("/api/system"), "camera"), "config"), "credentials")
	for _, key := range []string{"pass", "password", "secret", "creds", "credentials", "url"} {
		if v, present := creds[key]; present {
			t.Errorf("credentials carries a %q field (%v)", key, v)
		}
	}
	if got := len(creds); got != 3 {
		t.Errorf("credentials has %d fields (%v), want loaded, source, and user", got, creds)
	}
}

func TestSystemReportsHowTheCameraIsConfigured(t *testing.T) {
	e := withCamera(t)
	cfg := object(t, object(t, e.getJSON("/api/system"), "camera"), "config")

	if got := str(t, cfg, "host"); got != "192.168.1.40" {
		t.Errorf("host = %q", got)
	}
	if got := num(t, cfg, "port"); got != 554 {
		t.Errorf("port = %v, want 554", got)
	}
	if got := str(t, cfg, "sub_path"); got != "Preview_01_sub" {
		t.Errorf("sub_path = %q", got)
	}
	if got := str(t, cfg, "sub_path_source"); got != "configured" {
		t.Errorf("sub_path_source = %q, want \"configured\"", got)
	}
	// camera_rtsp_path_main is empty, so the main path is the sub path with
	// _main for _sub.
	if got := str(t, cfg, "main_path"); got != "Preview_01_main" {
		t.Errorf("main_path = %q, want \"Preview_01_main\"", got)
	}
	if got := num(t, cfg, "ring_minutes"); got != 10 {
		t.Errorf("ring_minutes = %v, want 10", got)
	}
	if got := num(t, cfg, "segment_seconds"); got != 10 {
		t.Errorf("segment_seconds = %v, want 10", got)
	}
	if got := str(t, cfg, "ffmpeg"); got != "ffmpeg version 7.1.1-1 Copyright (c) 2000-2025 the FFmpeg developers" {
		t.Errorf("ffmpeg = %q", got)
	}

	creds := object(t, cfg, "credentials")
	if got, ok := creds["loaded"].(bool); !ok || !got {
		t.Errorf("credentials loaded = %v, want true", creds["loaded"])
	}
	if got := str(t, creds, "source"); got != "file" {
		t.Errorf("credentials source = %q, want \"file\"", got)
	}
	if got := str(t, creds, "user"); got != "admin" {
		t.Errorf("credentials user = %q, want \"admin\"", got)
	}
}

// A path the collector found by trying the defaults is re-tried on every
// reconnect, so the owner has to be able to tell it from a configured one.
func TestSystemSaysWhenTheSubPathWasDiscovered(t *testing.T) {
	e := withCamera(t, func(c *Config, f *config.Config) {
		f.CameraRTSPPath = ""
		c.Camera = func() CameraStatus {
			return CameraStatus{Enabled: true, Connected: true, SubPath: "h264Preview_01_sub"}
		}
	})
	cfg := object(t, object(t, e.getJSON("/api/system"), "camera"), "config")
	if got := str(t, cfg, "sub_path_source"); got != "discovered" {
		t.Errorf("sub_path_source = %q, want \"discovered\"", got)
	}
	if got := str(t, cfg, "sub_path"); got != "h264Preview_01_sub" {
		t.Errorf("sub_path = %q, want the path the ring is using", got)
	}
	if got := str(t, cfg, "main_path"); got != "h264Preview_01_main" {
		t.Errorf("main_path = %q", got)
	}
}

// The owner reads this section while bringing a camera up, so it answers
// with no camera configured at all.
func TestSystemReportsTheCameraConfigurationWithNoCamera(t *testing.T) {
	e := newEnv(t, func(c *Config, _ *config.Config) {
		c.CameraLogin = func() video.Login {
			return video.Login{Source: "none", Err: errNoLoginForTest}
		}
	})
	camera := object(t, e.getJSON("/api/system"), "camera")
	if got, ok := camera["enabled"].(bool); !ok || got {
		t.Errorf("enabled = %v, want false", camera["enabled"])
	}
	cfg := object(t, camera, "config")
	// Nothing is configured and nothing has been found, so neither word
	// applies and the response must not claim one of them.
	if got := str(t, cfg, "sub_path_source"); got != "none" {
		t.Errorf("sub_path_source = %q, want \"none\" when no path is in force", got)
	}
	if got := str(t, cfg, "sub_path"); got != "" {
		t.Errorf("sub_path = %q, want the empty string", got)
	}
	if got := str(t, cfg, "host"); got != "" {
		t.Errorf("host = %q, want the empty string", got)
	}
	if got := str(t, cfg, "ffmpeg"); got != "" {
		t.Errorf("ffmpeg = %q, want the empty string when it is not installed", got)
	}
	creds := object(t, cfg, "credentials")
	if got, ok := creds["loaded"].(bool); !ok || got {
		t.Errorf("credentials loaded = %v, want false", creds["loaded"])
	}
	if got := str(t, creds, "source"); got != "none" {
		t.Errorf("credentials source = %q, want \"none\"", got)
	}
	if got := str(t, creds, "user"); got != "" {
		t.Errorf("credentials user = %q, want the empty string", got)
	}
}

// A credentials file that cannot be read must read as no login, not as a
// login with an empty user.
func TestSystemSaysNoLoginWhenTheFileIsUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camera.env")
	if err := os.WriteFile(path, []byte("CAMERA_USER=admin\nCAMERA_PASS=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, func(c *Config, f *config.Config) {
		f.CameraCredentialsFile = path
		c.CameraLogin = func() video.Login {
			return video.DescribeLogin(func(string) string { return "" }, path)
		}
	})
	creds := object(t, object(t, object(t, e.getJSON("/api/system"), "camera"), "config"), "credentials")
	if got, ok := creds["loaded"].(bool); !ok || got {
		t.Errorf("loaded = %v, want false", creds["loaded"])
	}
	if got := str(t, creds, "source"); got != "none" {
		t.Errorf("source = %q, want \"none\"", got)
	}
	// A response never names a file on disk, not even the one it failed on.
	body := e.get("/api/system").Body.String()
	if contains(body, path) {
		t.Errorf("the response names the credentials file:\n%s", body)
	}
}

// errNoLoginForTest stands in for the loader's own error.
var errNoLoginForTest = errors.New("no camera login")
