// Command stompwatch measures impact noise, detects impact events, and records
// short audio clips of them.
//
//	stompwatch run          -config FILE [-input WAV] [-ffmpeg PATH]
//	stompwatch calibrate    -config FILE [-input WAV] [-seconds N] [-reference TEXT]
//	stompwatch verify-dsp   -config FILE
//	stompwatch probe-camera -config FILE [-host HOST] [-ffmpeg PATH]
//	stompwatch reset        -config FILE [-yes] [-reviewed]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/clip"
	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/dsp"
	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/logs"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/pipeline"
	"github.com/minayousseif/stompwatch/internal/retention"
	"github.com/minayousseif/stompwatch/internal/settings"
	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/tailnet"
	"github.com/minayousseif/stompwatch/internal/verify"
	"github.com/minayousseif/stompwatch/internal/video"
	"github.com/minayousseif/stompwatch/internal/web"
)

const usage = `usage: stompwatch <command> [flags]

commands:
  run         measure, detect, and record until stopped
  calibrate   read a 94 dB SPL calibrator tone and print the sensitivity block
  verify-dsp  check the measurement chain and print a report
  probe-camera
              try the camera's RTSP paths and print which works
  reset       remove the recorded data and keep the configuration

Run "stompwatch <command> -h" for the flags of a command.
`

const defaultConfig = "/etc/stompwatch/stompwatch.conf"

// clock is what the commands read the time from. It is a variable so a test
// can fix the day that stompwatch calibrate stamps on the block it prints.
var clock = time.Now

// logFileName is the log file inside log_dir. The dashboard reads it back
// under the same name.
const logFileName = "stompwatch.log"

func main() { os.Exit(run(os.Args, os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[1] {
	case "run":
		return cmdRun(args[2:], stderr)
	case "calibrate":
		return cmdCalibrate(args[2:], stdout, stderr)
	case "verify-dsp":
		return cmdVerify(args[2:], stdout, stderr)
	case "probe-camera":
		return cmdProbeCamera(args[2:], stdout, stderr)
	case "reset":
		return cmdReset(args[2:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "stompwatch: unknown command %q\n\n%s", args[1], usage)
	return 2
}

func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("verify-dsp", flag.ContinueOnError)
	fl.SetOutput(stderr)
	cfgPath := fl.String("config", defaultConfig, "config file")
	if fl.Parse(args) != nil {
		return 2
	}
	s, cal, _, err := loadSettings(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}
	if verify.Report(stdout, verify.Run(s, cal)) > 0 {
		return 1
	}
	return 0
}

func cmdCalibrate(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fl.SetOutput(stderr)
	cfgPath := fl.String("config", defaultConfig, "config file")
	input := fl.String("input", "", "read a 48 kHz 24-bit stereo WAV file instead of the microphone")
	seconds := fl.Int("seconds", 10, "seconds to record from the microphone")
	reference := fl.String("reference", "",
		"what the microphone was measured against, such as \"B and K 4231 at 94 dB SPL\"")
	if fl.Parse(args) != nil {
		return 2
	}
	if strings.ContainsAny(*reference, "\r\n") {
		fmt.Fprint(stderr, "stompwatch: -reference must be one line\n")
		return 2
	}
	s, cal, _, err := loadSettings(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}

	var samples []float64
	if *input != "" {
		samples, err = readWAV(*input, s.CaptureChannel)
	} else {
		if _, err = checkGain(context.Background(), s); err == nil {
			samples, err = recordSeconds(context.Background(), s, *seconds)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}
	if len(samples) < 2*audio.SampleRate {
		fmt.Fprintf(stderr, "stompwatch: need at least 2 s of audio, got %.1f s\n", float64(len(samples))/audio.SampleRate)
		return 1
	}

	level := verify.CalibratorLevel(samples, cal)
	fmt.Fprintf(stdout, "Calibrator tone level: %.2f dBFS (AES17, after the calibration file).\n", level)
	fmt.Fprintf(stdout, "With the current sensitivity_dbfs of %.2f, it reads %.2f dB SPL.\n",
		s.SensitivityDBFS, 94+level-s.SensitivityDBFS)

	// The whole block, not the one line. A pasted sensitivity_dbfs on its
	// own would leave sensitivity_source saying datasheet beside a figure
	// that was measured, and then the instrument would report an
	// uncertainty of plus or minus 2 dB it no longer has
	// (SPEC.md section 15 decision 23).
	fmt.Fprint(stdout, "For a 94 dB SPL calibrator, set all four of these in the config file:\n\n")
	fmt.Fprintf(stdout, "sensitivity_dbfs = %.2f\n", level)
	fmt.Fprintf(stdout, "sensitivity_source = %s\n", config.SensitivityMeasured)
	fmt.Fprintf(stdout, "sensitivity_measured_on = %s\n", clock().Format("2006-01-02"))
	fmt.Fprintf(stdout, "sensitivity_reference = %s\n", *reference)
	if *reference == "" {
		fmt.Fprint(stdout, "\nWrite on sensitivity_reference what you held against the microphone, "+
			"such as \"B and K 4231 at 94 dB SPL\". Nothing else records what this figure was\n"+
			"measured against. Run calibrate again with -reference to have the line filled in.\n")
	}
	fmt.Fprint(stdout, "\nSet sensitivity_uncertainty_db as well, to how well you trust the reference. "+
		"It is\nstill 2.0, the datasheet's per-unit tolerance, which a measured figure no longer carries.\n")
	return 0
}

func cmdRun(args []string, stderr io.Writer) int {
	fl := flag.NewFlagSet("run", flag.ContinueOnError)
	fl.SetOutput(stderr)
	cfgPath := fl.String("config", defaultConfig, "config file")
	input := fl.String("input", "", "process a 48 kHz 24-bit stereo WAV file instead of the microphone, then stop")
	ffmpeg := fl.String("ffmpeg", "", "ffmpeg program for video; empty means ffmpeg from PATH")
	if fl.Parse(args) != nil {
		return 2
	}
	s, cal, calDesc, err := loadSettings(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}

	// The camera login is read once, here, before any child process starts,
	// and then taken out of the environment. systemd puts CAMERA_USER and
	// CAMERA_PASS there with EnvironmentFile=, and amixer, arecord, ffmpeg
	// and tailscale would all inherit the password. The login is read
	// whether or not a camera is set up: the dashboard shows it while the
	// owner brings one up. The password inside goes to ffmpeg's URL only.
	login := video.DescribeLogin(os.Getenv, s.CameraCredentialsFile)
	video.ForgetEnvLogin()

	// One collector per data directory. Two would both write the database and
	// both cut clips into the same directory, and the measurement would be
	// the interleaving of two of them. The lock is held until this function
	// returns, so stompwatch reset cannot remove the data under a live
	// collector either.
	lock, err := lockData(s.DBPath)
	if err != nil {
		if errors.Is(err, errLocked) {
			fmt.Fprintf(stderr, "stompwatch: another stompwatch is already running with this data; it holds %s.\n"+
				"Only one may run at a time.\n", lockPath(s.DBPath))
			return 1
		}
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}
	defer lock.release()

	// The log goes to the journal and to a file on the data disk, so the
	// dashboard can show it. A log file that will not open is a warning and
	// nothing more: the collector must never fail to measure because it
	// cannot write a log line (SPEC.md section 9.0.1 says the same about the
	// network).
	out := io.Writer(stderr)
	logFile, logErr := logs.NewWriter(logs.Config{
		Dir: s.LogDir, Name: logFileName, MaxBytes: s.LogFileMB << 20, Keep: s.LogFiles,
	})
	if logErr == nil {
		defer logFile.Close()
		out = io.MultiWriter(stderr, logFile)
	}
	log := slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if logErr != nil {
		log.Warn("cannot write the log file; the journal is the only copy", "dir", s.LogDir, "err", logErr)
	}

	status := health.NewStatus(time.Now())
	hb, err := health.NewHeartbeat(health.HeartbeatConfig{
		URL:      s.HeartbeatURL,
		Interval: s.HeartbeatInterval,
		Check: func(now time.Time) error {
			return status.Check(now, time.Duration(float64(s.HeartbeatInterval)*s.HeartbeatStallFactor), 10*time.Second)
		},
		OnSkip:  func(err error) { log.Error("heartbeat withheld: collection has stalled", "reason", err) },
		OnError: func(err error) { log.Warn("heartbeat ping failed", "err", err) },
	})
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}
	alert := newAlerter(hb, log)
	fatal := func(msg string, err error) int {
		log.Error(msg, "err", err)
		fmt.Fprintf(stderr, "stompwatch: %s: %v\n", msg, err)
		alert.now(msg + ": " + err.Error())
		return 1
	}

	log.Info("starting", "config", *cfgPath, "device", s.CaptureDevice, "channel", s.CaptureChannel,
		"sensitivity_dbfs", s.SensitivityDBFS, "calibration", calDesc.String(), "input", *input)
	if _, ok := cal.(meter.NoCalibration); ok {
		log.Warn("no calibration file; levels are not corrected for the mic's response")
	}
	// The owner must never be able to forget which mode the box is in. A
	// clip filter above the speech-safe cutoff keeps more than impact noise,
	// and that
	// is a privacy decision, not a tuning knob (SPEC.md section 15 decision 18).
	if s.ClipLowpassHz > dsp.SpeechSafeClipLowpassHz {
		log.Warn("the clip filter is above the speech-safe cutoff, so clips hold more than impact noise",
			"clip_lowpass_hz", s.ClipLowpassHz,
			"speech_safe_hz", dsp.SpeechSafeClipLowpassHz,
			"stored_rate_hz", 2*s.ClipLowpassHz,
			"effect", fmt.Sprintf("clips now hold sound up to %g Hz, so speech in them may be partly recoverable", s.ClipLowpassHz))
	}

	// The camera's own microphone is full bandwidth and uncalibrated, so its
	// track is not a measurement; it is a recording of the room, and it
	// holds speech in clear. That is a privacy decision, not a tuning knob
	// (SPEC.md section 15 decision 22).
	if s.CameraAudio != video.SpeechSafeCameraAudio {
		log.Warn("camera_audio is on, so the camera's own microphone is recorded",
			"camera_audio", s.CameraAudio,
			"effect", "the camera records speech in clear, unfiltered",
			"audio_clips", fmt.Sprintf("unaffected: the microphone's own clips stay filtered at %g Hz", s.ClipLowpassHz),
			"applies_to", "video recorded from now on; clips already on disk keep what they hold")
	}

	// The key is still parsed, so an old config file loads, but nothing is
	// built behind it. A setting that silently does nothing is a trap.
	if s.VideoMainOnEvent {
		log.Warn("video_main_on_event is true, but it is not built and does nothing: no main-stream clip is kept",
			"video_main_on_event", true)
	}

	for _, dir := range []string{filepath.Dir(s.DBPath), s.ClipDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fatal("cannot create data directory", err)
		}
		free, err := health.FreeMB(dir)
		if err != nil {
			return fatal("cannot read free space", err)
		}
		if free < s.DiskMinFreeMB {
			return fatal("not enough free space", fmt.Errorf("%s has %d MB free, below disk_min_free_mb = %d", dir, free, s.DiskMinFreeMB))
		}
		if free < s.DiskWarnFreeMB {
			log.Warn("free space is low", "dir", dir, "free_mb", free, "warn_below_mb", s.DiskWarnFreeMB)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gainDesc := "not read; the input is a file"
	if *input == "" {
		g, err := checkGain(ctx, s)
		if err != nil {
			return fatal("capture gain check failed", err)
		}
		gainDesc = g.String()
		log.Info("capture gain", "gain", gainDesc)
		if err := logChannelLevels(ctx, s, log); err != nil {
			return fatal("test recording failed", err)
		}
	}

	st, err := store.Open(s.DBPath)
	if err != nil {
		return fatal("cannot open database", err)
	}
	defer st.Close()
	rec, err := clip.NewRecorder(s.ClipDir, clip.BufferLength, s.ClipLowpassHz)
	if err != nil {
		return fatal("cannot set up clips", err)
	}

	// The dashboard writes its changes to the config table. They come back
	// here, on top of the config file.
	live, dropped := liveSettings(ctx, st, s, log)
	if live == nil {
		return fatal("cannot use the settings", dropped)
	}
	s = keepFilePaths(live.Current(), s, log)

	// What the instrument is, written down, so that every event recorded
	// from here on can cite the settings it was measured under rather than
	// the ones in force the day somebody reprints it
	// (SPEC.md section 15 decision 23). It goes after liveSettings, because
	// the row has to say what the collector is really running with: the
	// config file with the stored overrides merged on and the whole thing
	// validated. A row that could not be written is a warning, not a reason
	// to stop measuring.
	recordCaptureSettings(ctx, st, s, calDesc, log)

	// The camera is set up before the pipeline because the pipeline cuts
	// the clips, and its health rows go through the pipeline once it runs.
	// A camera that cannot be set up is reported and left alone.
	var p *pipeline.Pipeline
	var pending []func()
	record := func(at time.Time, kind, detail string, d time.Duration) {
		if p == nil {
			// Setup runs before the pipeline exists; its rows wait for it.
			pending = append(pending, func() { p.RecordHealth(at, kind, detail, d) })
			return
		}
		p.RecordHealth(at, kind, detail, d)
	}
	cam := setupVideo(s, login, *ffmpeg, log, record, newAlerter(hb, log),
		func(t time.Time) bool { return live.Current().RecordingPause.Contains(t) })

	// The dashboard shows the camera login and the ffmpeg version whether
	// or not a camera is set up: the owner reads them while bringing one
	// up. Neither reading ever carries the password.
	ffmpegCmd := ffmpegCommand(*ffmpeg)
	ffmpegVersion := video.Version(ffmpegCmd, nil)

	chunks := make(chan audio.Chunk, 64)
	feed := web.NewLiveFeed()
	pcfg := pipeline.Config{
		Settings: s, Store: st, Recorder: rec, Calibration: cal,
		Chunks: chunks, Status: status, Log: log, Alert: alert.now,
		Publish: feed.Publish,
	}
	if cam != nil {
		pcfg.Video = cam.writer
	}
	p, err = pipeline.New(pcfg)
	if err != nil {
		return fatal("cannot set up pipeline", err)
	}
	for _, fn := range pending {
		fn()
	}
	// A settings change from the dashboard reaches the running detector.
	live.OnChange(p.Reload)
	if dropped != nil {
		p.RecordHealth(time.Now(), store.HealthSettingsChange,
			"a stored setting was dropped at startup: "+dropped.Error(), 0)
	}

	var sourceErr error
	var wg sync.WaitGroup
	wg.Add(1)
	if *input != "" {
		src, err := audio.NewFileSource(*input, s.CaptureChannel, time.Now().Truncate(time.Second), chunks)
		if err != nil {
			return fatal("cannot read input", err)
		}
		go func() {
			defer wg.Done()
			sourceErr = src.Run(ctx)
			close(chunks)
		}()
	} else {
		capture, err := audio.NewCapture(audio.CaptureConfig{
			Device:     s.CaptureDevice,
			Channel:    s.CaptureChannel,
			Out:        chunks,
			StuckLimit: s.StuckLimit,
			OnEvent:    captureEvents(p, log, alert, s),
		})
		if err != nil {
			return fatal("cannot set up capture", err)
		}
		go func() {
			defer wg.Done()
			sourceErr = capture.Run(ctx)
			close(chunks)
		}()
	}

	tasks, stopTasks := context.WithCancel(ctx)
	defer stopTasks()
	serveDashboard(tasks, dashboard{
		settings: s, live: live, store: st, status: status, feed: feed,
		pipe: p, gain: gainDesc, calibration: calDesc, log: log, alert: alert, camera: cam,
		ffmpeg: ffmpegCmd, ffmpegVersion: ffmpegVersion, login: login,
		wg: &wg, record: p.RecordHealth,
	})
	if cam != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cam.ring.Run(tasks)
		}()
		go func() {
			select {
			case <-tasks.Done():
				return
			case <-time.After(clockCheckDelay):
				cam.checkClocks(tasks, log, p.RecordHealth)
			}
			every(tasks, clockCheckEvery, func() { cam.checkClocks(tasks, log, p.RecordHealth) })
		}()
	}
	go hb.Run(tasks)
	if *input == "" {
		go every(tasks, 10*time.Minute, func() { recheckGain(tasks, s, p, log, alert) })
	}
	go every(tasks, time.Hour, func() { checkDisk(s, p, log, alert) })
	go every(tasks, 10*time.Minute, func() { maintainWAL(tasks, st, log) })
	go daily(tasks, 4, func() { nightlySnapshot(tasks, s, st, p, log, alert) })
	// Retention is in the WaitGroup: a run that is deleting files when the
	// collector stops must be allowed to finish recording what it deleted,
	// or the database is left claiming files that are gone.
	wg.Add(1)
	go func() {
		defer wg.Done()
		runRetention(tasks, s, live, st, log)
	}()

	runErr := p.Run(ctx)
	stopTasks()
	wg.Wait()

	stats := p.Stats()
	log.Info("stopped", "chunks", stats.Chunks, "dropped_samples", stats.DroppedSamples,
		"bins_dropped", stats.BinsDropped, "events_dropped", stats.EventsDropped,
		"write_failures", stats.WriteFailures, "loop_restarts", stats.LoopRestarts)
	if sourceErr != nil && !errors.Is(sourceErr, context.Canceled) {
		return fatal("audio input failed", sourceErr)
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fatal("pipeline failed", runErr)
	}
	return 0
}

// recordCaptureSettings adds a row to the capture-settings history when the
// settings in force differ from the newest row, and says which of the two
// happened. A restart that changes nothing adds nothing.
func recordCaptureSettings(ctx context.Context, st *store.Store, s config.Config, cal calInfo, log *slog.Logger) {
	wrote, err := st.RecordCaptureSettings(ctx, store.CaptureSettings{
		From:            time.Now(),
		SensitivityDBFS: s.SensitivityDBFS,
		UncertaintyDB:   s.SensitivityUncertaintyDB,
		Source:          s.SensitivitySource,
		MeasuredOn:      s.SensitivityMeasuredOn,
		Reference:       s.SensitivityReference,
		// The name, never the path.
		CalibrationFile:     cal.Name,
		CalibrationOffsetDB: cal.OffsetDB,
		CaptureDevice:       s.CaptureDevice,
		CaptureChannel:      s.CaptureChannel,
	})
	switch {
	case err != nil:
		// The measurement is the product. An event that cannot cite its
		// settings is worse than one that can, and far better than none.
		log.Error("cannot record the capture settings; events recorded now will not cite them", "err", err)
	case wrote:
		log.Info("the capture settings changed, and the change is recorded",
			"sensitivity_dbfs", s.SensitivityDBFS, "uncertainty_db", s.SensitivityUncertaintyDB,
			"source", s.SensitivitySource, "calibration", cal.String())
	default:
		log.Info("the capture settings are unchanged since the last start")
	}
}

// liveSettings merges the settings stored in the config table onto the
// settings from the file. It returns the holder the dashboard edits, and the
// problem it had to drop to get there.
//
// A stored setting that does not work is reported and dropped, never a reason
// to stop. Refusing to start because of a value typed into a web form at 2am
// is the wrong failure: the owner can fix it from the dashboard or the file,
// and the collector keeps measuring meanwhile.
func liveSettings(ctx context.Context, st *store.Store, s config.Config, log *slog.Logger) (*settings.Live, error) {
	dropped := error(nil)
	overrides, err := st.Settings(ctx)
	if err == nil {
		// A key only the config file may set is dropped on its own, so the
		// rest of the stored settings still apply. The row is deleted, so it
		// is reported once, not at every start.
		if keys := settings.DropFileOnly(overrides); len(keys) > 0 {
			dropped = fmt.Errorf("%s may be set in the config file only; the stored value was deleted",
				strings.Join(keys, " and "))
			log.Error("a stored setting was dropped", "err", dropped)
			gone := make(map[string]*string, len(keys))
			for _, k := range keys {
				gone[k] = nil
			}
			if err := st.PutSettings(ctx, gone, time.Now()); err != nil {
				log.Error("cannot delete the dropped settings; they will be dropped again at the next start",
					"err", err)
			}
		}
	}
	if err != nil {
		dropped = err
		log.Error("cannot read the stored settings; the config file alone is in force", "err", err)
	} else if live, err := settings.New(s, overrides); err == nil {
		return live, dropped
	} else {
		dropped = errors.Join(dropped, err)
		log.Error("a stored setting is not usable; the config file alone is in force", "err", err)
	}
	live, err := settings.New(s, nil)
	if err != nil {
		return nil, err // the file settings already validated, so this cannot happen
	}
	return live, dropped
}

// keepFilePaths puts the paths from the config file back into the merged
// settings. The database, the clips, and the log are already open on those
// paths, and a row in the config table must not make the program disagree
// with itself about where its data is.
func keepFilePaths(merged, file config.Config, log *slog.Logger) config.Config {
	if merged.DBPath != file.DBPath || merged.ClipDir != file.ClipDir || merged.LogDir != file.LogDir {
		log.Warn("stored path settings are ignored; the paths come from the config file",
			"db_path", file.DBPath, "clip_dir", file.ClipDir, "log_dir", file.LogDir)
	}
	kept := merged
	kept.DBPath, kept.ClipDir, kept.LogDir = file.DBPath, file.ClipDir, file.LogDir
	return kept
}

// dashboard is what the web server needs from the running collector.
type dashboard struct {
	settings    config.Config
	live        *settings.Live
	store       *store.Store
	status      *health.Status
	feed        *web.LiveFeed
	pipe        *pipeline.Pipeline
	gain        string
	calibration calInfo
	log         *slog.Logger
	alert       *alerter
	camera      *camera // nil when video is off
	// wg holds the goroutines the dashboard starts that the collector waits
	// for on the way out. The Tailscale reading is one of them.
	wg *sync.WaitGroup
	// record writes a system_health row.
	record func(at time.Time, kind, detail string, d time.Duration)
	// The camera login and ffmpeg, read whether or not video is on. login
	// holds the password, and only the stream URL the probe hands ffmpeg
	// ever reads it.
	ffmpeg        video.Command
	ffmpegVersion string
	login         video.Login
}

// videoDir is where the dashboard serves video clips from, or empty with
// video off.
func (d dashboard) videoDir() string {
	if d.camera == nil {
		return ""
	}
	return d.camera.dir
}

// cameraStatus reads the ring for the system page, or is nil with video off.
func (d dashboard) cameraStatus() func() web.CameraStatus {
	if d.camera == nil {
		return nil
	}
	return func() web.CameraStatus {
		st := d.camera.ring.Status()
		return web.CameraStatus{
			Enabled: true, SubPath: st.Path,
			Connected: st.Connected, ConnectedSince: st.ConnectedSince,
			Uptime: st.Uptime, Disconnects: st.Disconnects, LastSegment: st.LastSegment,
			RingSegments: st.Segments, RingBytes: st.Bytes,
			// What the camera answered at start when it was asked about
			// audio, or nothing known when it was never asked.
			Audio: d.camera.audio, AudioKnown: d.camera.audioKnown,
		}
	}
}

// serveDashboard starts the web server in its own goroutine. A dashboard that
// will not start is reported and left alone: measurement is the product, and
// the dashboard is only how it is read.
func serveDashboard(ctx context.Context, d dashboard) {
	s := d.settings
	if s.HTTPAddr == "" {
		d.log.Info("the dashboard is switched off; http_addr is empty")
		return
	}
	d.log.Info("the dashboard", "http_addr", s.HTTPAddr, "auth_mode", s.AuthMode)
	if !s.LoopbackHTTPAddr() {
		d.log.Warn("http_addr is not a loopback address: the dashboard is reachable from the network, "+
			"and only the network authenticates the caller",
			"http_addr", s.HTTPAddr, "auth_mode", s.AuthMode)
	}

	srv, err := web.New(web.Config{
		Store:    d.store,
		Settings: d.live,
		Status:   d.status,
		Live:     d.feed,
		Stats: func() web.Stats {
			st := d.pipe.Stats()
			return web.Stats{
				Chunks: st.Chunks, DroppedSamples: st.DroppedSamples, BinsDropped: st.BinsDropped,
				EventsDropped: st.EventsDropped, WriteFailures: st.WriteFailures, LoopRestarts: st.LoopRestarts,
			}
		},
		Capture: web.Capture{
			Device: s.CaptureDevice, Channel: s.CaptureChannel, SensitivityDBFS: s.SensitivityDBFS,
			Gain: d.gain, Calibration: d.calibration.String(),
		},
		Started:       time.Now(),
		LogDir:        s.LogDir,
		LogName:       logFileName,
		ClipDir:       s.ClipDir,
		VideoDir:      d.videoDir(),
		Camera:        d.cameraStatus(),
		CameraLogin:   func() video.Login { return d.login },
		FFmpegVersion: d.ffmpegVersion,
		FFmpeg:        d.ffmpeg,
		Log:           d.log,
		RecordHealth:  d.pipe.RecordHealth,
		// The dashboard reports Tailscale and never configures it. The web
		// package caches the reading, so this runs at most once a minute.
		Tailscale: func(ctx context.Context) tailnet.Status {
			return tailnet.Read(ctx, s.HTTPAddr)
		},
	})
	if err != nil {
		d.log.Error("the dashboard did not start; the collector carries on measuring", "err", err)
		d.alert.now("the dashboard did not start: " + err.Error())
		return
	}
	go func() {
		if err := srv.Run(ctx); err != nil {
			d.log.Error("the dashboard did not start; the collector carries on measuring", "err", err)
			d.alert.now("the dashboard did not start: " + err.Error())
		}
	}()

	// What Tailscale is doing with the dashboard, once, after the listening
	// line above. It runs in a goroutine because shelling out twice must
	// not hold the measurement up, and on a context of its own rather than
	// ctx so that a shutdown in the first seconds does not cut the report
	// off half-read. tailnet.Budget bounds it either way.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		rctx, cancel := context.WithTimeout(context.Background(), tailnet.Budget)
		defer cancel()
		reportTailscale(tailnet.Read(rctx, s.HTTPAddr), s.HTTPAddr, d.log, d.record, d.alert.now)
	}()
}

// calInfo is the calibration file, said in a way that may leave this box.
// Name is the base name only, never the path: no response names a place on
// disk (SPEC.md section 3). Name is empty when no calibration file is
// configured, and then OffsetDB is 0.
type calInfo struct {
	Name     string
	OffsetDB float64
}

// String is what the dashboard and the log show.
func (c calInfo) String() string {
	if c.Name == "" {
		return "none"
	}
	return fmt.Sprintf("%s (correction %.2f dB)", c.Name, c.OffsetDB)
}

// loadSettings reads the config file and the calibration file it names.
func loadSettings(path string) (config.Config, meter.Calibration, calInfo, error) {
	s, err := config.Load(path)
	if err != nil {
		return config.Config{}, nil, calInfo{}, err
	}
	if s.CalibrationFile == "" {
		return s, meter.NoCalibration{}, calInfo{}, nil
	}
	f, err := os.Open(s.CalibrationFile)
	if err != nil {
		return config.Config{}, nil, calInfo{}, fmt.Errorf("calibration file: %w", err)
	}
	defer f.Close()
	points, err := meter.ParseREW(f)
	if err != nil {
		return config.Config{}, nil, calInfo{}, fmt.Errorf("%s: %w", s.CalibrationFile, err)
	}
	cal, err := meter.NewScalarCalibration(points)
	if err != nil {
		return config.Config{}, nil, calInfo{}, fmt.Errorf("%s: %w", s.CalibrationFile, err)
	}
	return s, cal, calInfo{Name: filepath.Base(s.CalibrationFile), OffsetDB: cal.OffsetDB()}, nil
}

// checkGain reads the mixer with amixer and compares the capture gain with
// expected_capture_gain.
func checkGain(ctx context.Context, s config.Config) (audio.CaptureGain, error) {
	out, runErr := exec.CommandContext(ctx, "amixer", "-c", s.MixerCard, "sget", s.MixerControl).CombinedOutput()
	g, err := audio.CheckGain(string(out), s.ExpectedCaptureGain)
	if err != nil && runErr != nil {
		return g, fmt.Errorf("%w (amixer: %v: %s)", err, runErr, out)
	}
	return g, err
}

// recordSeconds records from the capture device with arecord and returns
// the configured channel.
func recordSeconds(ctx context.Context, s config.Config, seconds int) ([]float64, error) {
	raw, err := recordRaw(ctx, s, seconds)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(raw)/(audio.Channels*3))
	n := audio.DecodeS24LE3(raw, audio.Channels, s.CaptureChannel, out)
	return out[:n], nil
}

func recordRaw(ctx context.Context, s config.Config, seconds int) ([]byte, error) {
	if err := audio.ValidateDevice(s.CaptureDevice); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds+10)*time.Second)
	defer cancel()
	args := append(audio.Args(s.CaptureDevice), "-d", strconv.Itoa(seconds))
	cmd := exec.CommandContext(ctx, "arecord", args...)
	var errOut limitedBuffer
	cmd.Stderr = &errOut
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("arecord: %v: %s", err, errOut.String())
	}
	return raw, nil
}

// logChannelLevels records one second and logs the level of each channel, so
// a wrong capture_channel is visible at once.
func logChannelLevels(ctx context.Context, s config.Config, log *slog.Logger) error {
	raw, err := recordRaw(ctx, s, 1)
	if err != nil {
		return err
	}
	rms := audio.ChannelRMS(raw, audio.Channels)
	levels := make([]any, 0, 2*len(rms))
	for c, v := range rms {
		levels = append(levels, fmt.Sprintf("channel_%d_dbfs", c), aes17(v))
	}
	log.Info("channel levels", levels...)
	other := 1 - s.CaptureChannel
	if rms[s.CaptureChannel] == 0 && rms[other] > 0 {
		log.Error("the selected capture channel is silent but the other is not; check capture_channel",
			"capture_channel", s.CaptureChannel)
	}
	return nil
}

func aes17(rms float64) float64 {
	if rms <= 0 {
		return math.Inf(-1)
	}
	return 20*math.Log10(rms) + 10*math.Log10(2)
}

// readWAV reads a whole WAV file through the file source.
func readWAV(path string, channel int) ([]float64, error) {
	ch := make(chan audio.Chunk, 16)
	src, err := audio.NewFileSource(path, channel, time.Time{}, ch)
	if err != nil {
		return nil, err
	}
	errc := make(chan error, 1)
	go func() { errc <- src.Run(context.Background()); close(ch) }()
	var out []float64
	for c := range ch {
		out = append(out, c.Samples...)
	}
	return out, <-errc
}

func captureEvents(p *pipeline.Pipeline, log *slog.Logger, alert *alerter, s config.Config) func(audio.CaptureEvent) {
	return func(e audio.CaptureEvent) {
		switch e.Kind {
		case audio.CaptureStarted:
			log.Info("capture started", "stream", e.Stream, "gap", e.Gap)
			if e.Stream > 1 {
				p.RecordHealth(e.At, store.HealthCaptureGap,
					fmt.Sprintf("no audio between stream %d and stream %d", e.Stream-1, e.Stream), e.Gap)
			}
		case audio.CaptureExited:
			log.Warn("capture stopped; restarting", "stream", e.Stream, "detail", e.Detail)
			p.RecordHealth(e.At, store.HealthCaptureGap, fmt.Sprintf("arecord stream %d ended: %s", e.Stream, e.Detail), 0)
		case audio.CaptureStuck:
			log.Error("audio stuck; restarting capture", "stream", e.Stream, "detail", e.Detail)
			p.RecordHealth(e.At, store.HealthStuckStream, e.Detail, s.StuckLimit)
			alert.limited("the microphone audio is stuck: " + e.Detail)
		}
	}
}

func recheckGain(ctx context.Context, s config.Config, p *pipeline.Pipeline, log *slog.Logger, alert *alerter) {
	if _, err := checkGain(ctx, s); err != nil {
		log.Error("capture gain changed", "err", err)
		p.RecordHealth(time.Now(), store.HealthGainChange, err.Error(), 0)
		alert.limited("capture gain check failed: " + err.Error())
	}
}

func checkDisk(s config.Config, p *pipeline.Pipeline, log *slog.Logger, alert *alerter) {
	for _, dir := range []string{filepath.Dir(s.DBPath), s.ClipDir} {
		free, err := health.FreeMB(dir)
		switch {
		case err != nil:
			log.Error("cannot read free space", "dir", dir, "err", err)
		case free < s.DiskMinFreeMB:
			msg := fmt.Sprintf("%s has %d MB free, below disk_min_free_mb = %d", dir, free, s.DiskMinFreeMB)
			log.Error("free space below floor", "dir", dir, "free_mb", free)
			p.RecordHealth(time.Now(), store.HealthDiskLow, msg, 0)
			alert.limited(msg)
		case free < s.DiskWarnFreeMB:
			log.Warn("free space is low", "dir", dir, "free_mb", free)
			p.RecordHealth(time.Now(), store.HealthDiskLow, fmt.Sprintf("%s has %d MB free", dir, free), 0)
		}
	}
}

func maintainWAL(ctx context.Context, st *store.Store, log *slog.Logger) {
	if size, err := st.WALSize(); err != nil {
		log.Warn("cannot read WAL size", "err", err)
	} else if size > store.WALSizeWarning {
		log.Warn("WAL file is large; a reader may be holding an old snapshot", "bytes", size)
	}
	if err := st.Checkpoint(ctx); err != nil {
		log.Warn("WAL checkpoint did not finish", "err", err)
	}
}

// nightlySnapshot checks the database and writes a dated copy with its
// SHA-256 (SPEC.md section 8).
func nightlySnapshot(ctx context.Context, s config.Config, st *store.Store, p *pipeline.Pipeline, log *slog.Logger, alert *alerter) {
	if err := st.IntegrityCheck(ctx); err != nil {
		log.Error("database integrity check failed", "err", err)
		p.RecordHealth(time.Now(), store.HealthWriteError, err.Error(), 0)
		alert.now("database integrity check failed: " + err.Error())
		return
	}
	dir := snapshotDir(s.DBPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		log.Error("cannot create snapshot directory", "err", err)
		return
	}
	path := filepath.Join(dir, "noise-"+time.Now().Format("2006-01-02")+".sqlite")
	sum, err := st.Snapshot(ctx, path)
	if err != nil {
		log.Error("snapshot failed", "path", path, "err", err)
		return
	}
	log.Info("snapshot written", "path", path, "sha256", sum)
	maintainWAL(ctx, st, log)
}

// snapshotDir is where the dated database copies go. stompwatch reset clears
// it, so both ends read the path from one place.
func snapshotDir(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "snapshots") }

// retentionDelay is how long after start the first retention run is, and
// retentionHour is the local hour of the daily run after that. It is the
// hour before the snapshot, so the night's purge rows are in the night's
// copy.
const (
	retentionDelay = 5 * time.Minute
	retentionHour  = 3
)

// runRetention deletes the clip files of old events: once shortly after
// start, then daily. It runs until ctx is done, and the caller waits for
// it: a run stopped part way still has files to record, and it writes its
// own health row through the store rather than through the pipeline, which
// has already stopped by then.
//
// The job reads retention_days from the live settings at each run, so a
// change from the dashboard applies at the next run. The video root is
// video_dir from the config whether or not a camera is on: video is what
// fills the disk, and a camera that was switched off later must not leave
// its old clips there forever. The containment check applies either way.
func runRetention(ctx context.Context, s config.Config, live *settings.Live, st *store.Store, log *slog.Logger) {
	job := &retention.Job{
		Store: st, ClipDir: s.ClipDir, VideoDir: s.VideoDir,
		Days: func() int { return live.Current().RetentionDays },
		Log:  log,
	}
	if days := job.Days(); days > 0 {
		log.Info("retention", "days", days, "first_run_in", retentionDelay.String(),
			"effect", "the audio and video files of older events are deleted; the events and their measurements stay")
	} else {
		log.Info("retention is off; retention_days = 0 keeps every recording forever")
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(retentionDelay):
		job.Run(ctx)
	}
	daily(ctx, retentionHour, func() { job.Run(ctx) })
}

// every runs fn every d until ctx is done.
func every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

// daily runs fn once a day at hour:00 local time until ctx is done.
func daily(ctx context.Context, hour int, fn func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(nextDaily(time.Now(), hour))):
			fn()
		}
	}
}

// nextDaily returns the next time after now at hour:00 in now's location.
// time.Date handles month ends and daylight saving changes.
func nextDaily(now time.Time, hour int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, hour, 0, 0, 0, now.Location())
	}
	return next
}

// alerter sends heartbeat failure pings. limited sends at most one every
// ten minutes, for conditions that can repeat.
type alerter struct {
	hb   *health.Heartbeat
	log  *slog.Logger
	mu   sync.Mutex
	last time.Time
}

func newAlerter(hb *health.Heartbeat, log *slog.Logger) *alerter { return &alerter{hb: hb, log: log} }

func (a *alerter) now(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.hb.Fail(ctx, reason); err != nil {
		a.log.Warn("failure ping not sent", "err", err)
	}
}

func (a *alerter) limited(reason string) {
	a.mu.Lock()
	if time.Since(a.last) < 10*time.Minute {
		a.mu.Unlock()
		return
	}
	a.last = time.Now()
	a.mu.Unlock()
	a.now(reason)
}

// limitedBuffer keeps the first 4 KB written to it.
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - len(l.b); room > 0 {
		l.b = append(l.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b) }
