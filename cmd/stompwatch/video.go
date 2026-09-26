package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/video"
)

// ringDirName is the directory under video_dir that holds the segment ring.
// Event clips go beside it, under the date.
const ringDirName = "ring"

// camera is the running video side: the ring that records the sub-stream
// and the writer that cuts event clips from it.
type camera struct {
	ring   *video.Ring
	writer *video.Writer
	dir    string       // video_dir, the root the dashboard serves clips from
	stream video.Stream // the camera, for the clock check
	ntp    string       // the NTP server both clocks are checked against
	// audio is what the camera answered when it was asked whether its
	// stream carries an audio track, and audioKnown says whether it was
	// asked at all. Both are written during setup, before the ring and the
	// dashboard run, and never written again.
	audio      video.AudioInfo
	audioKnown bool
}

// ffmpegCommand turns the -ffmpeg flag into a command. Empty means ffmpeg
// from PATH.
func ffmpegCommand(path string) video.Command {
	if path == "" {
		return nil
	}
	return video.Command{path}
}

// setupVideo prepares the camera when camera_host is set. Any problem is
// logged, recorded, and reported, and the collector carries on without
// video: nothing in it may depend on the camera (SPEC.md section 7). record is
// the pipeline's health queue; it may be called before the pipeline runs.
// paused reports whether an instant is inside the daily recording pause; the
// ring runs no ffmpeg inside it. It reads the live settings, so a change from
// the dashboard reaches the ring without a restart. login is the camera login
// the command read at start, before it took it out of the environment.
func setupVideo(s config.Config, login video.Login, ffmpeg string, log *slog.Logger,
	record func(time.Time, string, string, time.Duration), alert *alerter,
	paused func(time.Time) bool) *camera {
	if !s.VideoEnabled() {
		log.Info("video is off; camera_host is empty")
		return nil
	}
	off := func(why string, err error) *camera {
		log.Error("video is off: "+why, "err", err)
		record(time.Now(), store.HealthCameraDisconnect, "video is off: "+why+": "+err.Error(), 0)
		alert.now("video is off: " + why + ": " + err.Error())
		return nil
	}
	if login.Err != nil {
		return off("no camera login", login.Err)
	}
	creds := login.Creds
	cmd := ffmpegCommand(ffmpeg)
	if err := video.CheckFFmpeg(cmd); err != nil {
		return off("ffmpeg is missing", err)
	}
	var streams []video.Stream
	for _, path := range video.CandidatePaths(s.CameraRTSPPath) {
		streams = append(streams, video.Stream{Host: s.CameraHost, Port: s.CameraPort, Path: path, Creds: creds})
	}
	ring, err := video.NewRing(video.RingConfig{
		Dir:            filepath.Join(s.VideoDir, ringDirName),
		Streams:        streams,
		SegmentSeconds: s.VideoSegmentSeconds,
		Keep:           time.Duration(s.VideoRingMinutes) * time.Minute,
		Audio:          s.CameraAudio,
		Command:        cmd,
		OnEvent:        cameraEvents(log, record, alert),
		Paused:         paused,
	})
	if err != nil {
		return off("the ring could not be set up", err)
	}
	writer, err := video.NewWriter(video.WriterConfig{
		Dir: s.VideoDir, Source: ring, Command: cmd, Audio: s.CameraAudio,
	})
	if err != nil {
		return off("the clip writer could not be set up", err)
	}
	log.Info("video is on", "camera", streams[0], "paths", len(streams),
		"ring_minutes", s.VideoRingMinutes, "segment_seconds", s.VideoSegmentSeconds,
		"video_dir", s.VideoDir, "camera_audio", s.CameraAudio)
	cam := &camera{ring: ring, writer: writer, dir: s.VideoDir, stream: streams[0], ntp: s.NTPServer}
	if s.CameraAudio {
		// A setting that silently does nothing is a trap, so the one
		// question is asked now rather than left for the owner to wonder
		// about (SPEC.md section 15 decision 22). The ring itself is handed
		// over, not the setting, so the line that says the audio is recorded
		// can only be printed when the ring really records it.
		cam.audio, cam.audioKnown = checkCameraAudio(cmd, streams, ring, log)
	}
	return cam
}

// cameraAudioCheck bounds the one question the collector asks the camera at
// start. Nothing in the collector may depend on the camera (SPEC.md section 7),
// so a camera that does not answer costs this much of the start and no more.
// A camera on the same network answers an RTSP DESCRIBE in well under a
// second.
const cameraAudioCheck = 5 * time.Second

// checkCameraAudio asks the camera whether its stream carries an audio track
// and says what it found. It reports false when nothing answered: "no audio"
// would then be a reading nobody made.
//
// ring is the ring that will do the recording. Whether the camera's audio is
// really kept is asked of it, and never of the setting: for a day the line
// that says the audio is recorded came from the setting alone, and it said
// audio was being recorded while every clip came out silent. Taking the ring
// rather than a boolean leaves no way to pass the setting by mistake.
//
// It runs only while camera_audio is on, so a fresh install asks the camera
// nothing and starts exactly as fast as before.
func checkCameraAudio(cmd video.Command, streams []video.Stream, ring *video.Ring, log *slog.Logger) (video.AudioInfo, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), cameraAudioCheck)
	defer cancel()
	var paths []string
	for _, s := range streams {
		paths = append(paths, s.Path)
	}
	rep := video.Probe(ctx, cmd, nil, streams[0], paths)
	if rep.Working == nil {
		log.Warn("camera_audio is on, but no stream answered, so whether this camera sends any audio is not known",
			"camera_audio", true, "paths_tried", len(rep.Tried))
		return video.AudioInfo{}, false
	}
	a := rep.Working.Info.Audio
	if !a.Present {
		log.Warn("camera_audio is on, but this camera's stream carries no audio track, so it records none",
			"camera_audio", true, "path", rep.Working.Stream.Path,
			"effect", "nothing changes until a camera that sends audio is used")
		return a, true
	}
	if !ring.RecordsAudio() {
		log.Error("camera_audio is on and the camera sends audio, but the ring is not set up to keep it, so nothing is recorded",
			"camera_audio", true, "codec", a.Codec, "rate_hz", a.RateHz, "path", rep.Working.Stream.Path,
			"effect", "the clips will have no sound; this is a fault in the ring's ffmpeg arguments, not a setting")
		return a, true
	}
	log.Info("the camera sends audio, and camera_audio records it into the ring and every clip",
		"codec", a.Codec, "rate_hz", a.RateHz, "path", rep.Working.Stream.Path)
	return a, true
}

// clockCheckDelay is how long after start the first clock check runs, and
// clockCheckEvery is how often it runs after that.
const (
	clockCheckDelay = time.Minute
	clockCheckEvery = time.Hour
)

// checkClocks measures the host and the camera against the same NTP server
// and reports anything more than 2 s out (SPEC.md section 7).
func (c *camera) checkClocks(ctx context.Context, log *slog.Logger, record func(time.Time, string, string, time.Duration)) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reportClocks(video.CheckClocks(cctx, c.ntp, c.stream), log, record)
}

// reportClocks logs a report and writes a clock_drift row for each problem
// in it. A clean check is logged too, so the log shows the check ran.
//
// Both drifts are logged in milliseconds, with the unit in the key. A
// duration logged as itself comes out as a bare count of nanoseconds, and
// -12000000 does not say whether it is 12 ms or 12 seconds.
func reportClocks(rep video.ClockReport, log *slog.Logger, record func(time.Time, string, string, time.Duration)) {
	hostMS := rep.HostOffset.Round(time.Millisecond).Milliseconds()
	cameraMS := rep.CameraOffset.Round(time.Millisecond).Milliseconds()
	problems := rep.Problems()
	if len(problems) == 0 {
		log.Info("clocks checked", "host_from_ntp_ms", hostMS,
			"camera_from_host_ms", cameraMS, "limit_ms", video.MaxClockDrift.Milliseconds())
		return
	}
	for _, p := range problems {
		log.Warn("clock drift", "problem", p, "host_from_ntp_ms", hostMS,
			"camera_from_host_ms", cameraMS)
		record(rep.At, store.HealthClockDrift, p, driftOf(rep, p))
	}
}

// driftOf is the drift a problem is about, for the row's duration column.
func driftOf(rep video.ClockReport, problem string) time.Duration {
	switch {
	case strings.HasPrefix(problem, "the host clock is"):
		return rep.HostOffset
	case strings.HasPrefix(problem, "the camera clock is") && strings.Contains(problem, "from NTP"):
		return rep.CameraFromNTP()
	case strings.HasPrefix(problem, "the camera clock is") && strings.Contains(problem, "from the host"):
		return rep.CameraOffset
	}
	return 0
}

// cameraEvents logs and records what happens to the camera stream. Every
// disconnect gets a row when it happens and another, with its length, when
// video resumes (SPEC.md section 7).
func cameraEvents(log *slog.Logger, record func(time.Time, string, string, time.Duration), alert *alerter) func(video.Event) {
	return func(e video.Event) {
		switch e.Kind {
		case video.Started:
			log.Info("camera connected", "stream", e.Stream, "gap", e.Gap, "after_pause", e.AfterPause)
			// The stream after a recording pause follows a stop the owner
			// chose. The pipeline already logged the pause, so that time is
			// not a gap in the video.
			if e.Stream > 1 && !e.AfterPause {
				record(e.At, store.HealthCameraDisconnect,
					fmt.Sprintf("no video for %s between stream %d and stream %d", e.Gap.Round(time.Second), e.Stream-1, e.Stream), e.Gap)
			}
		case video.Exited:
			log.Warn("camera disconnected; reconnecting", "stream", e.Stream, "detail", e.Detail)
			record(e.At, store.HealthCameraDisconnect, fmt.Sprintf("stream %d ended: %s", e.Stream, e.Detail), 0)
			alert.limited("the camera disconnected: " + e.Detail)
		case video.Stuck:
			log.Error("camera stream stalled; restarting", "stream", e.Stream, "detail", e.Detail)
			record(e.At, store.HealthCameraDisconnect, fmt.Sprintf("stream %d stalled: %s", e.Stream, e.Detail), 0)
		case video.Paused:
			log.Info("camera stream stopped for the daily recording pause; no video is recorded until it ends",
				"stream", e.Stream)
		case video.PruneFailed:
			log.Error("the video ring could not be pruned", "detail", e.Detail)
			record(e.At, store.HealthWriteError, "video ring: "+e.Detail, 0)
		}
	}
}

// cmdProbeCamera tries each RTSP path on the camera and prints what works
// (SPEC.md section 7). It is the tool for bringing a real camera up.
func cmdProbeCamera(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("probe-camera", flag.ContinueOnError)
	fl.SetOutput(stderr)
	cfgPath := fl.String("config", defaultConfig, "config file")
	ffmpeg := fl.String("ffmpeg", "", "ffmpeg program to run; empty means ffmpeg from PATH")
	host := fl.String("host", "", "camera host to try instead of camera_host from the config")
	if fl.Parse(args) != nil {
		return 2
	}
	s, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}
	if *host != "" {
		s.CameraHost = *host
	}
	if s.CameraHost == "" {
		fmt.Fprintln(stderr, "stompwatch: camera_host is not set; set it in the config file or pass -host")
		return 1
	}
	// Read once, then out of the environment, so the ffmpeg this starts
	// does not inherit the password.
	creds, err := video.LoadCredentials(os.Getenv, s.CameraCredentialsFile)
	video.ForgetEnvLogin()
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}
	cmd := ffmpegCommand(*ffmpeg)
	if err := video.CheckFFmpeg(cmd); err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	base := video.Stream{Host: s.CameraHost, Port: s.CameraPort, Creds: creds}
	fmt.Fprintf(stdout, "Trying the sub-stream on %s as %s:\n", s.CameraHost, creds.User)

	// The dashboard's test button runs this same function, so the two can
	// never disagree about what works.
	rep := video.ProbeCamera(ctx, cmd, nil, base, video.CandidatePaths(s.CameraRTSPPath), s.CameraRTSPPathMain)

	fmt.Fprint(stdout, indent(rep.Sub.String()))
	if rep.Sub.Working == nil {
		fmt.Fprintln(stdout, "\nNo sub-stream path worked. Check camera_host, the login in CAMERA_USER and CAMERA_PASS"+
			" or camera_credentials_file, and that RTSP is switched on in the camera's settings.")
		return 1
	}
	sub := rep.Sub.Working.Stream.Path
	fmt.Fprintf(stdout, "\nThe sub-stream works: %s, %s\n", rep.Sub.Working.Stream, rep.Sub.Working.Info)

	if rep.MainPath == "" {
		fmt.Fprintln(stdout, "This sub path is not one of the known cameras, so the main stream path is not known;"+
			" set camera_rtsp_path_main to try it.")
	} else {
		fmt.Fprintln(stdout, "\nTrying the main stream:")
		fmt.Fprint(stdout, indent(rep.Main.String()))
		if rep.Main.Working == nil {
			fmt.Fprintln(stdout, "The main stream did not answer. The ring and the event clips use the sub-stream only, so they do not need it.")
		}
	}

	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, audioNote(rep.Sub.Working.Info.Audio, s.CameraAudio))

	fmt.Fprintln(stdout)
	if rep.ClockErr != nil {
		fmt.Fprintf(stdout, "Camera clock: not readable (%v). Check it by hand against the host's date.\n", rep.ClockErr)
	} else {
		fmt.Fprintf(stdout, "Camera clock: %s from the host clock; the limit is %s.\n",
			rep.ClockOffset.Round(time.Millisecond), video.MaxClockDrift)
	}

	fmt.Fprintf(stdout, "\nPut this in the config file:\n\ncamera_host = %s\ncamera_rtsp_path = %s\n", s.CameraHost, sub)
	if s.CameraRTSPPathMain == "" && rep.MainPath != "" {
		fmt.Fprintf(stdout, "camera_rtsp_path_main = %s\n", rep.MainPath)
	}
	return 0
}

// audioNote says whether the camera sends an audio track and what
// camera_audio would do about it. A setting that silently does nothing is a
// trap, so the camera with no microphone is named as such
// (SPEC.md section 15 decision 22).
func audioNote(a video.AudioInfo, setting bool) string {
	if !a.Present {
		return "Audio: this camera sends no audio track, so camera_audio would record nothing.\n"
	}
	what := fmt.Sprintf("Audio: this camera sends a %s track", a.Codec)
	if a.RateHz != 0 {
		what += fmt.Sprintf(" at %d Hz", a.RateHz)
	}
	if setting {
		return what + ".\ncamera_audio = true, so the ring and every event clip record it, unfiltered.\n" +
			"It holds speech in clear. The microphone's own clips are filtered and do not.\n"
	}
	return what + ".\ncamera_audio = false, so it is dropped: every ffmpeg command line carries -an.\n" +
		"Setting camera_audio = true would record it unfiltered, speech in clear.\n"
}

// indent sets a block of lines in from the margin.
func indent(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}
