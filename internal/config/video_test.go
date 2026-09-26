package config

import (
	"strings"
	"testing"
)

// An install without a camera must behave exactly as it does today, so
// every video key defaults to "off" or to a value that is harmless while
// camera_host is empty (SPEC.md section 7).
func TestParseLeavesVideoOffByDefault(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"camera_host", c.CameraHost, ""},
		{"camera_port", c.CameraPort, 554},
		{"camera_rtsp_path", c.CameraRTSPPath, ""},
		{"camera_rtsp_path_main", c.CameraRTSPPathMain, ""},
		{"camera_credentials_file", c.CameraCredentialsFile, ""},
		{"video_dir", c.VideoDir, "/data/clips/video"},
		{"video_ring_minutes", c.VideoRingMinutes, 10},
		{"video_main_on_event", c.VideoMainOnEvent, false},
		{"video_segment_seconds", c.VideoSegmentSeconds, 10},
		{"ntp_server", c.NTPServer, "pool.ntp.org"},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.name, ch.got, ch.want)
		}
	}
	if c.VideoEnabled() {
		t.Error("VideoEnabled() is true with no camera_host")
	}
}

func TestParseReadsVideoKeys(t *testing.T) {
	in := minimal + `camera_host = 192.168.1.20
camera_port = 8554
camera_rtsp_path = Preview_01_sub
camera_rtsp_path_main = Preview_01_main
camera_credentials_file = /etc/stompwatch/camera.env
video_dir = /mnt/video
video_ring_minutes = 30
video_main_on_event = true
video_segment_seconds = 5
ntp_server = time.example.org
`
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.CameraHost != "192.168.1.20" || c.CameraPort != 8554 || c.CameraRTSPPath != "Preview_01_sub" ||
		c.CameraRTSPPathMain != "Preview_01_main" || c.CameraCredentialsFile != "/etc/stompwatch/camera.env" ||
		c.VideoDir != "/mnt/video" || c.VideoRingMinutes != 30 || !c.VideoMainOnEvent ||
		c.VideoSegmentSeconds != 5 || c.NTPServer != "time.example.org" {
		t.Fatalf("parsed %+v", c)
	}
	if !c.VideoEnabled() {
		t.Error("VideoEnabled() is false with a camera_host")
	}
}

// A bad video value must be refused with the key named, like every other
// key. A bool accepts only true or false, so "yes" cannot be read as false.
func TestParseRejectsBadVideoValues(t *testing.T) {
	tests := []struct {
		name, line, want string
	}{
		{"ring too short", "video_ring_minutes = 0", "video_ring_minutes"},
		{"ring too long", "video_ring_minutes = 100000", "video_ring_minutes"},
		{"segment too short", "video_segment_seconds = 0", "video_segment_seconds"},
		{"segment too long", "video_segment_seconds = 120", "video_segment_seconds"},
		{"bad bool", "video_main_on_event = yes", "video_main_on_event"},
		{"bad port", "camera_port = 70000", "camera_port"},
		{"empty video dir", "camera_host = cam\nvideo_dir =", "video_dir"},
		{"host with scheme", "camera_host = rtsp://cam", "camera_host"},
		{"host with credentials", "camera_host = admin:secret@cam", "camera_host"},
	}
	for _, tc := range tests {
		_, err := Parse(strings.NewReader(minimal + tc.line + "\n"))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not name %s", tc.name, err, tc.want)
		}
	}
}

// The dashboard may write camera_host and the two RTSP paths, so what they
// accept is the only thing between a web form and a stream URL of somebody
// else's choosing. A host is a name or an address. A path is what ffmpeg
// can use: segments separated by /, with an optional query, and nothing
// that could rewrite the URL the builder makes.
func TestValidateRejectsACameraHostOrPathThatIsNotOne(t *testing.T) {
	tests := []struct {
		name, line, want string
	}{
		{"host with a space", "camera_host = my camera", "camera_host"},
		{"host with a tab", "camera_host = cam\tlocal", "camera_host"},
		{"host with a newline escape", "camera_host = cam\rlocal", "camera_host"},
		{"host with a slash", "camera_host = cam/../etc", "camera_host"},
		{"host with a query", "camera_host = cam?x=1", "camera_host"},
		{"sub path with a scheme", "camera_rtsp_path = rtsp://cam/x", "camera_rtsp_path"},
		{"sub path with a bare scheme mark", "camera_rtsp_path = a://b", "camera_rtsp_path"},
		{"sub path with a space", "camera_rtsp_path = Preview 01", "camera_rtsp_path"},
		{"sub path with a tab", "camera_rtsp_path = a\tb", "camera_rtsp_path"},
		{"sub path going up", "camera_rtsp_path = ..", "camera_rtsp_path"},
		{"sub path going up in the middle", "camera_rtsp_path = cam/../etc", "camera_rtsp_path"},
		{"sub path with a backslash", "camera_rtsp_path = cam\\realmonitor", "camera_rtsp_path"},
		{"sub path with a fragment", "camera_rtsp_path = cam/realmonitor#1", "camera_rtsp_path"},
		{"sub path with an empty segment", "camera_rtsp_path = a//b", "camera_rtsp_path"},
		{"sub path with a leading slash", "camera_rtsp_path = /cam/realmonitor", "camera_rtsp_path"},
		{"sub path with a login", "camera_rtsp_path = admin:pass@x", "camera_rtsp_path"},
		{"sub path with an at sign in the query", "camera_rtsp_path = cam?user=a@b", "camera_rtsp_path"},
		{"main path with a scheme", "camera_rtsp_path_main = rtsp://cam/x", "camera_rtsp_path_main"},
		{"main path with a space", "camera_rtsp_path_main = a b", "camera_rtsp_path_main"},
		{"main path with an at sign", "camera_rtsp_path_main = a@b", "camera_rtsp_path_main"},
		{"main path going up", "camera_rtsp_path_main = ../a", "camera_rtsp_path_main"},
	}
	for _, tc := range tests {
		_, err := Parse(strings.NewReader(minimal + tc.line + "\n"))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not name %s", tc.name, err, tc.want)
		}
	}
}

// Each rejected shape must be named in the message, so the owner reads what
// is wrong with the path rather than a list of every rule.
func TestValidateSaysWhatIsWrongWithAPath(t *testing.T) {
	tests := []struct {
		line, want string
	}{
		{"camera_rtsp_path = rtsp://cam/x", "scheme"},
		{"camera_rtsp_path = Preview 01", "space"},
		{"camera_rtsp_path = cam/../etc", ".."},
		{"camera_rtsp_path = cam\\realmonitor", "backslash"},
		{"camera_rtsp_path = cam#1", "#"},
		{"camera_rtsp_path = a//b", "empty"},
		{"camera_rtsp_path = admin:pass@x", "@"},
	}
	for _, tc := range tests {
		_, err := Parse(strings.NewReader(minimal + tc.line + "\n"))
		if err == nil {
			t.Errorf("%q: accepted", tc.line)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %q does not say %q is the problem", tc.line, err, tc.want)
		}
	}
}

// The shapes a real camera uses must all be accepted, the owner's Amcrest
// included. Before this list, an Amcrest path could not be set at all, by
// the dashboard or by hand.
func TestValidateAcceptsRealCameraHostsAndPaths(t *testing.T) {
	lines := []string{
		"camera_host = 192.168.1.40",
		"camera_host = 192.0.2.47",
		"camera_host = cam.local",
		"camera_host = camera-1.home.arpa",
		"camera_rtsp_path = cam/realmonitor?channel=1&subtype=1",
		"camera_rtsp_path_main = cam/realmonitor?channel=1&subtype=0",
		"camera_rtsp_path = Preview_01_sub",
		"camera_rtsp_path = h264Preview_01_sub",
		"camera_rtsp_path_main = Preview_01_main",
		"camera_rtsp_path = Streaming/Channels/102",
		"camera_rtsp_path_main = Streaming/Channels/101",
		"camera_rtsp_path = stream2",
		"camera_rtsp_path = videoSub",
		"camera_rtsp_path = s1",
		"camera_rtsp_path = live/ch0_0.264",
		"camera_rtsp_path =",
		"camera_rtsp_path_main =",
	}
	for _, line := range lines {
		if _, err := Parse(strings.NewReader(minimal + line + "\n")); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
}

// A password in the config file is the mistake SPEC.md section 3 forbids. The
// parser has no key for one, so the line is refused as unknown, and the
// value must not be echoed back in the error.
func TestParseHasNoKeyForACameraPassword(t *testing.T) {
	for _, key := range []string{"camera_pass", "camera_password", "camera_user"} {
		_, err := Parse(strings.NewReader(minimal + key + " = hunter2\n"))
		if err == nil {
			t.Errorf("%s was accepted", key)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the error %q repeats the value", key, err)
		}
	}
}
