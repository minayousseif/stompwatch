package video

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AudioInfo is the audio track the camera sends, if it sends one. Many
// cameras have no microphone, and then camera_audio does nothing, so the
// probe has to be able to say which kind the camera is
// (SPEC.md section 15 decision 22).
type AudioInfo struct {
	// Present is false when the stream carries no audio track. Codec and
	// RateHz are then empty and zero, not a reading nobody made.
	Present bool
	Codec   string
	RateHz  int
}

func (a AudioInfo) String() string {
	if !a.Present {
		return "no audio"
	}
	if a.RateHz == 0 {
		return "audio: " + a.Codec
	}
	return fmt.Sprintf("audio: %s at %d Hz", a.Codec, a.RateHz)
}

// StreamInfo is what the camera negotiated.
type StreamInfo struct {
	Codec  string
	Width  int
	Height int
	FPS    float64
	// Audio is the audio track of the same stream. The probe reads it from
	// the input ffmpeg lists, which it prints whether or not -an is given,
	// so finding out costs no extra connection and records nothing.
	Audio AudioInfo
}

func (i StreamInfo) String() string {
	return fmt.Sprintf("%dx%d at %g fps (%s), %s", i.Width, i.Height, i.FPS, i.Codec, i.Audio)
}

// Attempt is one stream the probe tried.
type Attempt struct {
	Stream Stream
	Info   StreamInfo
	Err    error // nil when the stream works
}

// Report is what probe-camera prints. Every string in it is masked.
type Report struct {
	Tried   []Attempt
	Working *Attempt // the first stream that worked, or nil
}

// String lists what was tried, in order, and what came of each.
func (r Report) String() string {
	var b strings.Builder
	for _, a := range r.Tried {
		if a.Err != nil {
			fmt.Fprintf(&b, "%s: failed: %v\n", a.Stream, a.Err)
		} else {
			fmt.Fprintf(&b, "%s: works: %s\n", a.Stream, a.Info)
		}
	}
	return b.String()
}

// probeTimeout bounds one attempt. A camera that does not answer in this
// long is not going to.
const probeTimeout = 20 * time.Second

// Probe tries each path on the camera in order and stops at the first that
// delivers a frame (SPEC.md section 7). It never returns a password in any field.
func Probe(ctx context.Context, cmd Command, env []string, base Stream, paths []string) Report {
	var rep Report
	for _, path := range paths {
		s := base
		s.Path = path
		a := Attempt{Stream: s}
		a.Info, a.Err = probeOne(ctx, cmd, env, s)
		rep.Tried = append(rep.Tried, a)
		if a.Err == nil {
			rep.Working = &rep.Tried[len(rep.Tried)-1]
			break
		}
	}
	return rep
}

func probeOne(ctx context.Context, cmd Command, env []string, s Stream) (StreamInfo, error) {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := append(slices.Clone(cmd.leading()), ProbeArgs(s)...)
	c := exec.CommandContext(pctx, cmd.program(), args...)
	c.Env = childEnv(env)
	stderr := &tailBuffer{keep: 8192}
	c.Stderr = stderr
	err := c.Run()
	info, ok := ParseStreamInfo(stderr.String())
	if err != nil {
		return StreamInfo{}, s.Errorf("%s", lastLine(s.Redact(stderr.String()), err.Error()))
	}
	if !ok {
		return StreamInfo{}, s.Errorf("ffmpeg connected but reported no video stream: %s", lastLine(s.Redact(stderr.String()), "no output"))
	}
	return info, nil
}

// lastLine is the last non-empty line of text, or fallback.
func lastLine(text, fallback string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return fallback
}

var (
	videoLine  = regexp.MustCompile(`Video: ([A-Za-z0-9_]+)`)
	audioLine  = regexp.MustCompile(`Audio: ([A-Za-z0-9_]+)`)
	resolution = regexp.MustCompile(`\b(\d{2,5})x(\d{2,5})\b`)
	fpsField   = regexp.MustCompile(`\b([\d.]+) fps\b`)
	tbrField   = regexp.MustCompile(`\b([\d.]+) tbr\b`)
	hzField    = regexp.MustCompile(`\b(\d{3,6}) Hz\b`)
)

// ParseStreamInfo reads the codec, resolution, and rate from ffmpeg's video
// stream line, and the codec and sample rate from its audio stream line if
// there is one. The rate is the fps field, or tbr when there is no fps.
//
// It reports false when there is no video stream: a camera that answers with
// audio alone is not a camera this instrument can use. The audio line may
// come before the video line or after it, so every line is read.
func ParseStreamInfo(stderr string) (StreamInfo, bool) {
	var info StreamInfo
	found := false
	for _, line := range strings.Split(stderr, "\n") {
		if m := audioLine.FindStringSubmatch(line); m != nil && !info.Audio.Present {
			info.Audio = AudioInfo{Present: true, Codec: m[1]}
			if hz := hzField.FindStringSubmatch(line); hz != nil {
				info.Audio.RateHz, _ = strconv.Atoi(hz[1])
			}
			continue
		}
		m := videoLine.FindStringSubmatch(line)
		if m == nil || found {
			continue
		}
		res := resolution.FindStringSubmatch(line)
		if res == nil {
			continue
		}
		info.Codec = m[1]
		info.Width, _ = strconv.Atoi(res[1])
		info.Height, _ = strconv.Atoi(res[2])
		if f := fpsField.FindStringSubmatch(line); f != nil {
			info.FPS, _ = strconv.ParseFloat(f[1], 64)
		} else if f := tbrField.FindStringSubmatch(line); f != nil {
			info.FPS, _ = strconv.ParseFloat(f[1], 64)
		}
		found = true
	}
	if !found {
		return StreamInfo{}, false
	}
	return info, true
}
