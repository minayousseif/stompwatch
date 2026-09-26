package detect

import (
	"math"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/meter"
)

var t0 = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)

const (
	baselineDB = 40.0
	loud       = 20.0 // dB above baseline; the default threshold is 15
	quiet      = 5.0
)

type feed struct {
	t      *testing.T
	d      *Detector
	events []Event
	next   time.Time
}

func newFeed(t *testing.T, change func(*Config)) *feed {
	t.Helper()
	f := &feed{t: t, next: t0}
	cfg := DefaultConfig()
	cfg.OnEvent = func(e Event) { f.events = append(f.events, e) }
	if change != nil {
		change(&cfg)
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.d = d
	return f
}

// send delivers n 100 ms frames at the given level above baseline. Like the
// meter, it delivers each frame's 10 envelope samples before the frame.
func (f *feed) send(n int, above float64) {
	f.sendFrames(n, above, meter.DefaultBaselineWindow)
}

func (f *feed) sendFrames(n int, above float64, baselineN int) {
	for i := 0; i < n; i++ {
		lv := baselineDB + above
		for k := 0; k < 10; k++ {
			f.d.Envelope(f.next.Add(time.Duration(k)*10*time.Millisecond), lv)
		}
		f.d.Frame(meter.Frame{
			Start: f.next, Samples: 4800,
			LAeq: lv, LAmax: lv, LowBand: lv - 10, HighBand: lv - 30,
			Baseline: baselineDB, BaselineN: baselineN,
		})
		f.next = f.next.Add(100 * time.Millisecond)
	}
}

func (f *feed) wantEvents(n int) {
	f.t.Helper()
	if len(f.events) != n {
		f.t.Fatalf("got %d events, want %d: %+v", len(f.events), n, f.events)
	}
}

func TestNoEventBeforeBaselineIsReady(t *testing.T) {
	f := newFeed(t, nil)
	f.sendFrames(10, loud, DefaultConfig().MinBaselineBins-1)
	f.sendFrames(30, quiet, DefaultConfig().MinBaselineBins-1)
	f.wantEvents(0)
}

func TestThresholdIsInclusive(t *testing.T) {
	f := newFeed(t, nil)
	f.send(10, 14.9)
	f.send(30, quiet)
	f.wantEvents(0)

	f.send(10, 15.0)
	f.send(30, quiet)
	f.wantEvents(1)
}

// 300 ms above the threshold is less than min_duration_ms (400 ms).
func TestShortBlipIsDiscarded(t *testing.T) {
	f := newFeed(t, nil)
	f.send(3, loud)
	f.send(30, quiet)
	f.wantEvents(0)
}

// Time above the threshold adds up across short dips: 200 + 200 = 400 ms.
// The event runs from the first loud frame to the end of the last one.
func TestTimeAboveThresholdAccumulatesAcrossDips(t *testing.T) {
	f := newFeed(t, nil)
	f.send(2, loud)
	f.send(5, quiet)
	f.send(2, loud)
	f.send(30, quiet)
	f.wantEvents(1)
	e := f.events[0]
	if !e.Start.Equal(t0) || !e.End.Equal(t0.Add(900*time.Millisecond)) {
		t.Errorf("event %v to %v, want %v to %v", e.Start, e.End, t0, t0.Add(900*time.Millisecond))
	}
}

// The event closes on the frame that brings the quiet time to 2 s.
func TestHangoverClosesAfterTwoSecondsOfQuiet(t *testing.T) {
	f := newFeed(t, nil)
	f.send(5, loud)
	f.send(19, quiet)
	f.wantEvents(0)
	f.send(1, quiet)
	f.wantEvents(1)
}

func TestQuietShorterThanHangoverKeepsOneEvent(t *testing.T) {
	f := newFeed(t, nil)
	f.send(5, loud)
	f.send(19, quiet)
	f.send(5, loud)
	f.send(20, quiet)
	f.wantEvents(1)
	if want := t0.Add(2900 * time.Millisecond); !f.events[0].End.Equal(want) {
		t.Errorf("event ends %v, want %v", f.events[0].End, want)
	}
}

// The first event closes at 2.5 s, so the cooldown lasts until 7.5 s.
func TestCooldownIgnoresTriggersAfterClose(t *testing.T) {
	f := newFeed(t, nil)
	f.send(5, loud)
	f.send(20, quiet) // closes at 2.5 s
	f.send(45, quiet) // until 7.0 s
	f.send(5, loud)   // 7.0-7.5 s, inside cooldown
	f.send(30, quiet)
	f.wantEvents(1)
}

func TestNewEventCanOpenWhenCooldownEnds(t *testing.T) {
	f := newFeed(t, nil)
	f.send(5, loud)
	f.send(20, quiet) // closes at 2.5 s
	f.send(50, quiet) // until 7.5 s
	f.send(5, loud)
	f.send(20, quiet)
	f.wantEvents(2)
	if want := t0.Add(7500 * time.Millisecond); !f.events[1].Start.Equal(want) {
		t.Errorf("second event starts %v, want %v", f.events[1].Start, want)
	}
}

func TestMaxEventForcesClose(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MaxEvent = 10 * time.Second })
	f.send(99, loud)
	f.wantEvents(0)
	f.send(1, loud)
	f.wantEvents(1)
	e := f.events[0]
	if !e.Forced || e.End.Sub(e.Start) != 10*time.Second {
		t.Errorf("event forced=%v duration=%v, want forced=true duration=10s", e.Forced, e.End.Sub(e.Start))
	}
}

// A gap in the frames (a capture restart) longer than the hangover closes
// the event at the end of its last loud frame. Loud audio after the gap must
// not stretch the old event across the outage.
func TestFrameGapClosesEvent(t *testing.T) {
	f := newFeed(t, nil)
	f.send(5, loud)
	f.next = f.next.Add(3 * time.Second)
	f.send(5, loud)
	f.send(30, quiet)
	f.wantEvents(1)
	if want := t0.Add(500 * time.Millisecond); !f.events[0].End.Equal(want) {
		t.Errorf("event ends %v, want %v", f.events[0].End, want)
	}
}

// Quiet dips between loud frames are inside the event, so they count in its
// levels. Five equal frames at 90, 90, 50, 90, 90 dB:
// 10*log10((4*10^9 + 10^5) / 5) = 89.031 dB. Without the dip it would be 90.
func TestEventLevelsIncludeQuietDips(t *testing.T) {
	f := newFeed(t, nil)
	for _, lv := range []float64{90, 90, 50, 90, 90} {
		f.d.Frame(meter.Frame{
			Start: f.next, Samples: 4800,
			LAeq: lv, LAmax: lv, LowBand: lv, HighBand: lv,
			Baseline: baselineDB, BaselineN: 600,
		})
		f.next = f.next.Add(100 * time.Millisecond)
	}
	f.send(30, quiet)
	f.wantEvents(1)
	if got := f.events[0].LAeq; math.Abs(got-89.031) > 0.001 {
		t.Errorf("LAeq = %.3f, want 89.031", got)
	}
}

func TestFlushClosesOpenEvent(t *testing.T) {
	f := newFeed(t, nil)
	f.send(5, loud)
	f.d.Flush()
	f.wantEvents(1)
}

// Levels are energy means over the event frames. Four equal frames at
// 80, 90, 80, 90 dB: 10*log10((10^8 + 10^9) / 2) = 87.404 dB.
func TestEventLevelsAreEnergyMeans(t *testing.T) {
	f := newFeed(t, nil)
	for _, lv := range []float64{80, 90, 80, 90} {
		f.d.Frame(meter.Frame{
			Start: f.next, Samples: 4800,
			LAeq: lv, LAmax: lv + 5, LowBand: lv - 10, HighBand: 50,
			Baseline: baselineDB, BaselineN: 600,
		})
		f.next = f.next.Add(100 * time.Millisecond)
	}
	f.send(30, quiet)
	f.wantEvents(1)
	e := f.events[0]
	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{"LAeq", e.LAeq, 87.404},
		{"LAmax", e.LAmax, 95},
		{"baseline at trigger", e.BaselineAtTrigger, baselineDB},
		{"low band", e.LowBand, 77.404},
		{"high band", e.HighBand, 50},
		{"low - high", e.LowHighRatioDB, 27.404},
	} {
		if math.Abs(c.got-c.want) > 0.001 {
			t.Errorf("%s = %.3f, want %.3f", c.name, c.got, c.want)
		}
	}
}

// The stored envelope covers the event plus 200 ms after its end, for the
// decay of the last impact: 0.5 s + 0.2 s = 70 samples at 100 Hz.
func TestEventKeepsEnvelope(t *testing.T) {
	f := newFeed(t, nil)
	f.send(100, quiet)
	f.send(5, loud)
	f.send(30, quiet)
	f.wantEvents(1)
	env := f.events[0].Envelope
	if len(env) != 70 {
		t.Fatalf("envelope has %d samples, want 70", len(env))
	}
	if env[0] != baselineDB+loud || env[69] != baselineDB+quiet {
		t.Errorf("envelope starts %v and ends %v, want %v and %v", env[0], env[69], baselineDB+loud, baselineDB+quiet)
	}
}

// A single hit: 30 s of quiet, one 100 ms frame 20 dB up, then quiet. The
// jump is the peak over the quiet before it, and the rise is the step from
// the quiet second to the loud one.
func TestEventMeasuresJumpAndRiseOverTheLevelBefore(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MinDuration = 100 * time.Millisecond })
	f.send(300, 0) // 30 s at the baseline
	f.send(1, 20)  // one loud frame
	f.send(30, 0)  // hangover closes it
	f.wantEvents(1)
	e := f.events[0]
	if !e.HasContext {
		t.Fatalf("HasContext = false after 30 s of frames")
	}
	if math.Abs(e.JumpDB-20) > 0.01 {
		t.Errorf("JumpDB = %.2f, want 20: the peak over the median of the 30 s before", e.JumpDB)
	}
	// The loud second holds one frame 20 dB up and nine at the baseline.
	// Its energy mean is 10*log10((10^2 + 9)/10) = 10.4 dB over the baseline.
	if e.RiseDB < 10 || e.RiseDB > 11 {
		t.Errorf("RiseDB = %.2f, want about 10.4: the step between two one-second means", e.RiseDB)
	}
}

// A slow ramp, like a fan starting: the level climbs 0.4 dB per frame, 4 dB
// per second, for 7.5 s and stays. The jump is large but no one-second step
// is, so the rise stays small.
func TestSlowRampHasSmallRise(t *testing.T) {
	f := newFeed(t, nil)
	f.send(300, 0)
	for i := 1; i <= 75; i++ {
		f.send(1, 0.4*float64(i))
	}
	f.send(30, 30)
	f.d.Flush()
	f.wantEvents(1)
	e := f.events[0]
	if e.JumpDB < 29 {
		t.Errorf("JumpDB = %.2f, want about 30", e.JumpDB)
	}
	if e.RiseDB >= 8 {
		t.Errorf("RiseDB = %.2f, want under 8 for a ramp of 0.4 dB per frame", e.RiseDB)
	}
}

// The jump is measured against the median, so a loud second inside the 30 s
// window does not move it much.
func TestJumpUsesTheMedianOfTheLevelBefore(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MinDuration = 100 * time.Millisecond })
	f.send(100, 0)
	f.send(10, 5) // one second 5 dB up, 20 s before the hit
	f.send(190, 0)
	f.send(1, 20)
	f.send(30, 0)
	f.wantEvents(1)
	if e := f.events[0]; math.Abs(e.JumpDB-20) > 0.01 {
		t.Errorf("JumpDB = %.2f, want 20", e.JumpDB)
	}
}

// Before 10 s of frames there is nothing to measure the jump against.
func TestEventWithoutHistoryHasNoContext(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MinDuration = 100 * time.Millisecond })
	f.send(50, 0)
	f.send(1, 20)
	f.send(30, 0)
	f.wantEvents(1)
	if f.events[0].HasContext {
		t.Errorf("HasContext = true after only 5 s of frames")
	}
}

// A long event keeps the level before it: the 30 s window is read when the
// event opens, not when it closes.
func TestLongEventKeepsTheLevelBeforeIt(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MaxEvent = 60 * time.Second })
	f.send(300, 0)
	f.send(450, 20) // 45 s loud, longer than the 30 s window
	f.send(30, 0)
	f.wantEvents(1)
	if e := f.events[0]; math.Abs(e.JumpDB-20) > 0.01 {
		t.Errorf("JumpDB = %.2f, want 20 over the quiet before the event", e.JumpDB)
	}
}

// A capture gap (a recording pause, a stream reset) must empty the context
// window: the first event after it must not measure its jump against frames
// from before the gap.
func TestEventAfterLongGapHasNoContext(t *testing.T) {
	f := newFeed(t, func(c *Config) { c.MinDuration = 100 * time.Millisecond })
	f.send(300, 0) // 30 s of frames, so context would otherwise be ready
	f.next = f.next.Add(2 * time.Hour)
	f.send(50, 0) // 5 s since the gap: not enough for context on its own
	f.send(1, 20) // the hit, just after frames resume
	f.send(30, 0)
	f.wantEvents(1)
	if f.events[0].HasContext {
		t.Errorf("HasContext = true across a 2 h gap")
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"zero threshold":     func(c *Config) { c.ThresholdDB = 0 },
		"negative hangover":  func(c *Config) { c.Hangover = -time.Second },
		"max below min":      func(c *Config) { c.MaxEvent = 100 * time.Millisecond },
		"no event handler":   func(c *Config) { c.OnEvent = nil },
		"no baseline needed": func(c *Config) { c.MinBaselineBins = 0 },
	} {
		cfg := DefaultConfig()
		cfg.OnEvent = func(Event) {}
		change(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New returned no error", name)
		}
	}
}
