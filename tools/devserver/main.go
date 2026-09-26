// Command devserver fills a throwaway database with believable measurements
// and serves the dashboard API from it. It exists so the web interface can be
// built and tried without a microphone, and it is never part of the shipped
// binary.
//
//	go run ./tools/devserver            # 127.0.0.1:8080, data in .dev/
//	go run ./tools/devserver -fresh     # throw the data away and build it again
//	go run ./tools/devserver -camera no-login   # a camera with no login loaded
//	go run ./tools/devserver -camera no-ffmpeg  # a box without ffmpeg
//	go run ./tools/devserver -camera off        # no camera at all
//	go run ./tools/devserver -tailscale funnel  # the Funnel fault
//	go run ./tools/devserver -tailscale not-serving | expiring | stopped | off
//
// The numbers are synthetic. Do not read anything into them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/dsp"
	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/settings"
	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/tailnet"
	"github.com/minayousseif/stompwatch/internal/testsignal"
	"github.com/minayousseif/stompwatch/internal/video"
	"github.com/minayousseif/stompwatch/internal/web"

	"github.com/minayousseif/stompwatch/internal/clip"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	dir := flag.String("dir", ".dev", "directory for the throwaway database and clips")
	days := flag.Int("days", 3, "days of history to invent")
	fresh := flag.Bool("fresh", false, "delete the directory first")
	camera := flag.String("camera", "on", "camera to invent: on, no-login, no-ffmpeg, or off")
	tailscale := flag.String("tailscale", "serving", "Tailscale to invent: serving, not-serving, expiring, funnel, stopped, or off")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(*addr, *dir, *days, *fresh, *camera, *tailscale, log); err != nil {
		fmt.Fprintf(os.Stderr, "devserver: %v\n", err)
		os.Exit(1)
	}
}

func run(addr, dir string, days int, fresh bool, camera, tailscale string, log *slog.Logger) error {
	if fresh {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}

	// The clip recorder stores the full path it wrote to, so the directory
	// must be absolute or the server cannot find a clip again.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	clipDir := filepath.Join(dir, "clips", "audio")
	logDir := filepath.Join(dir, "logs")
	for _, d := range []string{dir, clipDir, logDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}

	dbPath := filepath.Join(dir, "noise.db")
	_, statErr := os.Stat(dbPath)
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if os.IsNotExist(statErr) {
		log.Info("inventing measurements", "days", days)
		if err := seed(context.Background(), st, clipDir, days, log); err != nil {
			return err
		}
	}

	file := config.Default()
	file.DBPath, file.ClipDir, file.LogDir = dbPath, clipDir, logDir
	file.HTTPAddr, file.AuthMode = addr, config.AuthNone
	file.ExpectedCaptureGain = "none"
	// The measured shape of the levels block, so the System screen can be
	// built and read in the state the owner reaches after calibrating.
	file.SensitivityDBFS = -12.47
	file.SensitivitySource = config.SensitivityMeasured
	file.SensitivityUncertaintyDB = 0.5
	file.SensitivityMeasuredOn = time.Now().Add(-30 * 24 * time.Hour).Format("2006-01-02")
	file.SensitivityReference = "B and K 4231 at 94 dB SPL"
	if camera != "off" {
		// 127.0.0.1 rather than a camera address: the invented ffmpeg is
		// the only thing that answers, and a real address would make the
		// clock reading wait out its timeout.
		file.CameraHost = "127.0.0.1"
		file.VideoDir = filepath.Join(dir, "clips", "video")
		if err := os.MkdirAll(file.VideoDir, 0o750); err != nil {
			return err
		}
	}
	overrides, err := st.Settings(context.Background())
	if err != nil {
		return err
	}
	live, err := settings.New(file, overrides)
	if err != nil {
		log.Error("ignoring the stored settings", "err", err)
		if live, err = settings.New(file, nil); err != nil {
			return err
		}
	}

	// The camera test runs ffmpeg. There is no camera here, so the
	// devserver writes one that answers the way a Reolink on older
	// firmware does: the first path fails, the second works.
	fakeFFmpeg, err := writeFakeFFmpeg(dir, camera)
	if err != nil {
		return err
	}

	feed := web.NewLiveFeed()
	now := time.Now()
	status := health.NewStatus(now)
	srv, err := web.New(web.Config{
		Store:    st,
		Settings: live,
		Status:   status,
		Live:     feed,
		Stats:    func() web.Stats { return web.Stats{Chunks: 42} },
		Capture: web.Capture{
			Device: "hw:EM01,0", Gain: "no capture control",
			Calibration: "em01-response.txt (correction 0.42 dB)", SensitivityDBFS: -12.47,
		},
		Started: now,
		LogDir:  logDir, LogName: "stompwatch.log", ClipDir: clipDir,
		VideoDir:      file.VideoDir,
		Camera:        inventedCamera(camera, now),
		CameraLogin:   func() video.Login { return inventedLogin(camera) },
		FFmpegVersion: inventedFFmpeg(camera),
		FFmpeg:        fakeFFmpeg,
		Log:           log,
		// Without this the dashboard's own health rows, such as a purge or a
		// clip that has gone, would be dropped and the development System
		// screen would say nothing happened.
		// Tailscale is invented too. The devserver never runs the real
		// command: the System screen has to be buildable on a laptop with
		// no tailnet, and in every state at will.
		Tailscale: inventedTailscale(tailscale),
		RecordHealth: func(at time.Time, kind, detail string, d time.Duration) {
			if err := st.AddHealth(context.Background(), at, kind, detail, d); err != nil {
				log.Error("recording a health row failed", "kind", kind, "err", err)
			}
		},
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go publishLive(ctx, feed, status)
	go writeLog(ctx, logDir, log)

	log.Info("serving the dashboard API", "addr", addr, "auth_mode", "none")
	return srv.Run(ctx)
}

// publishLive feeds the live meter a wandering level, so the SSE stream can
// be tried without a microphone. It also keeps the health status fresh, or
// the system page would always say the collector had stopped.
func publishLive(ctx context.Context, feed *web.LiveFeed, status *health.Status) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	level, rng := 36.0, rand.New(rand.NewPCG(7, 9))
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			level += rng.NormFloat64() * 1.5
			level = math.Max(28, math.Min(78, level))
			status.AudioArrived(time.Now())
			status.BinsCommitted(time.Now())
			feed.Publish(meter.Bin{
				Start: time.Now().Truncate(time.Second), Samples: 48000,
				LAeq: level, LAmax: level + 2 + rng.Float64()*6,
				LowBand: level + 8, HighBand: level - 6, Baseline: 33,
			})
		}
	}
}

// writeLog keeps the log file moving so the log view has something to show.
func writeLog(ctx context.Context, dir string, log *slog.Logger) {
	f, err := os.OpenFile(filepath.Join(dir, "stompwatch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		log.Warn("no log file for the log view", "err", err)
		return
	}
	defer f.Close()
	l := slog.New(slog.NewJSONHandler(f, nil))
	l.Info("starting", "device", "hw:EM01,0", "channel", 0)
	l.Warn("free space is low", "free_mb", 900)
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Info("snapshot written", "sha256", "d0a1...")
		}
	}
}

// seed invents days of one-second levels and a handful of events a night.
// Levels follow a daily shape: quiet at night, busier in the evening.
func seed(ctx context.Context, st *store.Store, clipDir string, days int, log *slog.Logger) error {
	rec, err := clip.NewRecorder(clipDir, clip.BufferLength, dsp.DefaultClipLowpassHz)
	if err != nil {
		return err
	}
	rng := rand.New(rand.NewPCG(1, 2))
	end := time.Now().Truncate(time.Second)
	start := end.Add(-time.Duration(days) * 24 * time.Hour)

	// The capture-settings history, in all three of its states: the oldest
	// events predate it and cite nothing, the middle ones cite the datasheet
	// figure, and the newest cite a measured one. All three have to be
	// buildable on a laptop (SPEC.md section 15 decision 23).
	if err := seedCaptureSettings(ctx, st, start, end); err != nil {
		return err
	}

	var bins []meter.Bin
	var eventsAt []time.Time
	for t := start; t.Before(end); t = t.Add(time.Second) {
		base := dailyLevel(t)
		laeq := base + rng.NormFloat64()*1.5
		bins = append(bins, meter.Bin{
			Start: t, Samples: 48000,
			LAeq: laeq, LAmax: laeq + 1 + rng.Float64()*3,
			LowBand: laeq + 7, HighBand: laeq - 5, Baseline: base - 2,
		})
		// A few events an evening, all inside the hours that matter.
		if h := t.Hour(); (h >= 19 || h < 2) && rng.IntN(1400) == 0 {
			eventsAt = append(eventsAt, t)
		}
		if len(bins) == 3600 {
			if _, err := st.InsertBins(ctx, bins); err != nil {
				return err
			}
			bins = bins[:0]
		}
	}
	if _, err := st.InsertBins(ctx, bins); err != nil {
		return err
	}

	classes := []detect.Class{detect.Running, detect.Jumping, detect.Stomping, detect.Unknown}
	for i, at := range eventsAt {
		dur := time.Duration(600+rng.IntN(9000)) * time.Millisecond
		peak := dailyLevel(at) + 14 + rng.Float64()*18
		e := detect.Event{
			Start: at, End: at.Add(dur),
			LAeq: peak - 6, LAmax: peak, BaselineAtTrigger: dailyLevel(at) - 2,
			LowBand: peak + 4, HighBand: peak - 12, LowHighRatioDB: 16,
			Class: classes[rng.IntN(len(classes))], Confidence: 0.5 + rng.Float64()/2,
			Envelope: envelope(dur, rng),
		}
		id, err := st.InsertEvent(ctx, e, at)
		if err != nil {
			return err
		}
		if err := saveClip(st, rec, id, e, rng); err != nil {
			return err
		}
		// Leave the newest third unreviewed, so the review workflow has work.
		if i < len(eventsAt)*2/3 {
			status := []string{store.StatusVerified, store.StatusRejected, store.StatusUnsure}[rng.IntN(3)]
			note := ""
			if status == store.StatusVerified {
				note = "clearly footsteps"
			}
			if err := st.SetReview(ctx, store.Review{
				EventID: id, Status: status, Note: note,
				Reviewer: "dev", At: at.Add(time.Hour),
			}); err != nil {
				return err
			}
		}
	}

	for _, h := range []struct {
		back   time.Duration
		kind   string
		detail string
	}{
		{2 * time.Hour, store.HealthCaptureGap, "arecord stream 3 ended: exit status 1"},
		{6 * time.Hour, store.HealthDiskLow, "the data disk has 900 MB free"},
		{26 * time.Hour, store.HealthClockStep, "audio time moved forward by 180 ms"},
		{30 * time.Hour, store.HealthSettingsChange, "the baseline started again after a settings change"},
	} {
		if err := st.AddHealth(ctx, end.Add(-h.back), h.kind, h.detail, 0); err != nil {
			return err
		}
	}
	log.Info("invented", "seconds", int(end.Sub(start).Seconds()), "events", len(eventsAt))
	return nil
}

// dailyLevel is the ambient level at a time of day: quietest before dawn,
// loudest in the evening.
// seedCaptureSettings writes two rows: the datasheet figure from a third of
// the way into the range, and a measured one from two thirds in. Events
// before the first row have no settings to cite, which is what every event
// recorded before the history began looks like.
func seedCaptureSettings(ctx context.Context, st *store.Store, start, end time.Time) error {
	span := end.Sub(start)
	rows := []store.CaptureSettings{{
		From:            start.Add(span / 3),
		SensitivityDBFS: -13,
		UncertaintyDB:   2,
		Source:          store.SensitivityDatasheet,
		CaptureDevice:   "hw:EM01,0",
	}, {
		From:                start.Add(2 * span / 3),
		SensitivityDBFS:     -12.47,
		UncertaintyDB:       0.5,
		Source:              store.SensitivityMeasured,
		MeasuredOn:          start.Add(2 * span / 3).Format("2006-01-02"),
		Reference:           "B and K 4231 at 94 dB SPL",
		CalibrationFile:     "em01-response.txt",
		CalibrationOffsetDB: 0.42,
		CaptureDevice:       "hw:EM01,0",
	}}
	for _, row := range rows {
		if _, err := st.RecordCaptureSettings(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func dailyLevel(t time.Time) float64 {
	hours := float64(t.Hour()) + float64(t.Minute())/60
	return 36 + 7*math.Sin((hours-9)/24*2*math.Pi)
}

func envelope(d time.Duration, rng *rand.Rand) []float64 {
	n := int(d.Seconds()*100) + 20
	env := make([]float64, n)
	for i := range env {
		env[i] = 0.02 + rng.Float64()*0.05
	}
	for at := 5; at < n-5; at += 30 + rng.IntN(30) {
		for k := 0; k < 8 && at+k < n; k++ {
			env[at+k] = math.Max(env[at+k], 0.9*math.Exp(-float64(k)/3))
		}
	}
	return env
}

// saveClip writes a real clip file for an event, so playback and the waveform
// have something to read.
func saveClip(st *store.Store, rec *clip.Recorder, id int64, e detect.Event, rng *rand.Rand) error {
	rec.Reset()
	from := e.Start.Add(-10 * time.Second)
	to := e.End.Add(5 * time.Second)
	// Feed two seconds past the end. The decimator holds samples until it has
	// a whole output sample, so audio stops arriving a little before the
	// audio that was fed in.
	total := to.Add(2 * time.Second).Sub(from)
	x := testsignal.Pink(int(total.Seconds()*testsignal.Rate), 0.004, rng.Uint64())
	for at := 10.0; at < 10+e.Duration().Seconds(); at += 0.4 + rng.Float64()/2 {
		testsignal.AddThump(x, at, 0.25)
	}
	// Feed it in one-second pieces, the way capture does.
	for i := 0; i < len(x); i += testsignal.Rate {
		n := min(testsignal.Rate, len(x)-i)
		rec.Process(x[i:i+n], from.Add(time.Duration(i)*time.Second/testsignal.Rate))
	}
	c, err := rec.Save(id, from, to)
	if err != nil {
		return fmt.Errorf("clip for event %d: %w", id, err)
	}
	return st.InsertMedia(context.Background(), store.Media{
		EventID: id, Kind: store.KindAudio, Path: c.Path, Bytes: c.Bytes,
		Duration: c.Duration, SHA256: c.SHA256, Started: c.Start, Truncated: c.Truncated,
	})
}

// inventedCamera is a camera that has been delivering video for an hour,
// so the status half of the camera section has something to show. "off"
// gives no camera at all.
func inventedCamera(mode string, now time.Time) func() web.CameraStatus {
	if mode == "off" {
		return nil
	}
	return func() web.CameraStatus {
		return web.CameraStatus{
			Enabled: true, SubPath: "Preview_01_sub", Connected: true,
			ConnectedSince: now.Add(-time.Hour), Uptime: time.Hour,
			Disconnects: 2, LastSegment: time.Now().Add(-3 * time.Second),
			RingSegments: 60, RingBytes: 90_000_000,
			// A camera with a microphone, so the camera section can be seen
			// in the state where camera_audio would do something.
			AudioKnown: true,
			Audio:      video.AudioInfo{Present: true, Codec: "aac", RateHz: 16000},
		}
	}
}

// inventedFFmpeg is the ffmpeg on this machine, or an invented version when
// there is none, so the camera section can be seen in both states on a
// machine that has no ffmpeg.
func inventedFFmpeg(mode string) string {
	if mode == "no-ffmpeg" {
		return ""
	}
	if v := video.Version(nil, nil); v != "" {
		return v
	}
	return "ffmpeg version 7.1.1-1 (invented by the devserver)"
}

// inventedLogin is a camera login from a file, or none at all with
// -camera no-login, which is the state the owner starts from.
func inventedLogin(mode string) video.Login {
	if mode == "no-login" || mode == "off" {
		return video.Login{Source: video.FromNowhere,
			Err: errors.New("video: no camera login")}
	}
	return video.Login{Source: video.FromFile,
		Creds: video.Credentials{User: "admin", Pass: "development-only"}}
}

// writeFakeFFmpeg writes a script that stands in for ffmpeg, so the camera
// test has something to answer it. It is never near the shipped binary.
func writeFakeFFmpeg(dir, mode string) (video.Command, error) {
	if mode == "no-ffmpeg" {
		return video.Command{filepath.Join(dir, "no-such-ffmpeg")}, nil
	}
	path := filepath.Join(dir, "fake-ffmpeg")
	script := `#!/bin/sh
sleep 1
for a in "$@"; do
  case "$a" in
    *h264Preview_01_*)
      echo "  Stream #0:0: Video: h264 (Main), yuv420p(progressive), 640x360, 15 fps, 15 tbr, 90k tbn" >&2
      exit 0;;
  esac
done
for a in "$@"; do
  case "$a" in
    rtsp://*) echo "$a: 401 Unauthorized" >&2;;
  esac
done
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return nil, err
	}
	return video.Command{path}, nil
}

// inventedTailscale answers the way a box in one of the interesting states
// would. Nothing here runs tailscale.
func inventedTailscale(mode string) func(context.Context) tailnet.Status {
	node := "stompwatch.example.ts.net"
	st := tailnet.Status{
		Installed: true, Running: true, Backend: "Running", Name: node,
		Serving: true, ServeURL: "https://" + node + "/",
	}
	switch mode {
	case "off":
		st = tailnet.Status{Err: "tailscale is not installed on this box"}
	case "stopped":
		st = tailnet.Status{
			Installed: true, Backend: "Stopped",
			Err: "tailscale status could not be read: failed to connect to local tailscaled",
		}
	case "not-serving":
		st.Serving, st.ServeURL = false, ""
	case "expiring":
		st.KeyExpiry = time.Now().Add(14 * 24 * time.Hour)
	case "funnel":
		st.Funnel = true
	}
	return func(context.Context) tailnet.Status { return st }
}
