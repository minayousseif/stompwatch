package video

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The dashboard says whether a login is loaded and where it came from. It
// must never say what the password is, so DescribeLogin reports the source
// and the user and keeps the password inside Credentials, which cannot be
// printed.
func TestDescribeLoginSaysWhereTheLoginCameFrom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camera.env")
	if err := os.WriteFile(path, []byte("CAMERA_USER=fromfile\nCAMERA_PASS="+testPass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		env        map[string]string
		file       string
		wantSource string
		wantUser   string
		wantLoaded bool
	}{
		{"from the environment", map[string]string{"CAMERA_USER": "fromenv", "CAMERA_PASS": testPass},
			"", "environment", "fromenv", true},
		{"the environment wins over the file", map[string]string{"CAMERA_USER": "fromenv", "CAMERA_PASS": testPass},
			path, "environment", "fromenv", true},
		{"from the file", nil, path, "file", "fromfile", true},
		{"nowhere", nil, "", "none", "", false},
		{"a file that is not there", nil, filepath.Join(t.TempDir(), "missing"), "none", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeLogin(envOf(tc.env), tc.file)
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.Loaded() != tc.wantLoaded {
				t.Errorf("Loaded() = %v, want %v", got.Loaded(), tc.wantLoaded)
			}
			if got.Creds.User != tc.wantUser {
				t.Errorf("Creds.User = %q, want %q", got.Creds.User, tc.wantUser)
			}
			if tc.wantLoaded && got.Creds.Pass == "" {
				t.Error("no password was read, so no stream can be opened")
			}
			if got.Err != nil && tc.wantLoaded {
				t.Errorf("Err = %v with a login loaded", got.Err)
			}
			// Whatever went wrong, the reason must not repeat the password.
			if got.Err != nil && strings.Contains(got.Err.Error(), testPass) {
				t.Errorf("Err holds the password: %v", got.Err)
			}
		})
	}
}

// A file that cannot be read is "none" with a reason the owner can act on.
func TestDescribeLoginReportsWhyAFileFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camera.env")
	if err := os.WriteFile(path, []byte("CAMERA_USER=admin\nCAMERA_PASS=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DescribeLogin(envOf(nil), path)
	if got.Loaded() {
		t.Fatal("a world-readable file was accepted")
	}
	if got.Err == nil || !strings.Contains(got.Err.Error(), "0600") {
		t.Errorf("Err = %v, want the mode rule", got.Err)
	}
}

// The dashboard says which ffmpeg is installed, or that none is.
func TestVersionReadsTheFirstLineFFmpegPrints(t *testing.T) {
	cmd, _, env := fake(t, "version", map[string]string{
		"FAKE_STDOUT": "ffmpeg version 7.1.1-1 Copyright (c) 2000-2025 the FFmpeg developers\n" +
			"built with gcc 14 (Debian 14.2.0-19)\n",
	})
	got := Version(cmd, env)
	if got != "ffmpeg version 7.1.1-1 Copyright (c) 2000-2025 the FFmpeg developers" {
		t.Errorf("Version = %q", got)
	}
}

func TestVersionIsEmptyWhenFFmpegIsNotInstalled(t *testing.T) {
	if got := Version(Command{filepath.Join(t.TempDir(), "no-such-ffmpeg")}, nil); got != "" {
		t.Errorf("Version = %q, want the empty string", got)
	}
}

// ProbeCamera is the one implementation behind probe-camera and the
// dashboard's test button. It tries the sub paths in order, then the main
// stream derived from the one that worked.
func TestProbeCameraTriesTheSubPathsThenTheMainStream(t *testing.T) {
	cmd, argv, env := fake(t, "probe", map[string]string{
		"FAKE_PROBE_OK":   "Preview_01_",
		"FAKE_PROBE_INFO": "Input #0, rtsp\n  Stream #0:0: Video: h264 (Main), yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn\n",
		"FAKE_STDERR":     "401 Unauthorized",
	})
	base := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: testPass}}

	rep := ProbeCamera(context.Background(), cmd, env, base, CandidatePaths(""), "")
	if rep.Sub.Working == nil {
		t.Fatalf("no sub path worked:\n%s", rep.Sub)
	}
	if got := rep.Sub.Working.Stream.Path; got != "Preview_01_sub" {
		t.Errorf("sub path = %q, want \"Preview_01_sub\"", got)
	}
	if rep.MainPath != "Preview_01_main" {
		t.Errorf("MainPath = %q, want \"Preview_01_main\"", rep.MainPath)
	}
	if rep.Main.Working == nil {
		t.Fatalf("the main stream did not work:\n%s", rep.Main)
	}
	runs := readArgv(t, argv)
	if len(runs) != 2 {
		t.Fatalf("ffmpeg ran %d times, want one sub and one main", len(runs))
	}
}

// A configured main path is tried as it stands, not derived.
func TestProbeCameraUsesTheConfiguredMainPath(t *testing.T) {
	cmd, _, env := fake(t, "probe", map[string]string{
		"FAKE_PROBE_OK":   "Preview",
		"FAKE_PROBE_INFO": "  Stream #0:0: Video: h264, yuv420p, 640x360, 15 fps, 15 tbr, 90k tbn\n",
	})
	base := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: testPass}}

	rep := ProbeCamera(context.Background(), cmd, env, base, []string{"Preview_01_sub"}, "Preview_02_main")
	if rep.MainPath != "Preview_02_main" {
		t.Errorf("MainPath = %q, want the configured \"Preview_02_main\"", rep.MainPath)
	}
}

// With no sub path working there is nothing to derive a main path from, so
// the main stream is not tried at all.
func TestProbeCameraSkipsTheMainStreamWhenNoSubPathWorks(t *testing.T) {
	cmd, argv, env := fake(t, "probe", map[string]string{"FAKE_STDERR": "Connection timed out"})
	base := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: testPass}}

	rep := ProbeCamera(context.Background(), cmd, env, base, CandidatePaths(""), "")
	if rep.Sub.Working != nil {
		t.Fatal("a sub path was reported working")
	}
	if rep.MainPath != "" || len(rep.Main.Tried) != 0 {
		t.Errorf("the main stream was tried: %q %+v", rep.MainPath, rep.Main.Tried)
	}
	// Seven known cameras, so seven sub paths are tried and none of them
	// leads to a main stream.
	if got := len(readArgv(t, argv)); got != 7 {
		t.Errorf("ffmpeg ran %d times, want one per sub path", got)
	}
}

// Nothing in the report may hold the password, in either the plain or the
// escaped form, whatever ffmpeg printed.
func TestProbeCameraNeverReportsThePassword(t *testing.T) {
	cmd, _, env := fake(t, "probe", map[string]string{"FAKE_STDERR": "401 Unauthorized"})
	base := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: testPass}}

	rep := ProbeCamera(context.Background(), cmd, env, base, CandidatePaths(""), "")
	text := rep.Sub.String() + rep.Main.String()
	for _, a := range rep.Sub.Tried {
		if a.Err != nil {
			text += a.Err.Error()
		}
	}
	if strings.Contains(text, testPass) || strings.Contains(text, escapePassword(testPass)) {
		t.Fatalf("the report holds the password:\n%s", text)
	}
	// The fake repeats the URL it was given, so the masked form must be
	// there: an empty report would pass the check above by saying nothing.
	if !strings.Contains(text, "rtsp://admin:***@cam.local:554/Preview_01_sub") {
		t.Fatalf("the report does not name the stream it tried:\n%s", text)
	}
}

// A deadline stops the probe. The caller needs to be able to tell that it
// ran out of time rather than that the camera refused.
func TestProbeCameraStopsAtItsDeadline(t *testing.T) {
	cmd, _, env := fake(t, "probe", map[string]string{"FAKE_STDERR": "401 Unauthorized"})
	base := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: testPass}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	rep := ProbeCamera(ctx, cmd, env, base, CandidatePaths(""), "")
	if rep.Sub.Working != nil {
		t.Error("a stream worked after the deadline passed")
	}
	if ctx.Err() == nil {
		t.Error("the context did not expire, so this test proves nothing")
	}
}
