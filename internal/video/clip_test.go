package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRing is a segment list and an outage list, the two things a Writer
// reads from the ring.
type fakeRing struct {
	dir     string
	outages []Outage
}

func (f fakeRing) Segments() ([]Segment, error) { return ListSegments(f.dir) }
func (f fakeRing) Outages() []Outage            { return f.outages }
func (f fakeRing) SegmentLength() time.Duration { return 10 * time.Second }

func newWriter(t *testing.T, ring Source, env map[string]string) (*Writer, string, string) {
	t.Helper()
	cmd, argv, extra := fake(t, "concat", env)
	dir := filepath.Join(t.TempDir(), "video")
	w, err := NewWriter(WriterConfig{Dir: dir, Source: ring, Command: cmd, Env: extra})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w, dir, argv
}

// The clip of an event is the copied segments that span it, written once,
// hashed, and never replaced (SPEC.md section 7 and the clip recorder's rules).
func TestSaveWritesTheSpanningSegmentsOnce(t *testing.T) {
	ringDir := t.TempDir()
	writeSegments(t, ringDir, 10*time.Second, 6) // 03:00:00 to 03:00:50
	w, dir, argv := newWriter(t, fakeRing{dir: ringDir}, map[string]string{"FAKE_OUT_TIME_US": "30040000"})

	from, to := s0.Add(15*time.Second), s0.Add(42*time.Second)
	c, err := w.Save(context.Background(), 7, from, to)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	wantPath := filepath.Join(dir, "2026", "09", "12", "7.mp4")
	if c.Path != wantPath {
		t.Errorf("path %q, want %q", c.Path, wantPath)
	}
	// Segments 1 to 4 (03:00:10 to 03:00:49) span the window. The fake joins
	// their bytes, which writeSegments made "x", "xx", ... so the clip is
	// exactly these 14 bytes.
	want := []byte("xx" + "xxx" + "xxxx" + "xxxxx")
	got, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("clip holds %q, want %q", got, want)
	}
	sum := sha256.Sum256(want)
	if c.SHA256 != hex.EncodeToString(sum[:]) || c.Bytes != 14 {
		t.Errorf("hash %s, bytes %d", c.SHA256, c.Bytes)
	}
	if !c.Start.Equal(s0.Add(10 * time.Second)) {
		t.Errorf("start %v, want the first segment's start", c.Start)
	}
	if c.Duration.Milliseconds() != 30040 {
		t.Errorf("duration %v, want what ffmpeg reported", c.Duration)
	}
	if c.Truncated {
		t.Error("a fully covered window is marked truncated")
	}
	info, err := os.Stat(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Errorf("mode %04o, want 0400", info.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(c.Path), ".*")); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}

	// Never replaced.
	if _, err := w.Save(context.Background(), 7, from, to); !errors.Is(err, ErrExists) {
		t.Errorf("second Save gave %v, want ErrExists", err)
	}

	runs := readArgv(t, argv)
	if len(runs) != 1 {
		t.Fatalf("ffmpeg ran %d times", len(runs))
	}
	run := runs[0]
	if !contains(run, "-an") {
		t.Fatalf("the concat ran without -an: %v", run)
	}
	if !hasPair(run, "-c", "copy") || !hasPair(run, "-f", "concat") {
		t.Errorf("the concat ran without -c copy or -f concat: %v", run)
	}
	if a := reencodesVideo(run); a != "" {
		t.Errorf("the concat ran with %q, which re-encodes or trims: %v", a, run)
	}
}

// The list handed to ffmpeg names exactly the spanning segments, in order.
func TestSaveListsTheSegmentsInOrder(t *testing.T) {
	ringDir := t.TempDir()
	names := writeSegments(t, ringDir, 10*time.Second, 6)
	var seen string
	w, _, _ := newWriter(t, fakeRing{dir: ringDir}, map[string]string{"FAKE_OUT_TIME_US": "20000000"})
	w.beforeRun = func(list string) { b, _ := os.ReadFile(list); seen = string(b) }
	if _, err := w.Save(context.Background(), 1, s0.Add(25*time.Second), s0.Add(35*time.Second)); err != nil {
		t.Fatal(err)
	}
	want := "file '" + filepath.Join(ringDir, names[2]) + "'\nfile '" + filepath.Join(ringDir, names[3]) + "'\n"
	if seen != want {
		t.Errorf("list:\n%s\nwant:\n%s", seen, want)
	}
}

func TestSaveSaysWhenThereIsNoVideo(t *testing.T) {
	ringDir := t.TempDir()
	writeSegments(t, ringDir, 10*time.Second, 3)
	w, dir, _ := newWriter(t, fakeRing{dir: ringDir}, nil)
	_, err := w.Save(context.Background(), 2, s0.Add(5*time.Minute), s0.Add(6*time.Minute))
	if !errors.Is(err, ErrNoVideo) {
		t.Fatalf("Save gave %v, want ErrNoVideo", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("something was written for an event with no video: %v", entries)
	}
}

// A window the ring does not fully hold, or one an outage cuts through,
// gives a clip that says so. Silence about a hole is the failure SPEC.md
// section 3.4 forbids.
func TestSaveMarksAnUncoveredWindowTruncated(t *testing.T) {
	ringDir := t.TempDir()
	writeSegments(t, ringDir, 10*time.Second, 6)
	env := map[string]string{"FAKE_OUT_TIME_US": "10000000"}

	w, _, _ := newWriter(t, fakeRing{dir: ringDir}, env)
	c, err := w.Save(context.Background(), 3, s0.Add(-20*time.Second), s0.Add(15*time.Second))
	if err != nil || !c.Truncated {
		t.Errorf("a window before the ring: %v, truncated %v", err, c.Truncated)
	}

	outage := fakeRing{dir: ringDir, outages: []Outage{{From: s0.Add(22 * time.Second), To: s0.Add(27 * time.Second)}}}
	w, _, _ = newWriter(t, outage, env)
	c, err = w.Save(context.Background(), 4, s0.Add(15*time.Second), s0.Add(45*time.Second))
	if err != nil || !c.Truncated {
		t.Errorf("a window with an outage in it: %v, truncated %v", err, c.Truncated)
	}
	c, err = w.Save(context.Background(), 5, s0.Add(31*time.Second), s0.Add(45*time.Second))
	if err != nil || c.Truncated {
		t.Errorf("a window after the outage: %v, truncated %v", err, c.Truncated)
	}
}

// The segment that holds the end of the window is closed once the next one
// exists. Until then the clip would stop short.
func TestReadyWaitsForTheSegmentAfterTheWindow(t *testing.T) {
	ringDir := t.TempDir()
	writeSegments(t, ringDir, 10*time.Second, 4) // newest starts 03:00:30
	w, _, _ := newWriter(t, fakeRing{dir: ringDir}, nil)
	if !w.Ready(s0.Add(25 * time.Second)) {
		t.Error("not ready although a segment starts after the window")
	}
	if !w.Ready(s0.Add(30 * time.Second)) {
		t.Error("not ready although a segment starts at the end of the window")
	}
	if w.Ready(s0.Add(35 * time.Second)) {
		t.Error("ready although the segment holding the end is still open")
	}
}

// ffmpeg failing must not leave a half clip behind, and its reason must
// reach the caller.
func TestSaveReportsAFailedConcat(t *testing.T) {
	ringDir := t.TempDir()
	writeSegments(t, ringDir, 10*time.Second, 3)
	w, dir, _ := newWriter(t, fakeRing{dir: ringDir}, map[string]string{"FAKE_EXIT": "1", "FAKE_STDERR": "Invalid data found when processing input"})
	_, err := w.Save(context.Background(), 6, s0, s0.Add(20*time.Second))
	if err == nil || !strings.Contains(err.Error(), "Invalid data found") {
		t.Fatalf("Save gave %v", err)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*", "*")); len(entries) != 0 {
		t.Errorf("files left behind after a failure: %v", entries)
	}
}

// With ffmpeg installed, the whole path runs on real video: segments made
// from a test pattern, joined by copy into an MP4. Without it the test is
// skipped and says so.
func TestSaveWithRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed; the concat runs only on the fake")
	}
	ringDir := t.TempDir()
	for i := range 3 {
		name := filepath.Join(ringDir, SegmentName(s0.Add(time.Duration(i)*10*time.Second)))
		out, err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=duration=10:size=160x120:rate=10",
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p",
			"-f", "mpegts", name).CombinedOutput()
		if err != nil {
			t.Fatalf("making a segment: %v: %s", err, out)
		}
	}
	dir := filepath.Join(t.TempDir(), "video")
	w, err := NewWriter(WriterConfig{Dir: dir, Source: fakeRing{dir: ringDir}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.Save(context.Background(), 9, s0.Add(5*time.Second), s0.Add(25*time.Second))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if c.Duration < 29*time.Second || c.Duration > 31*time.Second {
		t.Errorf("duration %v, want about 30 s from three copied segments", c.Duration)
	}
	head := make([]byte, 12)
	f, err := os.Open(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Read(head); err != nil || string(head[4:8]) != "ftyp" {
		t.Errorf("the clip does not start with an MP4 ftyp box: %q", head)
	}
	if c.Bytes < 1000 {
		t.Errorf("the clip is only %d bytes", c.Bytes)
	}
}

// realRing is a segment source whose segment length the test chooses, for a
// ring written by real ffmpeg with a shorter segment than the box uses.
type realRing struct {
	dir     string
	nominal time.Duration
}

func (r realRing) Segments() ([]Segment, error) { return ListSegments(r.dir) }
func (r realRing) Outages() []Outage            { return nil }
func (r realRing) SegmentLength() time.Duration { return r.nominal }

// mustRun runs a program and fails the test with its output if it exits
// non-zero.
func mustRun(t *testing.T, program, what string, args ...string) {
	t.Helper()
	cmd := exec.Command(program, args...)
	cmd.Env = childEnv(nil)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v: %s", what, err, out)
	}
}

// ffprobeStreams is what ffprobe says about each stream of a file, one map
// per stream keyed by the field name.
func ffprobeStreams(t *testing.T, ffprobe, path string) []map[string]string {
	t.Helper()
	cmd := exec.Command(ffprobe, "-v", "error",
		"-show_entries", "stream=index,codec_type,codec_name,profile,sample_rate,channels,width,height,pix_fmt",
		"-of", "default", path)
	cmd.Env = childEnv(nil)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var streams []map[string]string
	var current map[string]string
	for _, line := range strings.Split(string(out), "\n") {
		switch strings.TrimSpace(line) {
		case "[STREAM]":
			current = map[string]string{}
			continue
		case "[/STREAM]":
			streams = append(streams, current)
			current = nil
			continue
		}
		if key, val, ok := strings.Cut(strings.TrimSpace(line), "="); ok && current != nil {
			current[key] = val
		}
	}
	return streams
}

// streamOfKind is the first stream of the given codec_type, or nil.
func streamOfKind(streams []map[string]string, kind string) map[string]string {
	for _, s := range streams {
		if s["codec_type"] == kind {
			return s
		}
	}
	return nil
}

// This is the gate for the bug the owner found on the box. With camera_audio
// on, the ring copied the camera's audio, the AAC configuration that arrives
// out of band never reached a segment, and the joined clip came out video only
// without a word of complaint.
//
// The whole path runs here on real ffmpeg: a source with real video and real
// audio, the ring's own argument list, the clip writer's own join. ffprobe
// then reads the result back. The argument lists are the proof that the video
// is copied and not re-encoded - there is no video encoder in them - and
// ffprobe confirms the clip still holds the source's own h264.
//
// ffmpeg is not on every machine, so the test skips without it. On the box:
//
//	go test ./internal/video -run RealFFmpeg -v
func TestCameraAudioSurvivesTheRingAndTheJoinWithRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed; this test has to run on the box")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed; it is what reads the result back")
	}

	// The stand-in camera: six seconds of test pattern with a 440 Hz tone at
	// 8 kHz mono, which is the rate the owner's camera sends.
	src := filepath.Join(t.TempDir(), "camera.ts")
	mustRun(t, ffmpeg, "making the source",
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=6:size=160x120:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "1",
		"-f", "mpegts", src)
	source := ffprobeStreams(t, ffprobe, src)
	if streamOfKind(source, "audio") == nil {
		t.Fatalf("the source has no audio stream, so it cannot test anything: %v", source)
	}

	// The ring, with its own arguments. Only the input is swapped: the camera
	// is not here, the codecs and the segmenter are. -re feeds the file at its
	// own rate, so each segment gets its own name from the clock.
	ringDir := t.TempDir()
	ring := RingArgs(testStream("Preview_01_sub"), 2, filepath.Join(ringDir, SegmentPattern), true)
	i := slices.Index(ring, "-i")
	if i < 0 || i+2 >= len(ring) {
		t.Fatalf("RingArgs has no input to swap: %v", ring)
	}
	if a := reencodesVideo(ring); a != "" {
		t.Fatalf("the ring re-encodes the video with %q: %v", a, ring)
	}
	args := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-re", "-i", src}, ring[i+2:]...)
	mustRun(t, ffmpeg, "writing the ring", args...)

	segs, err := ListSegments(ringDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) < 2 {
		t.Fatalf("the ring wrote %d segments, want at least 2", len(segs))
	}
	// The fault was here first: a copied track reached the segment with
	// sample_rate=0 and channels=0, and nothing downstream could recover it.
	seg := ffprobeStreams(t, ffprobe, segs[0].Path)
	a := streamOfKind(seg, "audio")
	if a == nil {
		t.Fatalf("the first segment has no audio stream: %v", seg)
	}
	if rate, _ := strconv.Atoi(a["sample_rate"]); rate == 0 {
		t.Errorf("the segment's audio stream has no sample rate, so it cannot be decoded: %v", a)
	}
	if ch, _ := strconv.Atoi(a["channels"]); ch == 0 {
		t.Errorf("the segment's audio stream has no channel count: %v", a)
	}

	// The join, with the clip writer's own arguments.
	dir := filepath.Join(t.TempDir(), "video")
	w, err := NewWriter(WriterConfig{Dir: dir, Source: realRing{dir: ringDir, nominal: 2 * time.Second},
		Audio: true})
	if err != nil {
		t.Fatal(err)
	}
	from, to := segs[0].Start, segs[len(segs)-1].Start.Add(time.Second)
	c, err := w.Save(context.Background(), 11, from, to)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !c.CameraAudio {
		t.Errorf("the clip says it carries no audio track, and the setting was on")
	}

	clip := ffprobeStreams(t, ffprobe, c.Path)
	a = streamOfKind(clip, "audio")
	if a == nil {
		t.Fatalf("the joined clip has no audio stream at all: %v", clip)
	}
	if rate, _ := strconv.Atoi(a["sample_rate"]); rate == 0 {
		t.Errorf("the clip's audio stream has no sample rate: %v", a)
	}
	if a["codec_name"] != "aac" {
		t.Errorf("the clip's audio codec is %q, want aac", a["codec_name"])
	}
	v := streamOfKind(clip, "video")
	if v == nil {
		t.Fatalf("the joined clip has no video stream: %v", clip)
	}
	// Copied, not re-encoded: the same codec, profile, size, and pixel format
	// as the source handed over.
	sv := streamOfKind(source, "video")
	for _, field := range []string{"codec_name", "profile", "width", "height", "pix_fmt"} {
		if v[field] != sv[field] {
			t.Errorf("the clip's video %s is %q and the source's is %q, so the video was re-encoded",
				field, v[field], sv[field])
		}
	}
}

// SPEC.md section 15 decision 22: with camera_audio on, the event clip keeps
// the camera's audio track, and the clip says so, so the screen that plays
// it can say so too. A clip cut with the setting off carries no audio and
// must not claim to.
func TestSaveKeepsTheCameraAudioWhenTheSettingIsOn(t *testing.T) {
	for _, audio := range []bool{false, true} {
		ringDir := t.TempDir()
		writeSegments(t, ringDir, 10*time.Second, 6)
		cmd, argv, env := fake(t, "concat", map[string]string{
			"FAKE_OUT_TIME_US": "30040000",
			// What ffmpeg lists when it reads the written clip back.
			"FAKE_INSPECT_INFO": "  Stream #0:0: Video: h264 (High), yuv420p, 160x120, 10 fps, 10 tbr, 90k tbn\n" +
				"  Stream #0:1: Audio: aac (LC), 8000 Hz, mono, fltp\n",
		})
		w, err := NewWriter(WriterConfig{
			Dir:     filepath.Join(t.TempDir(), "video"),
			Source:  fakeRing{dir: ringDir},
			Command: cmd, Env: env, Audio: audio,
		})
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		c, err := w.Save(context.Background(), 7, s0.Add(15*time.Second), s0.Add(35*time.Second))
		if err != nil {
			t.Fatalf("audio = %v: Save: %v", audio, err)
		}
		if c.CameraAudio != audio {
			t.Errorf("audio = %v: the clip reports CameraAudio = %v", audio, c.CameraAudio)
		}
		if c.AudioProblem != "" {
			t.Errorf("audio = %v: the clip reports a problem: %s", audio, c.AudioProblem)
		}
		runs := readArgv(t, argv)
		if got := contains(runs[0], "-an"); got == audio {
			t.Errorf("audio = %v: the concat ran with -an = %v: %v", audio, got, runs[0])
		}
		if a := reencodesVideo(runs[0]); a != "" {
			t.Errorf("audio = %v: the concat ran with %q: %v", audio, a, runs[0])
		}
		// With the setting off there is no audio to look for, so nothing is
		// read back: one run, the join. With it on the clip is read back.
		want := 1
		if audio {
			want = 2
		}
		if len(runs) != want {
			t.Fatalf("audio = %v: ffmpeg ran %d times, want %d", audio, len(runs), want)
		}
	}
}

// The clip's own record of its audio is read back from the file, not taken
// from the setting. The setting says what was asked for; only the file says
// what is there. A clip cut with camera_audio on that came out silent is the
// fault the owner found on the box, and it has to be visible from the
// instrument.
func TestSaveReadsBackWhetherTheClipReallyHasAudio(t *testing.T) {
	const video = "  Stream #0:0: Video: h264 (High), yuv420p, 160x120, 10 fps, 10 tbr, 90k tbn\n"
	for _, tc := range []struct {
		name        string
		info        string
		wantAudio   bool
		wantProblem string
	}{
		{
			name:      "the audio track is there and says its rate",
			info:      video + "  Stream #0:1: Audio: aac (LC), 8000 Hz, mono, fltp\n",
			wantAudio: true,
		},
		{
			name:        "the clip came out video only",
			info:        video,
			wantProblem: "no audio track",
		},
		{
			name:        "the audio track does not say its rate",
			info:        video + "  Stream #0:1: Audio: aac ([15][0][0][0] / 0x000F), 0 channels, fltp\n",
			wantProblem: "sample rate",
		},
		{
			name:        "the clip could not be read back",
			info:        "",
			wantProblem: "not known",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ringDir := t.TempDir()
			writeSegments(t, ringDir, 10*time.Second, 6)
			env := map[string]string{
				"FAKE_OUT_TIME_US":  "30040000",
				"FAKE_INSPECT_INFO": tc.info,
			}
			if tc.info == "" {
				env["FAKE_INSPECT_EXIT"] = "1"
			}
			cmd, _, extra := fake(t, "concat", env)
			w, err := NewWriter(WriterConfig{
				Dir:     filepath.Join(t.TempDir(), "video"),
				Source:  fakeRing{dir: ringDir},
				Command: cmd, Env: extra, Audio: true,
			})
			if err != nil {
				t.Fatalf("NewWriter: %v", err)
			}
			c, err := w.Save(context.Background(), 7, s0.Add(15*time.Second), s0.Add(35*time.Second))
			if err != nil {
				t.Fatalf("Save: %v", err)
			}
			if c.CameraAudio != tc.wantAudio {
				t.Errorf("CameraAudio = %v, want %v", c.CameraAudio, tc.wantAudio)
			}
			if tc.wantProblem == "" {
				if c.AudioProblem != "" {
					t.Errorf("AudioProblem = %q, want none", c.AudioProblem)
				}
				return
			}
			if !strings.Contains(c.AudioProblem, tc.wantProblem) {
				t.Errorf("AudioProblem = %q, want it to mention %q", c.AudioProblem, tc.wantProblem)
			}
			// A clip that lost its audio is still evidence and is still kept.
			if _, err := os.Stat(c.Path); err != nil {
				t.Errorf("the clip was not written: %v", err)
			}
		})
	}
}
