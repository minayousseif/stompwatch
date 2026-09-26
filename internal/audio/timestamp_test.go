package audio

import (
	"math/rand/v2"
	"testing"
	"time"
)

var ts0 = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)

// span is the duration of n samples at 48 kHz. Multiples of 48 are exact.
func span(n int) time.Duration { return time.Duration(n) * time.Second / SampleRate }

func newTestTimestamper() *Timestamper {
	return NewTimestamper(SampleRate, 30*time.Second, time.Second)
}

// A read returns data that ended some latency before the read. With a
// constant latency, a chunk's stamp is its true start plus that latency.
func TestTimestamperWithConstantLatency(t *testing.T) {
	ts := newTestTimestamper()
	const latency = 20 * time.Millisecond
	for i, samples := 0, 0; i < 100; i, samples = i+1, samples+4800 {
		got := ts.Stamp(ts0.Add(span(samples+4800)+latency), 4800)
		if want := ts0.Add(span(samples) + latency); !got.Equal(want) {
			t.Fatalf("read %d: stamp %v, want %v", i, got, want)
		}
	}
}

// Read latency jitters between 5 and 125 ms. Once the 30 s window is full,
// stamps must sit within 5 ms of the smallest latency and move with the
// sample count, not with the jitter.
func TestTimestamperIgnoresReadJitter(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	ts := newTestTimestamper()
	var prev time.Time
	for i, samples := 0, 0; i < 600; i, samples = i+1, samples+4800 {
		latency := 5*time.Millisecond + time.Duration(rng.Int64N(int64(120*time.Millisecond)))
		got := ts.Stamp(ts0.Add(span(samples+4800)+latency), 4800)
		if i >= 300 {
			if off := got.Sub(ts0.Add(span(samples))); off < 5*time.Millisecond || off > 10*time.Millisecond {
				t.Fatalf("read %d: stamp is %v after the true start, want 5-10 ms", i, off)
			}
			if step := got.Sub(prev) - 100*time.Millisecond; step < -5*time.Millisecond || step > 5*time.Millisecond {
				t.Fatalf("read %d: stamp moved %v more than the sample count", i, step)
			}
		}
		prev = got
	}
}

func TestTimestamperFollowsHostClockSteps(t *testing.T) {
	for _, jump := range []time.Duration{10 * time.Second, -10 * time.Second} {
		ts := newTestTimestamper()
		const latency = 20 * time.Millisecond
		samples := 0
		for ; samples < 100*4800; samples += 4800 {
			ts.Stamp(ts0.Add(span(samples+4800)+latency), 4800)
		}
		got := ts.Stamp(ts0.Add(jump+span(samples+4800)+latency), 4800)
		if want := ts0.Add(jump + span(samples) + latency); !got.Equal(want) {
			t.Errorf("host clock step %v: stamp %v, want %v", jump, got, want)
		}
	}
}

// The USB audio clock and the host clock drift apart. At 50 ppm for one hour
// the sample count falls 180 ms behind the host. Stamps must follow the host
// clock, lagging at most by the drift over the 30 s window (1.5 ms).
func TestTimestamperTracksClockDrift(t *testing.T) {
	ts := newTestTimestamper()
	const latency = 20 * time.Millisecond
	host := func(samples int) time.Time {
		return ts0.Add(time.Duration(float64(span(samples)) * (1 + 50e-6)))
	}
	var got time.Time
	samples := 0
	for ; samples < 3600*SampleRate; samples += 4800 {
		got = ts.Stamp(host(samples+4800).Add(latency), 4800)
	}
	start := samples - 4800
	if off := got.Sub(host(start).Add(latency)); off < -2*time.Millisecond || off > 2*time.Millisecond {
		t.Fatalf("after one hour of 50 ppm drift, stamp is %v from the host time, want within +/-2 ms", off)
	}
}

func TestTimestamperResetStartsNewStream(t *testing.T) {
	ts := newTestTimestamper()
	const latency = 20 * time.Millisecond
	for samples := 0; samples < 50*4800; samples += 4800 {
		ts.Stamp(ts0.Add(span(samples+4800)+latency), 4800)
	}
	ts.Reset()
	later := ts0.Add(time.Hour)
	if got, want := ts.Stamp(later.Add(span(4800)+latency), 4800), later.Add(latency); !got.Equal(want) {
		t.Fatalf("first stamp after Reset = %v, want %v", got, want)
	}
}

// A quick restart leaves a gap shorter than the step limit. After Reset the
// gap must stay in the stamps.
func TestTimestamperResetKeepsShortGap(t *testing.T) {
	ts := newTestTimestamper()
	const latency = 20 * time.Millisecond
	samples := 0
	for ; samples < 50*4800; samples += 4800 {
		ts.Stamp(ts0.Add(span(samples+4800)+latency), 4800)
	}
	ts.Reset()
	restart := ts0.Add(span(samples) + 300*time.Millisecond)
	for k := 0; k < 3; k++ {
		got := ts.Stamp(restart.Add(span((k+1)*4800)+latency), 4800)
		if want := restart.Add(span(k*4800) + latency); !got.Equal(want) {
			t.Fatalf("chunk %d after restart: stamp %v, want %v", k, got, want)
		}
	}
}

// Multiplying a sample count by 10^9 overflows int64 after 9,223,372,036
// samples (53 h). Once both terms of a stamp have wrapped they cancel, so only
// the chunk that crosses that point can go wrong. This test stamps that chunk.
func TestTimestamperStaysCorrectAcrossOverflowPoint(t *testing.T) {
	ts := newTestTimestamper()
	const latency = 20 * time.Millisecond
	const n1 = 192153534 * 48 // 9,223,369,632 samples, exactly 192153.534 s
	d1 := 192153534 * time.Millisecond
	ts.Stamp(ts0.Add(d1+latency), n1)
	got := ts.Stamp(ts0.Add(d1+span(4800)+latency), 4800)
	if want := ts0.Add(d1 + latency); !got.Equal(want) {
		t.Fatalf("stamp of the chunk that crosses 2^63 ns = %v, want %v", got, want)
	}
}
