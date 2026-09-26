package meter

import (
	"math"
	"testing"
	"time"
)

const fs = 48000

var t0 = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)

// tone returns a sine at freq whose AES17 level is dbfs: peak amplitude
// 10^(dbfs/20), so a full-scale sine is 0 dBFS.
func tone(freq, dbfs, seconds float64) []float64 {
	a := math.Pow(10, dbfs/20)
	x := make([]float64, int(seconds*fs))
	for i := range x {
		x[i] = a * math.Sin(2*math.Pi*freq*float64(i)/fs)
	}
	return x
}

type recorder struct {
	bins   []Bin
	frames []Frame
	env    []time.Time
	steps  [][2]time.Time
}

func newTestMeter(t *testing.T, sens float64, cal Calibration) (*Meter, *recorder) {
	t.Helper()
	r := &recorder{}
	m, err := New(Config{
		SampleRate:      fs,
		SensitivityDBFS: sens,
		Calibration:     cal,
		OnBin:           func(b Bin) { r.bins = append(r.bins, b) },
		OnFrame:         func(f Frame) { r.frames = append(r.frames, f) },
		OnEnvelope:      func(ts time.Time, _ float64) { r.env = append(r.env, ts) },
		OnClockStep:     func(want, got time.Time) { r.steps = append(r.steps, [2]time.Time{want, got}) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, r
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f, want %.3f +/-%g", what, got, want, tol)
	}
}

// SPEC.md section 15 decision 1: a 1 kHz sine at the sensitivity level reads 94 dB SPL.
// The first bin holds the filter start-up, so later bins are checked.
func TestSineAtSensitivityReads94DBSPL(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(1000, -13, 3), t0)
	m.Flush()
	for _, b := range r.bins[1:] {
		near(t, "LAeq", b.LAeq, 94.0, 0.03)
	}
}

func TestFullScaleSineReads107DBSPL(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(1000, 0, 3), t0)
	m.Flush()
	near(t, "LAeq", r.bins[1].LAeq, 107.0, 0.03)
}

// A less sensitive mic setting (-10 dBFS at 94 dB SPL) means the same digital
// level is a quieter sound: -13 dBFS reads 91 dB SPL.
func TestSensitivityShiftsLevel(t *testing.T) {
	m, r := newTestMeter(t, -10, NoCalibration{})
	m.Process(tone(1000, -13, 3), t0)
	m.Flush()
	near(t, "LAeq", r.bins[1].LAeq, 91.0, 0.03)
}

func TestLevelIsLinearOver20DB(t *testing.T) {
	loud, rl := newTestMeter(t, -13, NoCalibration{})
	loud.Process(tone(1000, -20, 3), t0)
	loud.Flush()
	quiet, rq := newTestMeter(t, -13, NoCalibration{})
	quiet.Process(tone(1000, -40, 3), t0)
	quiet.Flush()
	near(t, "LAeq difference", rl.bins[1].LAeq-rq.bins[1].LAeq, 20.0, 0.01)
}

func TestCalibrationIsApplied(t *testing.T) {
	cal, err := NewScalarCalibration([]CalPoint{{FreqHz: 1000, MagDB: 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	m, r := newTestMeter(t, -13, cal)
	m.Process(tone(1000, -13, 3), t0)
	m.Flush()
	near(t, "LAeq", r.bins[1].LAeq, 93.0, 0.03)
}

// A steady sine: Fast LAmax sits on LAeq. The 2 kHz ripple of the squared
// signal is reduced to 0.06 % by the 125 ms average.
func TestLAmaxOfSteadySineEqualsLAeq(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(1000, -13, 3), t0)
	m.Flush()
	b := r.bins[1]
	if d := b.LAmax - b.LAeq; d < 0 || d > 0.05 {
		t.Errorf("LAmax - LAeq = %.3f dB, want 0 to 0.05 dB", d)
	}
}

// A 10 ms burst at 94 dB in a silent second. Fast weighting reaches
// 1 - e^(-0.010/0.125) = 0.0769 of the steady value: 94 - 11.14 = 82.86 dB.
// A raw sample peak would read 97 dB. LAeq holds 1 % of the second's time
// at 94 dB: 74.0 dB.
func TestLAmaxUsesFastTimeWeighting(t *testing.T) {
	x := make([]float64, 3*fs)
	burst := tone(1000, -13, 0.010)
	copy(x[fs+fs/2:], burst)
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(x, t0)
	m.Flush()
	near(t, "LAmax", r.bins[1].LAmax, 82.86, 0.2)
	near(t, "LAeq", r.bins[1].LAeq, 74.0, 0.2)
}

// Band levels are unweighted. A 60 Hz sine is inside the 20-120 Hz band and
// 74 dB below the 500 Hz high-pass. A 2 kHz sine is the reverse.
func TestBandLevelsSeparateLowAndHigh(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(60, -13, 3), t0)
	m.Flush()
	near(t, "60 Hz low band", r.bins[1].LowBand, 94.0, 0.05)
	if r.bins[1].HighBand > 30 {
		t.Errorf("60 Hz high band = %.1f dB, want <= 30 dB", r.bins[1].HighBand)
	}
	near(t, "60 Hz low band in frame 15", r.frames[15].LowBand, 94.0, 0.05)
	if r.frames[15].HighBand > 30 {
		t.Errorf("60 Hz high band in frame 15 = %.1f dB, want <= 30 dB", r.frames[15].HighBand)
	}

	m, r = newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(2000, -13, 3), t0)
	m.Flush()
	near(t, "2 kHz high band", r.bins[1].HighBand, 94.0, 0.05)
	if r.bins[1].LowBand > 30 {
		t.Errorf("2 kHz low band = %.1f dB, want <= 30 dB", r.bins[1].LowBand)
	}
}

// Each band edge is a Butterworth -3 dB point: 20 Hz and 120 Hz for the low
// band, 500 Hz for the high band. A tone at an edge reads 94 - 3.01 dB.
func TestBandEdgesReadMinus3DB(t *testing.T) {
	for _, tc := range []struct {
		freq float64
		band func(Bin) float64
		name string
	}{
		{20, func(b Bin) float64 { return b.LowBand }, "low band at 20 Hz"},
		{120, func(b Bin) float64 { return b.LowBand }, "low band at 120 Hz"},
		{500, func(b Bin) float64 { return b.HighBand }, "high band at 500 Hz"},
	} {
		m, r := newTestMeter(t, -13, NoCalibration{})
		m.Process(tone(tc.freq, -13, 3), t0)
		m.Flush()
		near(t, tc.name, tc.band(r.bins[2]), 90.99, 0.1)
	}
}

// Levels below one 24-bit step cannot be measured. They read as that floor:
// 20*log10(2^-23) + 3.01 + 13 + 94 = -28.46 dB SPL, never -Inf.
func TestDigitalSilenceReadsQuantizationFloor(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(make([]float64, 2*fs), t0)
	m.Flush()
	for _, b := range r.bins {
		near(t, "LAeq", b.LAeq, -28.46, 0.01)
		near(t, "LAmax", b.LAmax, -28.46, 0.01)
		near(t, "low band", b.LowBand, -28.46, 0.01)
		near(t, "high band", b.HighBand, -28.46, 0.01)
	}
}

func TestBinsAlignToWallClockSeconds(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(make([]float64, 2*fs), t0.Add(500*time.Millisecond))
	m.Flush()
	want := []struct {
		start   time.Time
		samples int
	}{
		{t0, 24000},
		{t0.Add(time.Second), 48000},
		{t0.Add(2 * time.Second), 24000},
	}
	if len(r.bins) != len(want) {
		t.Fatalf("got %d bins, want %d", len(r.bins), len(want))
	}
	for i, w := range want {
		if !r.bins[i].Start.Equal(w.start) || r.bins[i].Samples != w.samples {
			t.Errorf("bin %d = %v with %d samples, want %v with %d",
				i, r.bins[i].Start, r.bins[i].Samples, w.start, w.samples)
		}
	}
}

func TestFramesAre100Milliseconds(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(1000, -13, 1), t0)
	m.Flush()
	if len(r.frames) != 10 {
		t.Fatalf("got %d frames, want 10", len(r.frames))
	}
	for i, f := range r.frames {
		if want := t0.Add(time.Duration(i) * 100 * time.Millisecond); !f.Start.Equal(want) || f.Samples != 4800 {
			t.Errorf("frame %d = %v with %d samples, want %v with 4800", i, f.Start, f.Samples, want)
		}
	}
}

// Read timestamps from a pipe jitter by a few milliseconds. Bin contents must
// follow the sample count, not the jitter.
func TestChunkTimestampJitterDoesNotChangeBins(t *testing.T) {
	x := tone(125, -20, 3)

	whole, rw := newTestMeter(t, -13, NoCalibration{})
	whole.Process(x, t0)
	whole.Flush()

	chunked, rc := newTestMeter(t, -13, NoCalibration{})
	for k, i := 0, 0; i < len(x); k, i = k+1, i+700 {
		end := min(i+700, len(x))
		// 0, +2, -1, +1, -2 ms, repeating. The first chunk sets the
		// anchor, so it has no jitter.
		jitter := time.Duration((k*7+2)%5-2) * time.Millisecond
		start := t0.Add(time.Duration(i)*time.Second/fs + jitter)
		chunked.Process(x[i:end], start)
	}
	chunked.Flush()

	if len(rc.steps) != 0 {
		t.Fatalf("jitter of +/-2 ms reported %d clock steps, want 0", len(rc.steps))
	}
	if len(rc.bins) != len(rw.bins) {
		t.Fatalf("chunked run gave %d bins, whole run %d", len(rc.bins), len(rw.bins))
	}
	for i := range rw.bins {
		if rc.bins[i] != rw.bins[i] {
			t.Errorf("bin %d differs:\nchunked %+v\nwhole   %+v", i, rc.bins[i], rw.bins[i])
		}
	}
}

func TestForwardClockStepReanchorsAndIsReported(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(make([]float64, fs), t0)
	m.Process(make([]float64, fs), t0.Add(11*time.Second))
	m.Flush()

	if len(r.steps) != 1 || !r.steps[0][0].Equal(t0.Add(time.Second)) || !r.steps[0][1].Equal(t0.Add(11*time.Second)) {
		t.Fatalf("clock steps = %v, want one step from %v to %v", r.steps, t0.Add(time.Second), t0.Add(11*time.Second))
	}
	if len(r.bins) != 2 || !r.bins[0].Start.Equal(t0) || !r.bins[1].Start.Equal(t0.Add(11*time.Second)) {
		t.Fatalf("bins = %+v, want starts at %v and %v", r.bins, t0, t0.Add(11*time.Second))
	}
}

// samples_1s uses the second as its primary key. After the clock steps back,
// the meter must never emit a second it has already emitted, and it must not
// drop samples.
func TestBackwardClockStepNeverRepeatsASecond(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(make([]float64, 2*fs), t0)
	m.Process(make([]float64, fs), t0)
	m.Flush()

	if len(r.steps) != 1 {
		t.Fatalf("got %d clock steps, want 1", len(r.steps))
	}
	total := 0
	for i, b := range r.bins {
		total += b.Samples
		if i > 0 && !b.Start.After(r.bins[i-1].Start) {
			t.Errorf("bin %d starts at %v, not after bin %d at %v", i, b.Start, i-1, r.bins[i-1].Start)
		}
	}
	if total != 3*fs {
		t.Errorf("bins hold %d samples, want %d", total, 3*fs)
	}
}

// After Reset the meter starts fresh: no energy carries over from before.
func TestResetClearsFilterState(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(tone(1000, 0, 1), t0)
	m.Reset()
	r.bins = nil
	m.Process(make([]float64, fs), t0.Add(5*time.Second))
	m.Flush()
	if len(r.bins) != 1 {
		t.Fatalf("got %d bins after Reset, want 1", len(r.bins))
	}
	near(t, "LAmax after Reset", r.bins[0].LAmax, -28.46, 0.01)
}

// The baseline in a bin includes that bin. With two bins and the 10th
// percentile, the baseline is the quieter bin. Frames carry the baseline of
// the last closed bin.
func TestBaselineFollowsClosedBins(t *testing.T) {
	x := append(tone(1000, -40, 1), tone(1000, -13, 1)...)
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(x, t0)
	m.Flush()
	if len(r.bins) != 2 {
		t.Fatalf("got %d bins, want 2", len(r.bins))
	}
	if r.bins[0].Baseline != r.bins[0].LAeq || r.bins[1].Baseline != r.bins[0].LAeq {
		t.Errorf("baselines = %.2f, %.2f; want both %.2f", r.bins[0].Baseline, r.bins[1].Baseline, r.bins[0].LAeq)
	}
	first, second := r.frames[0], r.frames[10]
	if first.BaselineN != 0 {
		t.Errorf("first frame BaselineN = %d, want 0", first.BaselineN)
	}
	if second.BaselineN != 1 || second.Baseline != r.bins[0].LAeq {
		t.Errorf("frame 10 baseline = %.2f (n=%d), want %.2f (n=1)", second.Baseline, second.BaselineN, r.bins[0].LAeq)
	}
}

func TestEnvelopeSamplesArriveAt100Hz(t *testing.T) {
	m, r := newTestMeter(t, -13, NoCalibration{})
	m.Process(make([]float64, fs), t0)
	if len(r.env) != 100 {
		t.Fatalf("got %d envelope samples in 1 s, want 100", len(r.env))
	}
	for i := 1; i < len(r.env); i++ {
		if !r.env[i].After(r.env[i-1]) {
			t.Fatalf("envelope time %d (%v) is not after %d (%v)", i, r.env[i], i-1, r.env[i-1])
		}
	}
	if r.env[0].Before(t0) || !r.env[99].Before(t0.Add(time.Second)) {
		t.Errorf("envelope times %v to %v, want inside [%v, %v)", r.env[0], r.env[99], t0, t0.Add(time.Second))
	}
}

func TestNewRejectsUnsafeConfig(t *testing.T) {
	if _, err := New(Config{SampleRate: fs, SensitivityDBFS: -13}); err == nil {
		t.Error("New with no Calibration returned no error")
	}
	if _, err := New(Config{SampleRate: 44100, SensitivityDBFS: -13, Calibration: NoCalibration{}}); err == nil {
		t.Error("New with 44.1 kHz returned no error")
	}
}

func TestProcessDoesNotAllocate(t *testing.T) {
	m, err := New(Config{SampleRate: fs, SensitivityDBFS: -13, Calibration: NoCalibration{}})
	if err != nil {
		t.Fatal(err)
	}
	x := tone(1000, -13, 0.1)
	start := t0
	m.Process(x, start)
	allocs := testing.AllocsPerRun(50, func() {
		start = start.Add(100 * time.Millisecond)
		m.Process(x, start)
	})
	if allocs != 0 {
		t.Fatalf("Process allocates %v times per call, want 0", allocs)
	}
}
