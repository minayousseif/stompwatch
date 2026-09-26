package video

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Command is the program that stands in for ffmpeg, with any leading
// arguments. nil means ffmpeg from PATH. Tests run the test binary instead.
type Command []string

func (c Command) program() string {
	if len(c) == 0 {
		return "ffmpeg"
	}
	return c[0]
}

func (c Command) leading() []string {
	if len(c) == 0 {
		return nil
	}
	return c[1:]
}

// CheckFFmpeg reports whether the program can be run at all. The collector
// calls it once at start, when video is on, so a box without ffmpeg says
// so in one line instead of logging a failed start every minute.
func CheckFFmpeg(c Command) error {
	if _, err := exec.LookPath(c.program()); err != nil {
		return fmt.Errorf("video: %s is not installed or not on PATH, so no video can be recorded; "+
			"install it with: apt install ffmpeg (%w)", c.program(), err)
	}
	return nil
}

// childEnv is the environment ffmpeg runs with. TZ is pinned to UTC so the
// segment names, which ffmpeg makes with strftime in local time, are UTC.
// No CAMERA_ entry is passed on, from the process or from extra: the login
// reaches ffmpeg on its URL and nowhere else. The collector has already
// taken it out of its own environment (ForgetEnvLogin); this is the second
// guard.
func childEnv(extra []string) []string {
	var env []string
	for _, kv := range append(append(os.Environ(), "TZ=UTC"), extra...) {
		if !strings.HasPrefix(kv, "CAMERA_") {
			env = append(env, kv)
		}
	}
	return env
}

// SpeechSafeCameraAudio is the setting of camera_audio at which a recording
// cannot hold speech: no camera audio at all. The collector warns at every
// start when camera_audio differs from it, whatever the default happens to
// be, because that is a privacy decision and the owner should never be able
// to forget it (SPEC.md section 15 decision 22).
const SpeechSafeCameraAudio = false

// rtspTimeout is how long ffmpeg waits on the socket before it gives up,
// in microseconds. Without it a camera that vanishes mid-stream can leave
// ffmpeg waiting for far longer than the stall limit.
const rtspTimeout = "10000000"

// dropAudio is the argument that keeps the camera's audio out of an output,
// or nothing when audio is wanted. The -an always sits after -i: before it,
// it would apply to the input, and ffmpeg ignores it there.
//
// audio is camera_audio. False, the default, forces -an as SPEC.md section 7
// says. True leaves the audio track in, unfiltered
// (SPEC.md section 15 decision 22).
func dropAudio(audio bool) []string {
	if audio {
		return nil
	}
	return []string{"-an"}
}

// ringCodecs is how the ring writes the streams it was given.
//
// With audio off it is one -c copy for everything, which is what SPEC.md
// section 7 asks for and what the default does.
//
// With audio on the video is still copied and the audio is re-encoded to
// AAC. The camera sends AAC whose configuration - sample rate, channel
// count, profile - arrives out of band, in the RTSP session description, and
// never inside the stream. A copied track therefore reaches an MPEG-TS
// segment with sample_rate=0 and channels=0, and no decoder can read it
// afterwards: measured on the owner's Amcrest camera with ffmpeg 7.1.5 on 12
// September 2026, where every segment's audio failed with "unspecified
// sample format" and the joined clip held no audio at all. Re-encoding at the
// ring, where the configuration is still to hand, makes each segment describe
// itself. 8 kHz mono AAC is negligible work; the video is never re-encoded,
// which is the property section 7 is about.
func ringCodecs(audio bool) []string {
	if audio {
		// The bitrate is pinned because the encoder's default asks for more
		// bits than an 8 kHz frame can hold, and ffmpeg then prints
		// "Too many bits ... clamping to max" on every reconnect. A log
		// line that appears every time teaches the owner to ignore the log.
		// 32 kbps is far more than 8 kHz mono needs.
		return []string{"-c:v", "copy", "-c:a", "aac", "-b:a", "32k"}
	}
	return []string{"-c", "copy"}
}

// RingArgs is the ffmpeg command line that writes stream s to segments of
// segmentSeconds under pattern (SPEC.md section 7). It copies the video, and
// drops the audio unless audio says to keep it.
func RingArgs(s Stream, segmentSeconds int, pattern string, audio bool) []string {
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "warning",
		"-rtsp_transport", "tcp", "-timeout", rtspTimeout,
		"-i", s.URL(),
	}
	args = append(args, dropAudio(audio)...)
	args = append(args, ringCodecs(audio)...)
	return append(args,
		"-f", "segment", "-segment_time", strconv.Itoa(segmentSeconds),
		"-segment_format", "mpegts", "-strftime", "1", "-reset_timestamps", "1",
		pattern,
	)
}

// keepsUsableAudio reports whether an ffmpeg command line writes an audio
// track that can be decoded afterwards. It has to name an audio encoder: a
// copied track from this camera reaches the file with no sample rate and no
// channel count, because the camera sends its AAC configuration out of band,
// and no decoder can read it. An -an anywhere settles it: nothing is written.
//
// It exists so that the claim in the start log - that the camera's audio is
// recorded into the ring - is read off the command line that will really run.
// A claim read off the setting is what let the log say audio was being
// recorded for a day while every clip came out silent.
func keepsUsableAudio(args []string) bool {
	if slices.Contains(args, "-an") {
		return false
	}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-c:a", "-codec:a", "-acodec":
			if args[i+1] != "copy" {
				return true
			}
		}
	}
	return false
}

// ProbeArgs is the ffmpeg command line that reads one frame of s and throws
// it away. ffmpeg prints the stream's codec, resolution, and rate on the way.
func ProbeArgs(s Stream) []string {
	return []string{
		"-nostdin", "-hide_banner",
		"-rtsp_transport", "tcp", "-timeout", rtspTimeout,
		"-i", s.URL(),
		"-an", "-frames:v", "1", "-f", "null", "-",
	}
}

// ConcatArgs is the ffmpeg command line that joins the segments in list
// into one MP4 at out, copying the streams. There is no trim: a clip runs
// from the first edge to the last (SPEC.md section 7). The progress output on
// stdout carries the length of the result. -y is there because out is a
// temporary file the caller has already created. audio is camera_audio: with
// it on, the segments' audio track is copied into the clip as it is.
//
// -map 0 takes every stream of the joined input. Without it ffmpeg picks one
// stream per kind by its own rules and drops the rest without a word, which
// is how a clip with no audio track went unnoticed for a day. With it, a
// stream that cannot be written fails the join instead of vanishing.
//
// -bsf:a aac_adtstoasc is the filter that turns ADTS AAC, which is how AAC is
// framed in MPEG-TS, into the form MP4 stores. ffmpeg 7's MP4 muxer adds that
// filter itself, so naming it changes nothing there; it is named because
// -map 0 makes an unwritable stream fatal, and because the filter passes a
// packet that already carries its configuration straight through, so it
// cannot do harm where it is not needed.
func ConcatArgs(list, out string, audio bool) []string {
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "warning", "-nostats", "-y",
		"-progress", "pipe:1",
		"-f", "concat", "-safe", "0", "-i", list,
	}
	args = append(args, dropAudio(audio)...)
	args = append(args, "-map", "0", "-c", "copy")
	if audio {
		args = append(args, "-bsf:a", "aac_adtstoasc")
	}
	return append(args, "-movflags", "+faststart", "-f", "mp4", out)
}

// InspectArgs is the ffmpeg command line that reads the streams of a file on
// disk and writes nothing. ffmpeg lists the streams of every input whatever it
// is asked to do with them, so one video frame, thrown away, is enough to
// learn what a file holds. ParseStreamInfo reads the list back.
//
// The -an is there for the same reason as everywhere else: this process reads
// no audio. The file is already written, so nothing here can change it.
func InspectArgs(path string) []string {
	return []string{
		"-nostdin", "-hide_banner",
		"-i", path,
		"-an", "-frames:v", "1", "-f", "null", "-",
	}
}

// ConcatList writes the list the concat demuxer reads. A quote in a path is
// closed, escaped, and opened again, which is how the demuxer wants it.
func ConcatList(segs []Segment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(s.Path, "'", `'\''`))
		b.WriteString("'\n")
	}
	return b.String()
}

// ParseProgress reads the length of the output from ffmpeg's -progress
// lines. The line repeats as ffmpeg runs, so the last value is the length.
func ParseProgress(out string) (time.Duration, bool) {
	var d time.Duration
	found := false
	for _, line := range strings.Split(out, "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "out_time_us=")
		if !ok {
			continue
		}
		us, err := strconv.ParseInt(v, 10, 64)
		if err != nil || us < 0 {
			continue
		}
		d, found = time.Duration(us)*time.Microsecond, true
	}
	return d, found
}

// tailBuffer keeps at most the last keep bytes written to it, for ffmpeg's
// error output, and only ever whole lines. ffmpeg repeats the camera URL in
// that output, and the callers redact the password from the whole buffer
// afterwards. Redact finds only the whole password, so a cut inside the URL
// would leave the tail of the password behind. When the buffer trims, it
// drops the rest of the line it cut into. When that line has not ended
// yet, it keeps nothing, and skips what is written until the line ends:
// no part of a line whose start is gone is ever kept.
type tailBuffer struct {
	keep int
	buf  []byte
	skip bool // true while the rest of a cut line is being dropped
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.skip {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			return n, nil
		}
		p, b.skip = p[i+1:], false
	}
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.keep; over > 0 {
		rest := b.buf[over:]
		if b.buf[over-1] != '\n' {
			// The cut is inside a line: drop what is left of it.
			i := bytes.IndexByte(rest, '\n')
			if i < 0 {
				b.buf, b.skip = b.buf[:0], true
				return n, nil
			}
			rest = rest[i+1:]
		}
		b.buf = append(b.buf[:0], rest...)
	}
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.buf) }
