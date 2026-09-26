package video

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// events collects ring events from the supervisor goroutine.
type events struct {
	mu   sync.Mutex
	list []Event
}

func (e *events) add(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) snapshot() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.list...)
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

func kinds(evs []Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Kind.String())
	}
	return strings.Join(out, " ")
}

// startRing runs a ring on a fake ffmpeg until the test ends.
func startRing(t *testing.T, mode map[string]string, change func(*RingConfig)) (*Ring, *events, string, context.CancelFunc) {
	t.Helper()
	cmd, argv, env := fake(t, "segments", mode)
	evs := &events{}
	cfg := RingConfig{
		Dir:            filepath.Join(t.TempDir(), "ring"),
		Streams:        []Stream{testStream("Preview_01_sub"), testStream("h264Preview_01_sub")},
		SegmentSeconds: 10,
		Keep:           10 * time.Minute,
		Command:        cmd,
		Env:            env,
		OnEvent:        evs.add,
		MinBackoff:     20 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
		StallLimit:     2 * time.Second,
		Poll:           10 * time.Millisecond,
	}
	if change != nil {
		change(&cfg)
	}
	r, err := NewRing(cfg)
	if err != nil {
		t.Fatalf("NewRing: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return r, evs, argv, cancel
}

// SPEC.md section 7: the ring supervises ffmpeg, restarts it with backoff, and
// reports every disconnect with its duration. ffmpeg's own words come
// through with the password masked.
func TestRingRestartsFFmpegAndReportsEachDisconnect(t *testing.T) {
	now := time.Now().Unix()
	r, evs, argv, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_COUNT": "3", "FAKE_INTERVAL_MS": "20",
		"FAKE_EXIT": "1", "FAKE_STDERR": "rtsp://admin:" + escapePassword(testPass) + "@cam.local:554/Preview_01_sub: Connection reset by peer\n",
	}, nil)

	waitFor(t, "two streams", func() bool { return len(evs.snapshot()) >= 4 })
	got := evs.snapshot()[:4]
	if k := kinds(got); k != "started exited started exited" {
		t.Fatalf("events: %s %+v", k, got)
	}
	if got[0].Stream != 1 || got[2].Stream != 2 {
		t.Errorf("streams %d and %d, want 1 and 2", got[0].Stream, got[2].Stream)
	}
	if got[0].Gap != 0 {
		t.Errorf("the first start reports a gap of %v", got[0].Gap)
	}
	if got[2].Gap <= 0 {
		t.Errorf("the restart reports no gap")
	}
	exit := got[1]
	if !strings.Contains(exit.Detail, "Connection reset by peer") || !strings.Contains(exit.Detail, "rtsp://admin:***@") {
		t.Errorf("exit detail %q does not carry ffmpeg's masked reason", exit.Detail)
	}
	if strings.Contains(exit.Detail, testPass) || strings.Contains(exit.Detail, escapePassword(testPass)) {
		t.Fatalf("exit detail holds the password: %q", exit.Detail)
	}
	if st := r.Status(); st.Disconnects < 1 {
		t.Errorf("status counts %d disconnects", st.Disconnects)
	}
	if !strings.Contains(got[1].Detail, "/Preview_01_sub") || !strings.Contains(got[3].Detail, "/h264Preview_01_sub") {
		t.Errorf("the exits do not name the path each stream used: %q, %q", got[1].Detail, got[3].Detail)
	}

	runs := readArgv(t, argv)
	if len(runs) < 2 {
		t.Fatalf("ffmpeg ran %d times", len(runs))
	}
	// Each restart moves to the next candidate path.
	if !hasPair(runs[0], "-i", testStream("Preview_01_sub").URL()) || !hasPair(runs[1], "-i", testStream("h264Preview_01_sub").URL()) {
		t.Errorf("the runs did not rotate through the paths:\n%v\n%v", runs[0], runs[1])
	}
	for _, run := range runs {
		if !contains(run, "-an") {
			t.Fatalf("ffmpeg ran without -an: %v", run)
		}
		if !hasPair(run, "-c", "copy") || !hasPair(run, "-rtsp_transport", "tcp") || !hasPair(run, "-f", "segment") {
			t.Errorf("ffmpeg ran without copy, tcp, or segment: %v", run)
		}
		if a := reencodesVideo(run); a != "" {
			t.Errorf("ffmpeg ran with %q: %v", a, run)
		}
		if !strings.HasSuffix(run[len(run)-1], SegmentPattern) || !strings.HasPrefix(run[len(run)-1], r.Dir()) {
			t.Errorf("output pattern %q is not in the ring directory", run[len(run)-1])
		}
	}
}

// With camera_rtsp_path empty the ring finds a working path by trying the
// defaults in turn, and the dashboard has to be able to say which one it
// is now using.
func TestRingStatusSaysWhichPathItIsUsing(t *testing.T) {
	now := time.Now().Unix()
	r, evs, _, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_COUNT": "1", "FAKE_INTERVAL_MS": "20",
		"FAKE_EXIT": "1",
	}, nil)

	waitFor(t, "the first stream", func() bool { return len(evs.snapshot()) >= 1 })
	if got := r.Status().Path; got != "Preview_01_sub" {
		t.Errorf("Path = %q on the first stream, want \"Preview_01_sub\"", got)
	}
	waitFor(t, "the second stream", func() bool { return r.Status().Path == "h264Preview_01_sub" })
}

// A stream that stops producing segments is as dead as one that exited,
// and ffmpeg does not always notice. The ring kills it and starts again.
func TestRingRestartsAStalledStream(t *testing.T) {
	now := time.Now().Unix()
	_, evs, _, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_HANG": "1",
	}, func(c *RingConfig) { c.StallLimit = 100 * time.Millisecond })

	waitFor(t, "a stall and a restart", func() bool { return len(evs.snapshot()) >= 4 })
	got := evs.snapshot()[:4]
	if k := kinds(got); k != "started stuck exited started" {
		t.Fatalf("events: %s %+v", k, got)
	}
	if !strings.Contains(got[1].Detail, "100ms") {
		t.Errorf("stuck detail %q does not say how long", got[1].Detail)
	}
}

// The ring holds video_ring_minutes and no more. Old segments go, oldest
// first, while the stream runs.
func TestRingPrunesOldSegmentsWhileRunning(t *testing.T) {
	now := time.Now().Unix()
	dir := filepath.Join(t.TempDir(), "ring")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	old := writeSegments(t, dir, time.Minute, 25) // 2026-09-12 03:00, long before now
	r, evs, _, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_COUNT": "0", "FAKE_INTERVAL_MS": "20",
	}, func(c *RingConfig) { c.Dir = dir })

	waitFor(t, "the stream to start", func() bool { return len(evs.snapshot()) >= 1 })
	waitFor(t, "the old segments to go", func() bool {
		segs, _ := r.Segments()
		for _, s := range segs {
			if filepath.Base(s.Path) == old[24] {
				return false
			}
		}
		return len(segs) > 0
	})
	segs, err := r.Segments()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range segs {
		if s.Start.Before(time.Unix(now, 0)) {
			t.Errorf("an old segment survived: %s", s.Path)
		}
	}
	st := r.Status()
	if !st.Connected || st.Uptime <= 0 || st.Segments == 0 || st.Bytes == 0 {
		t.Errorf("status while running: %+v", st)
	}
}

// Uptime is the time the camera was actually delivering segments, not the
// time since the ring started, and the outages are kept so an event clip
// can say it has a hole. The status is read during the wait before the
// restart, when the stream is down: that is when the two differ.
func TestRingTracksUptimeAndOutages(t *testing.T) {
	now := time.Now().Unix()
	began := time.Now()
	r, evs, _, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_COUNT": "2", "FAKE_INTERVAL_MS": "30", "FAKE_EXIT": "1",
	}, func(c *RingConfig) { c.MinBackoff, c.MaxBackoff = 3*time.Second, 3*time.Second })
	waitFor(t, "the stream to end", func() bool {
		evs := evs.snapshot()
		return len(evs) >= 2 && evs[1].Kind == Exited
	})
	time.Sleep(300 * time.Millisecond)

	st := r.Status()
	if st.Connected {
		t.Error("connected while the stream is down")
	}
	if st.Uptime <= 0 {
		t.Error("no uptime after a stream that delivered two segments")
	}
	// The stream lived about 60 ms, and the 300 ms since it ended must not
	// count. Time since the ring began is the wrong answer.
	if since := time.Since(began); st.Uptime > since-250*time.Millisecond {
		t.Errorf("uptime %v is close to the %v since the ring began; it counts the outage", st.Uptime, since)
	}
	if st.Disconnects != 1 {
		t.Errorf("disconnects = %d, want 1", st.Disconnects)
	}
	outages := r.Outages()
	if len(outages) != 1 {
		t.Fatalf("outages = %+v, want the open one", outages)
	}
	if o := outages[0]; o.From.IsZero() || !o.To.IsZero() {
		t.Errorf("outage %+v should be open, with no end yet", o)
	}
}

func TestChildEnvPinsUTC(t *testing.T) {
	env := childEnv([]string{"A=1"})
	if !contains(env, "TZ=UTC") || !contains(env, "A=1") {
		t.Fatalf("childEnv = %v", env)
	}
}

// ffmpeg that is not installed must be said plainly, not found out from a
// stream of exit errors at 2am.
func TestCheckFFmpegNamesTheMissingProgram(t *testing.T) {
	err := CheckFFmpeg(Command{filepath.Join(t.TempDir(), "ffmpeg")})
	if err == nil {
		t.Fatal("a missing ffmpeg passed")
	}
	if !strings.Contains(err.Error(), "ffmpeg") || !strings.Contains(err.Error(), "install") {
		t.Errorf("error %q does not name ffmpeg or say to install it", err)
	}
	if err := CheckFFmpeg(Command{os.Args[0]}); err != nil {
		t.Errorf("an existing program failed: %v", err)
	}
}

// SPEC.md section 15 decision 22: with camera_audio on, the ring records the
// camera's own audio track. The test reads the argument list the ring
// really handed ffmpeg, because that is what decides what lands on disk.
func TestRingKeepsTheCameraAudioWhenTheSettingIsOn(t *testing.T) {
	now := time.Now().Unix()
	_, evs, argv, cancel := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_COUNT": "2", "FAKE_INTERVAL_MS": "20",
	}, func(cfg *RingConfig) { cfg.Audio = true })
	defer cancel()

	waitFor(t, "the first stream", func() bool { return len(evs.snapshot()) >= 1 })
	runs := readArgv(t, argv)
	if len(runs) < 1 {
		t.Fatalf("ffmpeg ran %d times", len(runs))
	}
	for _, run := range runs {
		if contains(run, "-an") {
			t.Errorf("camera_audio is on and the ring still ran with -an: %v", run)
		}
		// The video is copied and the audio is re-encoded, so that a segment
		// describes its own audio and can be decoded later.
		if !hasPair(run, "-c:v", "copy") {
			t.Errorf("the ring ran without -c:v copy, so it re-encodes the video: %v", run)
		}
		if !hasPair(run, "-c:a", "aac") {
			t.Errorf("the ring ran without -c:a aac, so the segment's audio cannot be decoded: %v", run)
		}
	}
}

// The collector logs at start that the camera's audio is recorded into the
// ring. That claim is read off the ring's own command line, because a claim
// read off the setting was what made the log lie for a day.
func TestKeepsUsableAudioReadsTheCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"the audio re-encoded, as the ring does it now", []string{"-i", "rtsp://x", "-c:v", "copy", "-c:a", "aac"}, true},
		{"the audio dropped", []string{"-i", "rtsp://x", "-an", "-c", "copy"}, false},
		{"every stream copied, which loses the configuration", []string{"-i", "rtsp://x", "-c", "copy"}, false},
		{"the audio copied by name", []string{"-i", "rtsp://x", "-c:v", "copy", "-c:a", "copy"}, false},
		{"an audio encoder and an -an as well", []string{"-i", "rtsp://x", "-an", "-c:a", "aac"}, false},
		{"the older spelling of the audio codec", []string{"-i", "rtsp://x", "-acodec", "aac"}, true},
	} {
		if got := keepsUsableAudio(tc.args); got != tc.want {
			t.Errorf("%s: keepsUsableAudio = %v, want %v: %v", tc.name, got, tc.want, tc.args)
		}
	}
}

// RecordsAudio answers for the command line this ring will really run.
func TestRingRecordsAudioComesFromTheRealArguments(t *testing.T) {
	for _, audio := range []bool{false, true} {
		r, err := NewRing(RingConfig{
			Dir: t.TempDir(), Streams: []Stream{testStream("Preview_01_sub")},
			SegmentSeconds: 10, Keep: time.Minute, Audio: audio,
		})
		if err != nil {
			t.Fatalf("audio = %v: NewRing: %v", audio, err)
		}
		if got := r.RecordsAudio(); got != audio {
			t.Errorf("audio = %v: RecordsAudio = %v", audio, got)
		}
	}
}

// Inside the daily recording pause the ring runs no ffmpeg, so nothing from
// the camera reaches the disk. Stopping for the pause is not a disconnect:
// it is not counted, it is not an outage, and the stream after it is marked
// as coming after a pause, so it is not reported as a gap in the video.
func TestRingStopsForThePauseAndResumesWithoutADisconnect(t *testing.T) {
	now := time.Now().Unix()
	var paused atomic.Bool
	r, evs, _, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_COUNT": "0", "FAKE_INTERVAL_MS": "20",
	}, func(c *RingConfig) { c.Paused = func(time.Time) bool { return paused.Load() } })

	waitFor(t, "the first stream", func() bool { return r.Status().Connected })
	paused.Store(true)
	waitFor(t, "ffmpeg to stop for the pause", func() bool { return !r.Status().Connected })

	before := r.Status().Segments
	time.Sleep(300 * time.Millisecond) // thirty polls and fifteen segment intervals
	st := r.Status()
	if st.Segments != before {
		t.Errorf("the ring grew from %d to %d segments during the pause; ffmpeg is still running", before, st.Segments)
	}
	if st.Disconnects != 0 {
		t.Errorf("the pause counted %d disconnects, want 0", st.Disconnects)
	}
	if o := r.Outages(); len(o) != 0 {
		t.Errorf("the pause is recorded as outages: %+v", o)
	}

	paused.Store(false)
	waitFor(t, "ffmpeg to start again after the pause", func() bool { return r.Status().Connected })

	var sawPause bool
	var lastStart Event
	for _, e := range evs.snapshot() {
		switch e.Kind {
		case Exited, Stuck:
			t.Errorf("the pause was reported as %s: %+v", e.Kind, e)
		case Paused:
			sawPause = true
		case Started:
			lastStart = e
		}
	}
	if !sawPause {
		t.Error("no Paused event was reported when ffmpeg stopped for the pause")
	}
	if !lastStart.AfterPause {
		t.Errorf("the stream after the pause is not marked as coming after one: %+v", lastStart)
	}
	if st := r.Status(); st.Disconnects != 0 {
		t.Errorf("after the pause the ring counts %d disconnects, want 0", st.Disconnects)
	}
}

// With no pause function the ring behaves as it always did.
func TestRingWithNoPauseNeverStops(t *testing.T) {
	now := time.Now().Unix()
	r, evs, _, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(now, 10), "FAKE_COUNT": "0", "FAKE_INTERVAL_MS": "20",
	}, nil)
	waitFor(t, "the first stream", func() bool { return r.Status().Connected })
	time.Sleep(200 * time.Millisecond)
	if !r.Status().Connected {
		t.Error("the ring stopped with no pause set")
	}
	for _, e := range evs.snapshot() {
		if e.Kind != Started {
			t.Errorf("unexpected %s event with no pause set: %+v", e.Kind, e)
		}
	}
}

// The ring asks about now and about one poll ahead, so ffmpeg stops before
// the pause begins rather than up to one poll after it, and it starts again
// only once the pause is over. The clock is the ring's own, so the edges are
// exact rather than raced.
func TestRingStopsOnePollBeforeThePauseAndStartsOnlyAfterIt(t *testing.T) {
	base := time.Date(2026, 9, 13, 21, 0, 0, 0, time.UTC)
	pauseFrom, pauseTo := base.Add(time.Minute), base.Add(2*time.Minute)
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	r, _, _, _ := startRing(t, map[string]string{
		"FAKE_START": strconv.FormatInt(time.Now().Unix(), 10), "FAKE_COUNT": "0", "FAKE_INTERVAL_MS": "20",
	}, func(c *RingConfig) {
		c.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
		c.Paused = func(t time.Time) bool { return !t.Before(pauseFrom) && t.Before(pauseTo) }
	})
	waitFor(t, "the first stream", func() bool { return r.Status().Connected })

	// Half a poll before the pause: not inside it yet, but inside it by the
	// next poll, so ffmpeg must stop now.
	clock.Store(pauseFrom.Add(-5 * time.Millisecond).UnixNano())
	waitFor(t, "ffmpeg to stop half a poll before the pause", func() bool { return !r.Status().Connected })

	// Half a poll before the pause ends: still inside it, so ffmpeg must stay
	// stopped.
	clock.Store(pauseTo.Add(-5 * time.Millisecond).UnixNano())
	time.Sleep(200 * time.Millisecond)
	if r.Status().Connected {
		t.Fatal("ffmpeg started again before the pause was over")
	}

	clock.Store(pauseTo.UnixNano())
	waitFor(t, "ffmpeg to start once the pause is over", func() bool { return r.Status().Connected })
}
