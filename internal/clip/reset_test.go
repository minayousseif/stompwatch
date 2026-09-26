package clip

import (
	"math"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/dsp"
)

// A dropped 100 ms chunk moves the next start by exactly the stream step
// limit, so the recorder cannot see it from the time alone. Reset tells it
// the next chunk starts a new stream, so the gap is kept and the clip marked.
func TestResetKeepsGapAfterDroppedChunk(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 5, silence)
	r.Reset() // the chunk at 5.0-5.1 s was dropped
	feed(r, t0.Add(5100*time.Millisecond), 5, silence)
	c, err := r.Save(1, t0.Add(2*time.Second), t0.Add(9*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || c.Start.Before(t0.Add(5100*time.Millisecond)) {
		t.Fatalf("truncated %v, start %v; want true and no earlier than 5.1 s", c.Truncated, c.Start.Sub(t0))
	}
}

// Latest lets the clip writer give up waiting when capture has stopped.
func TestLatestIsTimeOfNewestSample(t *testing.T) {
	r, _ := newRecorder(t)
	if got := r.Latest(); !got.IsZero() {
		t.Fatalf("Latest before any audio = %v, want zero", got)
	}
	feed(r, t0, 5, silence)
	f, err := dsp.NewClipFilter(500)
	if err != nil {
		t.Fatal(err)
	}
	want := t0.Add(5*time.Second - f.Delay())
	if got := r.Latest(); got.After(want) || got.Before(want.Add(-2*time.Millisecond)) {
		t.Fatalf("Latest = %v, want within 2 ms before %v", got.Sub(t0), want.Sub(t0))
	}
}

// A stream break rebuilds the filter, and the new one must have the cutoff
// the recorder was built with. A rebuild at the default would quietly narrow
// every clip after the first dropped chunk, while the WAV header went on
// saying the wider rate.
func TestResetKeepsTheConfiguredCutoff(t *testing.T) {
	r, err := NewRecorder(t.TempDir(), 30*time.Second, 1000)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	tone := func(s float64) float64 { return 0.5 * math.Sin(2*math.Pi*700*s) }
	feed(r, t0, 5, tone)
	r.Reset()
	feed(r, t0.Add(5100*time.Millisecond), 10, tone)

	from := t0.Add(7 * time.Second)
	c, err := r.Save(1, from, t0.Add(14*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if c.Truncated {
		t.Error("the clip after the restart is truncated, so the sample spacing changed")
	}
	rate, samples := readWAV(t, c.Path)
	if rate != 2000 {
		t.Errorf("stored rate after a restart = %d, want 2000", rate)
	}
	if len(samples) != 14000 {
		t.Errorf("stored %d samples, want 14000: seven seconds at 2000 Hz", len(samples))
	}
	peak := 0.0
	for _, v := range samples {
		peak = math.Max(peak, math.Abs(v))
	}
	if peak < 0.44 || peak > 0.52 {
		t.Errorf("the 700 Hz tone peaks at %.3f after a restart, want about 0.486; "+
			"the filter was rebuilt at the wrong cutoff", peak)
	}
}
