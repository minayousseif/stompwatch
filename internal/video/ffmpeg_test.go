package video

import (
	"slices"
	"strings"
	"testing"
)

// hasPair reports whether flag is followed by value in args.
func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// reencodesVideo reports whether args ask ffmpeg to re-encode the video or to
// trim, which would break "no re-encode" and "pad outward rather than trim"
// (SPEC.md section 7). A video codec of copy is not a re-encode, so the pair
// is read and not only the flag. The audio codec is left alone here: the ring
// re-encodes the audio on purpose, because a copied track cannot be decoded
// (SPEC.md section 15 decision 22). An audio filter is still a fault: the
// camera's track is recorded as the camera heard it, unfiltered.
func reencodesVideo(args []string) string {
	for i, a := range args {
		switch a {
		case "-c:v", "-vcodec", "-codec:v":
			if i+1 < len(args) && args[i+1] != "copy" {
				return a + " " + args[i+1]
			}
			continue
		case "-vf", "-filter_complex", "-af", "-ss", "-t", "-to":
			return a
		}
		if strings.Contains(a, "libx26") || strings.Contains(a, "h264_") || strings.Contains(a, "hevc_") {
			return a
		}
	}
	return ""
}

// SPEC.md section 7: never ingest camera audio. With camera_audio off, which
// is the default, every ffmpeg command line carries -an, whatever it is for.
func TestEveryFFmpegCommandDropsAudioByDefault(t *testing.T) {
	s := testStream("Preview_01_sub")
	for name, args := range map[string][]string{
		"ring":   RingArgs(s, 10, "/ring/%Y%m%dT%H%M%SZ.ts", false),
		"test":   StreamTestArgs(s),
		"concat": ConcatArgs("/tmp/list.txt", "/tmp/out.mp4", false),
	} {
		if !slices.Contains(args, "-an") {
			t.Errorf("%s: no -an in %v", name, args)
		}
		if a := reencodesVideo(args); a != "" {
			t.Errorf("%s: %q re-encodes the video or trims: %v", name, a, args)
		}
		if !slices.Contains(args, "-nostdin") {
			t.Errorf("%s: no -nostdin, so ffmpeg could stop to wait for a key", name)
		}
	}
}

// SPEC.md section 15 decision 22: camera_audio drops the -an, and only then.
// The command lines that write to disk are the ring and the event-clip
// concat. Both directions are tested here: a command line that lost its -an
// with the setting off would let the default regress in silence, and one
// that kept it with the setting on would make the setting do nothing.
func TestCameraAudioKeepsTheAudioTrackInWhatIsWritten(t *testing.T) {
	s := testStream("Preview_01_sub")
	for name, args := range map[string][]string{
		"ring":   RingArgs(s, 10, "/ring/%Y%m%dT%H%M%SZ.ts", true),
		"concat": ConcatArgs("/tmp/list.txt", "/tmp/out.mp4", true),
	} {
		if slices.Contains(args, "-an") {
			t.Errorf("%s: camera_audio is on and -an is still there, so no audio is recorded: %v", name, args)
		}
		if a := reencodesVideo(args); a != "" {
			t.Errorf("%s: %q re-encodes the video or trims: %v", name, a, args)
		}
	}
	// The camera test keeps -an whichever way the setting is set. It throws its
	// output away, and it learns about audio from the stream list ffmpeg
	// prints about its input.
	if !slices.Contains(StreamTestArgs(s), "-an") {
		t.Error("the camera test dropped -an; it records nothing and needs none")
	}
}

// The camera sends AAC whose configuration arrives out of band, in the RTSP
// session description. A copied track therefore reaches an MPEG-TS segment
// with sample_rate=0 and channels=0, and no decoder can read it: measured on
// the owner's camera with ffmpeg 7.1.5 on 12 September 2026. The ring has to
// re-encode the audio, where the configuration is still to hand, so that the
// segment describes itself. The video is copied, as section 7 requires: 8 kHz
// mono audio is negligible work, a video re-encode is not.
func TestRingReEncodesTheAudioAndNeverTheVideo(t *testing.T) {
	s := testStream("Preview_01_sub")
	on := RingArgs(s, 10, "/ring/%Y%m%dT%H%M%SZ.ts", true)
	for _, want := range [][2]string{{"-c:v", "copy"}, {"-c:a", "aac"}} {
		if !hasPair(on, want[0], want[1]) {
			t.Errorf("camera_audio is on: no %s %s in %v", want[0], want[1], on)
		}
	}
	if hasPair(on, "-c", "copy") {
		t.Errorf("the ring copies every stream, so the audio reaches the segment undecodable: %v", on)
	}
	if hasPair(on, "-c:a", "copy") {
		t.Errorf("the ring copies the audio, so a segment cannot be decoded: %v", on)
	}

	// With the setting off nothing changes at all: -an, and one -c copy for
	// the whole stream.
	off := RingArgs(s, 10, "/ring/%Y%m%dT%H%M%SZ.ts", false)
	if !slices.Contains(off, "-an") {
		t.Errorf("camera_audio is off, so the ring must carry -an: %v", off)
	}
	if !hasPair(off, "-c", "copy") {
		t.Errorf("camera_audio is off, so the ring must copy the whole stream: %v", off)
	}
	if slices.Contains(off, "-c:a") {
		t.Errorf("camera_audio is off, so the ring must name no audio codec: %v", off)
	}
}

// Without -map 0 ffmpeg chooses one stream per kind by its own rules and
// drops the rest without a word. That silence is what hid this bug: the
// joined clip had no audio track and nothing said so. -map 0 takes every
// stream of the input, so a stream that cannot be written fails the join
// instead of vanishing.
func TestConcatMapsEveryStream(t *testing.T) {
	for _, audio := range []bool{false, true} {
		args := ConcatArgs("/tmp/list.txt", "/tmp/out.mp4", audio)
		if !hasPair(args, "-map", "0") {
			t.Errorf("camera_audio=%v: no -map 0, so a stream can be dropped in silence: %v", audio, args)
		}
	}
}

// The whole command line, written out, so a change to it has to be meant.
// The URL is the literal form url_test.go pins, not a value read back from
// the code under test.
func TestRingArgsAreExactlyThis(t *testing.T) {
	const url = "rtsp://admin:p%40ss%3Aw%2Frd%231%20%3F@cam.local:554/Preview_01_sub"
	s := testStream("Preview_01_sub")

	want := []string{
		"-nostdin", "-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp", "-timeout", "10000000",
		"-i", url,
		"-c:v", "copy", "-c:a", "aac", "-b:a", "32k",
		"-f", "segment", "-segment_time", "10",
		"-segment_format", "mpegts", "-strftime", "1", "-reset_timestamps", "1",
		"/ring/%Y%m%dT%H%M%SZ.ts",
	}
	if got := RingArgs(s, 10, "/ring/%Y%m%dT%H%M%SZ.ts", true); !slices.Equal(got, want) {
		t.Errorf("camera_audio on:\n got %v\nwant %v", got, want)
	}

	want = []string{
		"-nostdin", "-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp", "-timeout", "10000000",
		"-i", url,
		"-an",
		"-c", "copy",
		"-f", "segment", "-segment_time", "10",
		"-segment_format", "mpegts", "-strftime", "1", "-reset_timestamps", "1",
		"/ring/%Y%m%dT%H%M%SZ.ts",
	}
	if got := RingArgs(s, 10, "/ring/%Y%m%dT%H%M%SZ.ts", false); !slices.Equal(got, want) {
		t.Errorf("camera_audio off:\n got %v\nwant %v", got, want)
	}
}

// The same for the join. With audio on the join also names the bitstream
// filter that turns ADTS AAC, which is how AAC is framed in MPEG-TS, into the
// form MP4 stores. ffmpeg 7's MP4 muxer inserts that filter itself, so naming
// it changes nothing there, and it costs nothing where it is not needed: the
// filter passes a packet that already carries its configuration straight
// through. -map 0 turns a stream that cannot be written into an error, so the
// filter the muxer needs is named rather than hoped for.
func TestConcatArgsAreExactlyThis(t *testing.T) {
	want := []string{
		"-nostdin", "-hide_banner", "-loglevel", "warning", "-nostats", "-y",
		"-progress", "pipe:1",
		"-f", "concat", "-safe", "0", "-i", "/tmp/list.txt",
		"-map", "0",
		"-c", "copy", "-bsf:a", "aac_adtstoasc",
		"-movflags", "+faststart", "-f", "mp4", "/tmp/out.mp4",
	}
	if got := ConcatArgs("/tmp/list.txt", "/tmp/out.mp4", true); !slices.Equal(got, want) {
		t.Errorf("camera_audio on:\n got %v\nwant %v", got, want)
	}

	want = []string{
		"-nostdin", "-hide_banner", "-loglevel", "warning", "-nostats", "-y",
		"-progress", "pipe:1",
		"-f", "concat", "-safe", "0", "-i", "/tmp/list.txt",
		"-an",
		"-map", "0",
		"-c", "copy", "-movflags", "+faststart", "-f", "mp4", "/tmp/out.mp4",
	}
	if got := ConcatArgs("/tmp/list.txt", "/tmp/out.mp4", false); !slices.Equal(got, want) {
		t.Errorf("camera_audio off:\n got %v\nwant %v", got, want)
	}
}

func TestRingArgsFollowTheSpec(t *testing.T) {
	s := testStream("Preview_01_sub")
	args := RingArgs(s, 10, "/ring/%Y%m%dT%H%M%SZ.ts", false)
	for _, want := range [][2]string{
		{"-rtsp_transport", "tcp"},
		{"-i", s.URL()},
		{"-c", "copy"},
		{"-f", "segment"},
		{"-segment_time", "10"},
		{"-segment_format", "mpegts"},
		{"-strftime", "1"},
	} {
		if !hasPair(args, want[0], want[1]) {
			t.Errorf("no %s %s in %v", want[0], want[1], args)
		}
	}
	if args[len(args)-1] != "/ring/%Y%m%dT%H%M%SZ.ts" {
		t.Errorf("the output pattern is not last: %v", args)
	}
	// The -an comes after -i: before it, it would apply to the input and
	// ffmpeg ignores it there.
	if slices.Index(args, "-an") < slices.Index(args, "-i") {
		t.Errorf("-an is before -i, where it does nothing: %v", args)
	}
	// Only the URL may hold the password, and only once.
	joined := strings.Join(args, " ")
	if strings.Count(joined, testPass) != 0 || strings.Count(joined, escapePassword(testPass)) != 1 {
		t.Errorf("the password appears other than once, escaped, in the URL: %v", args)
	}
}

func TestConcatArgsCopyIntoAnMP4(t *testing.T) {
	args := ConcatArgs("/tmp/list.txt", "/tmp/out.mp4", false)
	for _, want := range [][2]string{
		{"-f", "concat"},
		{"-safe", "0"},
		{"-i", "/tmp/list.txt"},
		{"-c", "copy"},
		{"-movflags", "+faststart"},
		{"-progress", "pipe:1"},
	} {
		if !hasPair(args, want[0], want[1]) {
			t.Errorf("no %s %s in %v", want[0], want[1], args)
		}
	}
	if args[len(args)-1] != "/tmp/out.mp4" {
		t.Errorf("the output is not last: %v", args)
	}
	if slices.Index(args, "-an") < slices.Index(args, "-i") {
		t.Errorf("-an is before -i, where it does nothing: %v", args)
	}
}

func TestCameraTestArgsReadOneFrameAndDiscardIt(t *testing.T) {
	s := testStream("Preview_01_sub")
	args := StreamTestArgs(s)
	for _, want := range [][2]string{
		{"-rtsp_transport", "tcp"},
		{"-i", s.URL()},
		{"-frames:v", "1"},
		{"-f", "null"},
	} {
		if !hasPair(args, want[0], want[1]) {
			t.Errorf("no %s %s in %v", want[0], want[1], args)
		}
	}
}

// The concat list is the one place a path is quoted for ffmpeg. A quote in
// a path must be escaped the way the concat demuxer reads it.
func TestConcatListQuotesPaths(t *testing.T) {
	got := ConcatList([]Segment{{Path: "/ring/a.ts"}, {Path: "/ring/it's.ts"}})
	want := "file '/ring/a.ts'\nfile '/ring/it'\\''s.ts'\n"
	if got != want {
		t.Fatalf("ConcatList = %q, want %q", got, want)
	}
}

// out_time_us is the progress line that carries the clip length.
func TestParseProgressReadsTheLength(t *testing.T) {
	got, ok := ParseProgress("frame=450\nfps=0.0\nout_time_us=30040000\nout_time=00:00:30.040000\nprogress=end\n")
	if !ok || got.Milliseconds() != 30040 {
		t.Fatalf("ParseProgress = %v, %v", got, ok)
	}
	if _, ok := ParseProgress("frame=0\nprogress=end\n"); ok {
		t.Error("ParseProgress found a length in output with none")
	}
	// The last value wins: the line repeats every second.
	got, _ = ParseProgress("out_time_us=1000000\nout_time_us=2000000\n")
	if got.Seconds() != 2 {
		t.Errorf("ParseProgress took %v, not the last value", got)
	}
}

// ffmpeg repeats the camera URL in its error output, and the callers redact
// the whole tail buffer afterwards. Redact finds only the whole password, so
// a cut that falls inside the password must not leave its tail behind.
func TestTailBufferNeverKeepsPartOfALine(t *testing.T) {
	const pass = "Hunter2Kangaroo9"
	s := Stream{Host: "cam.local", Port: 554, Path: "Preview_01_sub", Creds: Credentials{User: "admin", Pass: pass}}
	urlLine := "[rtsp @ 0x1] Input #0, rtsp, from 'rtsp://admin:" + pass + "@cam.local:554/Preview_01_sub':\n"
	lastLine := "[rtsp @ 0x1] method DESCRIBE failed: 401 Unauthorized\n"
	cutInPass := strings.Index(urlLine, pass) + 7 // the cut leaves "Kangaroo9" of the password

	for _, chunk := range []int{1, 5, len(urlLine) + len(lastLine)} {
		b := &tailBuffer{keep: len(urlLine) - cutInPass + len(lastLine)}
		stream := urlLine + lastLine
		for len(stream) > 0 {
			n := min(chunk, len(stream))
			b.Write([]byte(stream[:n]))
			stream = stream[n:]
		}
		got := s.Redact(b.String())
		if strings.Contains(got, pass[7:]) {
			t.Errorf("writes of %d bytes: the tail of the password survived redaction: %q", chunk, got)
		}
		if got != lastLine {
			t.Errorf("writes of %d bytes: kept %q, want only the whole last line %q", chunk, got, lastLine)
		}
	}
}

// A line longer than the buffer cannot be kept whole, so none of it is kept,
// and the buffer starts again at the next line.
func TestTailBufferDropsALineLongerThanItself(t *testing.T) {
	const pass = "Hunter2Kangaroo9"
	s := Stream{Host: "cam.local", Port: 554, Creds: Credentials{User: "admin", Pass: pass}}
	b := &tailBuffer{keep: 40}
	b.Write([]byte("short line\n"))
	// The last 40 bytes start inside the password: "r2Kangaroo9@cam...".
	b.Write([]byte("[rtsp @ 0x1] could not open rtsp://admin:" + pass + "@cam.local:554/Preview_01_sub"))
	if got := s.Redact(b.String()); strings.Contains(got, "Kangaroo") {
		t.Fatalf("part of the password survived redaction: %q", got)
	}
	b.Write([]byte(" timed out\nConnection refused\n"))
	if got := b.String(); got != "Connection refused\n" {
		t.Fatalf("after the long line the buffer holds %q, want %q", got, "Connection refused\n")
	}
}
