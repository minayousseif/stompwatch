package audio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const fakeEnv = "STOMPWATCH_FAKE_ARECORD"

// TestHelperProcess stands in for arecord when a test sets STOMPWATCH_FAKE_ARECORD.
// The capture arguments come after "--".
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(fakeEnv)
	if mode == "" {
		return
	}
	frame := func(l, r int32) []byte {
		return []byte{byte(l), byte(l >> 8), byte(l >> 16), byte(r), byte(r >> 8), byte(r >> 16)}
	}
	// ramp writes frames L = i, R = -i in 100 ms blocks. When paced, each
	// block waits for its real time.
	ramp := func(frames int, paced bool) {
		begin := time.Now()
		block := make([]byte, 0, 4800*6)
		for i := 0; i < frames; i++ {
			block = append(block, frame(int32(i), int32(-i))...)
			if len(block) == cap(block) || i == frames-1 {
				os.Stdout.Write(block)
				block = block[:0]
				if paced {
					time.Sleep(time.Until(begin.Add(time.Duration(i+1) * time.Second / SampleRate)))
				}
			}
		}
	}
	switch mode {
	case "ramp":
		ramp(SampleRate/2, true)
	case "fastramp":
		ramp(3*SampleRate, false)
	case "zeros":
		os.Stdout.Write(make([]byte, 10*SampleRate*6))
		time.Sleep(time.Minute)
	case "args":
		dash := 0
		for i, a := range os.Args {
			if a == "--" {
				dash = i
			}
		}
		fmt.Fprintln(os.Stderr, "args:", strings.Join(os.Args[dash+1:], " "))
		os.Exit(1)
	case "fail":
		fmt.Fprintln(os.Stderr, "arecord: main:850: audio open error: No such device")
		os.Exit(1)
	}
	os.Exit(0)
}

type captureRun struct {
	cap    *Capture
	chunks chan Chunk
	events chan CaptureEvent
	cancel context.CancelFunc
	done   chan error
}

func startCapture(t *testing.T, mode string, change func(*CaptureConfig)) *captureRun {
	t.Helper()
	r := &captureRun{
		chunks: make(chan Chunk, 1000),
		events: make(chan CaptureEvent, 1000),
		done:   make(chan error, 1),
	}
	cfg := CaptureConfig{
		Device:     "hw:EM01,0",
		Channel:    0,
		Command:    []string{os.Args[0], "-test.run=^TestHelperProcess$", "--"},
		Env:        []string{fakeEnv + "=" + mode},
		Out:        r.chunks,
		OnEvent:    func(e CaptureEvent) { r.events <- e },
		MinBackoff: 20 * time.Millisecond,
		MaxBackoff: 160 * time.Millisecond,
		StuckLimit: time.Hour,
	}
	if change != nil {
		change(&cfg)
	}
	c, err := NewCapture(cfg)
	if err != nil {
		t.Fatalf("NewCapture: %v", err)
	}
	r.cap = c
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { r.done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return r
}

// waitEvent returns the next event of the given kind, skipping others.
func (r *captureRun) waitEvent(t *testing.T, kind CaptureEventKind) CaptureEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-r.events:
			if e.Kind == kind {
				return e
			}
		case <-deadline:
			t.Fatalf("no %v event within 10 s", kind)
		}
	}
}

func TestCaptureDeliversSelectedChannel(t *testing.T) {
	for _, channel := range []int{0, 1} {
		r := startCapture(t, "ramp", func(c *CaptureConfig) { c.Channel = channel })
		var got []float64
		deadline := time.After(10 * time.Second)
		for len(got) < SampleRate/2 {
			select {
			case ch := <-r.chunks:
				if ch.Stream != 1 {
					t.Fatalf("chunk from stream %d, want 1", ch.Stream)
				}
				if ch.Offset != int64(len(got)) {
					t.Fatalf("chunk offset %d, want %d", ch.Offset, len(got))
				}
				got = append(got, ch.Samples...)
			case <-deadline:
				t.Fatalf("channel %d: got %d samples in 10 s", channel, len(got))
			}
		}
		sign := 1.0
		if channel == 1 {
			sign = -1
		}
		for i := 0; i < SampleRate/2; i++ {
			if want := sign * float64(i) / (1 << 23); got[i] != want {
				t.Fatalf("channel %d sample %d = %v, want %v", channel, i, got[i], want)
			}
		}
		r.cancel()
	}
}

// Every gap in capture is reported with its length (SPEC.md section 6.1).
func TestCaptureRestartsAfterExitAndReportsGap(t *testing.T) {
	r := startCapture(t, "ramp", nil)
	first := r.waitEvent(t, CaptureStarted)
	if first.Stream != 1 || first.Gap != 0 {
		t.Errorf("first start: stream %d gap %v, want 1 and 0", first.Stream, first.Gap)
	}
	exit := r.waitEvent(t, CaptureExited)
	second := r.waitEvent(t, CaptureStarted)
	if exit.Stream != 1 || second.Stream != 2 {
		t.Errorf("exit stream %d, restart stream %d; want 1 and 2", exit.Stream, second.Stream)
	}
	if second.Gap < 20*time.Millisecond || second.Gap > 3*time.Second {
		t.Errorf("gap before stream 2 = %v, want at least the 20 ms backoff and under 3 s", second.Gap)
	}
}

func TestCaptureBacksOffOnRepeatedFailure(t *testing.T) {
	r := startCapture(t, "fail", nil)
	var at []time.Time
	for i := 0; i < 5; i++ {
		e := r.waitEvent(t, CaptureExited)
		if !strings.Contains(e.Detail, "No such device") {
			t.Errorf("exit detail %q does not include arecord's error", e.Detail)
		}
		at = append(at, e.At)
	}
	// The waits are 20, 40, 80, then 160 ms. Process start-up adds about the
	// same time to every gap, so the last gap is about 140 ms longer.
	firstGap, lastGap := at[1].Sub(at[0]), at[4].Sub(at[3])
	if lastGap-firstGap < 100*time.Millisecond {
		t.Errorf("gaps between failures %v then %v, want the last about 140 ms longer", firstGap, lastGap)
	}
}

// SPEC.md section 6.1: capture S24_3LE, stereo, 48 kHz, raw, from the hw: device.
func TestCaptureRunsArecordWithRawHardwareArguments(t *testing.T) {
	r := startCapture(t, "args", nil)
	e := r.waitEvent(t, CaptureExited)
	for _, want := range []string{"-D hw:EM01,0", "-f S24_3LE", "-r 48000", "-c 2", "-t raw"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("arguments %q do not include %q", e.Detail, want)
		}
	}
}

// A stuck stream is a failure: report it, stop the process, and start again.
func TestCaptureRestartsStuckStream(t *testing.T) {
	r := startCapture(t, "zeros", func(c *CaptureConfig) { c.StuckLimit = time.Second })
	stuck := r.waitEvent(t, CaptureStuck)
	restarted := r.waitEvent(t, CaptureStarted)
	for restarted.Stream < 2 {
		restarted = r.waitEvent(t, CaptureStarted)
	}
	if stuck.Stream != 1 {
		t.Errorf("stuck event for stream %d, want 1", stuck.Stream)
	}
}

// SPEC.md section 3.5: a slow consumer must never stall the reader. Chunks that do
// not fit are dropped and counted.
func TestCaptureDropsChunksWhenConsumerIsSlow(t *testing.T) {
	r := startCapture(t, "fastramp", func(c *CaptureConfig) { c.Out = make(chan Chunk, 1) })
	r.waitEvent(t, CaptureExited)
	if r.cap.Dropped() == 0 {
		t.Fatal("no chunks dropped with a full channel, want drops counted")
	}
}

func TestCaptureStopsWhenCancelled(t *testing.T) {
	r := startCapture(t, "zeros", nil)
	r.waitEvent(t, CaptureStarted)
	r.cancel()
	select {
	case err := <-r.done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
		r.done <- err // for the cleanup
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5 s of cancel")
	}
}

func TestNewCaptureRejectsBadConfig(t *testing.T) {
	out := make(chan Chunk, 1)
	for name, cfg := range map[string]CaptureConfig{
		"default device": {Device: "default", Out: out},
		"channel 2":      {Device: "hw:EM01,0", Channel: 2, Out: out},
		"no output":      {Device: "hw:EM01,0"},
	} {
		if _, err := NewCapture(cfg); err == nil {
			t.Errorf("%s: NewCapture returned no error", name)
		}
	}
}
