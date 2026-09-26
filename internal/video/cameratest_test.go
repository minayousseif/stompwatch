package video

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These are lines real ffmpeg builds print for a Reolink stream. The camera test
// reads the resolution and the rate from them (SPEC.md section 7).
func TestParseStreamInfoReadsFFmpegOutput(t *testing.T) {
	tests := []struct {
		in    string
		codec string
		w, h  int
		fps   float64
		ok    bool
	}{
		{"  Stream #0:0: Video: h264 (Main), yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn\n", "h264", 640, 360, 15, true},
		{"  Stream #0:1: Video: hevc (Main), yuv420p(tv), 2560x1440 [SAR 1:1 DAR 16:9], 15 fps, 15 tbr, 90k tbn\n", "hevc", 2560, 1440, 15, true},
		{"  Stream #0:0: Video: h264 (High), yuvj420p(pc, bt709, progressive), 1920x1080, 25 tbr, 90k tbn\n", "h264", 1920, 1080, 25, true},
		{"  Stream #0:0: Video: h264, yuv420p, 896x512, 14.99 fps, 15 tbr, 90k tbn\n", "h264", 896, 512, 14.99, true},
		{"  Stream #0:1: Audio: aac (LC), 16000 Hz, mono, fltp\n", "", 0, 0, 0, false},
		{"rtsp://admin:***@cam:554/x: Connection refused\n", "", 0, 0, 0, false},
	}
	for _, tc := range tests {
		info, ok := ParseStreamInfo("junk before\n" + tc.in + "junk after\n")
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if info.Codec != tc.codec || info.Width != tc.w || info.Height != tc.h || info.FPS != tc.fps {
			t.Errorf("%q: got %+v", tc.in, info)
		}
	}
}

// test-camera tries each path in order and reports which works. The
// report is what the owner reads, so the password is masked everywhere in
// it, including the part ffmpeg wrote.
func TestCameraTestTriesEachPathAndMasksThePassword(t *testing.T) {
	cmd, argv, env := fake(t, "test", map[string]string{
		"FAKE_TEST_OK":   "h264Preview_01_sub",
		"FAKE_TEST_INFO": "Input #0, rtsp\n  Stream #0:0: Video: h264 (Main), yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn\n",
		"FAKE_STDERR":    "401 Unauthorized",
	})
	base := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: testPass}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	rep := TryPaths(ctx, cmd, env, base, CandidatePaths(""))
	if rep.Working == nil {
		t.Fatalf("no working stream found:\n%s", rep)
	}
	if rep.Working.Stream.Path != "h264Preview_01_sub" {
		t.Errorf("working path = %q", rep.Working.Stream.Path)
	}
	if rep.Working.Info.Width != 640 || rep.Working.Info.Height != 360 || rep.Working.Info.FPS != 15 {
		t.Errorf("info = %+v", rep.Working.Info)
	}
	if len(rep.Tried) != 2 || rep.Tried[0].Err == nil || rep.Tried[1].Err != nil {
		t.Fatalf("tried = %+v", rep.Tried)
	}
	text := rep.String()
	for _, want := range []string{"rtsp://admin:***@cam.local:554/Preview_01_sub", "401 Unauthorized",
		"rtsp://admin:***@cam.local:554/h264Preview_01_sub", "640x360", "15"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, testPass) || strings.Contains(text, escapePassword(testPass)) {
		t.Fatalf("the report holds the password:\n%s", text)
	}
	if e := rep.Tried[0].Err.Error(); strings.Contains(e, testPass) || strings.Contains(e, escapePassword(testPass)) {
		t.Fatalf("the error holds the password: %s", e)
	}

	runs := readArgv(t, argv)
	if len(runs) != 2 {
		t.Fatalf("ffmpeg ran %d times, want 2", len(runs))
	}
	for _, run := range runs {
		if !hasPair(run, "-rtsp_transport", "tcp") || !contains(run, "-an") {
			t.Errorf("the camera test ran ffmpeg without tcp or -an: %v", run)
		}
	}
}

func TestCameraTestReportsWhenNothingWorks(t *testing.T) {
	cmd, _, env := fake(t, "test", map[string]string{"FAKE_STDERR": "Connection timed out"})
	base := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: testPass}}
	rep := TryPaths(context.Background(), cmd, env, base, []string{"a", "b"})
	if rep.Working != nil {
		t.Fatal("a stream was reported working")
	}
	if len(rep.Tried) != 2 {
		t.Fatalf("tried %d", len(rep.Tried))
	}
	if !strings.Contains(rep.String(), "Connection timed out") {
		t.Errorf("report does not carry ffmpeg's reason:\n%s", rep)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// Many cameras send no audio track, and then camera_audio does nothing. The
// camera test has to say which kind the camera is, so the owner is not left to
// wonder (SPEC.md section 15 decision 22). These are lines real ffmpeg
// builds print for the input, which it lists whether or not -an is given.
func TestParseStreamInfoReadsTheAudioTrack(t *testing.T) {
	const videoLine = "  Stream #0:0: Video: h264 (Main), yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn\n"
	tests := []struct {
		name    string
		in      string
		present bool
		codec   string
		rate    int
	}{
		{"aac after the video", videoLine + "  Stream #0:1: Audio: aac (LC), 16000 Hz, mono, fltp\n",
			true, "aac", 16000},
		{"g711 before the video", "  Stream #0:0: Audio: pcm_alaw, 8000 Hz, mono, s16, 64 kb/s\n" + videoLine,
			true, "pcm_alaw", 8000},
		{"no audio at all", videoLine, false, "", 0},
	}
	for _, tc := range tests {
		info, ok := ParseStreamInfo("Input #0, rtsp, from 'rtsp://cam':\n" + tc.in)
		if !ok {
			t.Errorf("%s: no video stream was read", tc.name)
			continue
		}
		if info.Audio.Present != tc.present || info.Audio.Codec != tc.codec || info.Audio.RateHz != tc.rate {
			t.Errorf("%s: audio = %+v, want present %v, codec %q, %d Hz",
				tc.name, info.Audio, tc.present, tc.codec, tc.rate)
		}
		// The video reading must not change because audio is now read too.
		if info.Codec != "h264" || info.Width != 640 || info.Height != 360 || info.FPS != 15 {
			t.Errorf("%s: video = %+v", tc.name, info)
		}
	}
}

// The report is what the owner reads, on the command line and in the log.
// It must name the audio track, or say there is none, in words.
func TestStreamInfoSaysWhetherThereIsAudio(t *testing.T) {
	with, ok := ParseStreamInfo("  Stream #0:0: Video: h264, yuv420p, 640x360, 15 fps\n" +
		"  Stream #0:1: Audio: aac (LC), 16000 Hz, mono, fltp\n")
	if !ok {
		t.Fatal("no video stream was read")
	}
	for _, want := range []string{"aac", "16000"} {
		if !strings.Contains(with.String(), want) {
			t.Errorf("%q does not mention %q", with.String(), want)
		}
	}
	without, ok := ParseStreamInfo("  Stream #0:0: Video: h264, yuv420p, 640x360, 15 fps\n")
	if !ok {
		t.Fatal("no video stream was read")
	}
	if !strings.Contains(without.String(), "no audio") {
		t.Errorf("%q does not say there is no audio", without.String())
	}
}
