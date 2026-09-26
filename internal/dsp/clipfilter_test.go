package dsp

import (
	"math"
	"strings"
	"testing"
)

// clipCutoffs is the list of cutoffs a clip filter may use, written out here
// rather than read from the package. A test that took the list from the code
// would pass whatever the list became.
var clipCutoffs = []float64{500, 1000, 2000, 4000, 6000, 8000, 12000}

// clipGainDB drives a new clip filter at the given cutoff with a unit sine at
// freq and returns the ratio of output power to input power in dB. It
// discards a quarter of a second, then measures two seconds. A quarter of a
// second is four times the longest FIR window, so nothing of the start-up
// reaches the measurement.
func clipGainDB(t *testing.T, cutoff, freq float64) float64 {
	t.Helper()
	f, err := NewClipFilter(cutoff)
	if err != nil {
		t.Fatalf("NewClipFilter(%g): %v", cutoff, err)
	}
	const settle = 12000
	const total = settle + 2*48000
	w := 2 * math.Pi * freq / 48000
	var in2, out2 float64
	var nIn, nOut int
	for i := 0; i < total; i++ {
		x := math.Sin(w * float64(i))
		y, ok := f.Process(x)
		if i < settle {
			continue
		}
		in2 += x * x
		nIn++
		if ok {
			out2 += y * y
			nOut++
		}
	}
	if nOut == 0 {
		t.Fatalf("no output samples at %g Hz with a %g Hz cutoff", freq, cutoff)
	}
	if out2 == 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10((out2/float64(nOut))/(in2/float64(nIn)))
}

// stopFrequencies returns the frequencies that prove the stop band of a
// filter at cutoff. The first group is fixed multiples of the cutoff, so
// every setting is tested against the same aliases. The second group is
// fixed frequencies near the top of the band, so a wide cutoff is still
// tested up to 24 kHz rather than only just above itself.
//
// Nothing above 23 900 Hz is used, because 24 kHz is the input Nyquist
// frequency. A frequency whose alias lands on 0 Hz or on the output Nyquist
// frequency is refused: there the measured power depends on the sampling
// phase and the number would mean nothing.
func stopFrequencies(t *testing.T, cutoff float64) []float64 {
	t.Helper()
	var out []float64
	seen := make(map[float64]bool)
	add := func(f float64) {
		if f <= cutoff*1.01 || f > 23900 || seen[f] {
			return
		}
		rate := 2 * cutoff
		alias := math.Abs(f - rate*math.Round(f/rate))
		if alias < 5 || cutoff-alias < 5 {
			t.Fatalf("%g Hz aliases to %g Hz at a %g Hz cutoff, too near 0 Hz or %g Hz",
				f, alias, cutoff, cutoff)
		}
		seen[f] = true
		out = append(out, f)
	}
	for _, r := range []float64{
		1.02, 1.074, 1.226, 1.554, 1.806, 2.026, 2.5, 2.976,
		4.034, 6.666, 9.554, 16.246, 24.69, 32.022, 40.11, 47.8,
	} {
		add(cutoff * r)
	}
	for _, f := range []float64{1013, 2017, 3333, 4777, 8123, 12345, 16011, 20055, 23900} {
		add(f)
	}
	return out
}

// The choices are fixed by SPEC.md section 15 decision 18. Each one must give an
// output rate that divides 48 000 exactly, or the decimation would not be a
// whole number of samples.
func TestClipLowpassChoicesAreTheAgreedCutoffs(t *testing.T) {
	if DefaultClipLowpassHz != 1000 {
		t.Errorf("DefaultClipLowpassHz = %g, want 1000", DefaultClipLowpassHz)
	}
	// The warning at startup is measured against this, not against the
	// default, so raising the default must not silence it.
	if SpeechSafeClipLowpassHz != 500 {
		t.Errorf("SpeechSafeClipLowpassHz = %g, want 500", SpeechSafeClipLowpassHz)
	}
	if len(ClipLowpassChoices) != len(clipCutoffs) {
		t.Fatalf("ClipLowpassChoices = %v, want %v", ClipLowpassChoices, clipCutoffs)
	}
	for i, want := range clipCutoffs {
		if ClipLowpassChoices[i] != want {
			t.Errorf("ClipLowpassChoices[%d] = %g, want %g", i, ClipLowpassChoices[i], want)
		}
		if 48000%int(2*want) != 0 {
			t.Errorf("a %g Hz cutoff gives a %g Hz output rate, which does not divide 48000", want, 2*want)
		}
	}
}

// A cutoff that is not on the list is refused, and the message says what may
// be used instead. Getting this wrong would put a rate on disk that the
// decimation cannot produce.
func TestNewClipFilterRefusesACutoffThatIsNotAChoice(t *testing.T) {
	for _, hz := range []float64{0, -500, 250, 750, 1500, 5000, 16000, 24000, 48000} {
		f, err := NewClipFilter(hz)
		if err == nil {
			t.Errorf("NewClipFilter(%g) returned a filter, want an error", hz)
			continue
		}
		if f != nil {
			t.Errorf("NewClipFilter(%g) returned both a filter and an error", hz)
		}
		for _, want := range []string{"500", "1000", "2000", "4000", "6000", "8000", "12000"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("NewClipFilter(%g) error %q does not list %s", hz, err, want)
			}
		}
	}
}

// The output rate is twice the cutoff, and the filter says which cutoff it
// holds. The WAV header and the clip buffer both read these back.
func TestClipFilterReportsItsRateAndCutoff(t *testing.T) {
	for _, tc := range []struct {
		cutoff float64
		rate   int
	}{
		{500, 1000}, {1000, 2000}, {2000, 4000}, {4000, 8000},
		{6000, 12000}, {8000, 16000}, {12000, 24000},
	} {
		f, err := NewClipFilter(tc.cutoff)
		if err != nil {
			t.Fatalf("NewClipFilter(%g): %v", tc.cutoff, err)
		}
		if got := f.OutputRate(); got != tc.rate {
			t.Errorf("a %g Hz filter has OutputRate %d, want %d", tc.cutoff, got, tc.rate)
		}
		if got := f.LowpassHz(); got != tc.cutoff {
			t.Errorf("a %g Hz filter has LowpassHz %g, want %g", tc.cutoff, got, tc.cutoff)
		}
	}
}

// One second of input gives exactly one second of output at the filter's own
// rate. A decimation worked out from the wrong rate shows up here first.
func TestClipFilterDecimatesToItsOwnRate(t *testing.T) {
	for _, tc := range []struct {
		cutoff  float64
		outputs int
	}{
		{500, 1000}, {1000, 2000}, {2000, 4000}, {4000, 8000},
		{6000, 12000}, {8000, 16000}, {12000, 24000},
	} {
		f, err := NewClipFilter(tc.cutoff)
		if err != nil {
			t.Fatalf("NewClipFilter(%g): %v", tc.cutoff, err)
		}
		n := 0
		for i := 0; i < 48000; i++ {
			if _, ok := f.Process(0); ok {
				n++
			}
		}
		if n != tc.outputs {
			t.Errorf("a %g Hz filter gave %d outputs for 48000 inputs, want %d", tc.cutoff, n, tc.outputs)
		}
	}
}

// SPEC.md section 3.2: nothing above the cutoff may survive in a clip, at any
// setting. This is the test that keeps that promise honest. Every frequency
// from just above the cutoff to 24 kHz must come out at least 90 dB down,
// including the ones that alias back into the passband.
func TestClipFilterRejectsEverythingAboveItsCutoff(t *testing.T) {
	for _, cutoff := range clipCutoffs {
		worst := math.Inf(-1)
		worstAt := 0.0
		for _, freq := range stopFrequencies(t, cutoff) {
			got := clipGainDB(t, cutoff, freq)
			if got > worst {
				worst, worstAt = got, freq
			}
			if got > -90 {
				t.Errorf("a %g Hz filter at %g Hz = %.1f dB, want at most -90 dB", cutoff, freq, got)
			}
		}
		t.Logf("%5.0f Hz cutoff: worst rejection %.1f dB, at %g Hz", cutoff, worst, worstAt)
	}
}

// The impact band is what the system measures, and the filter must leave it
// alone at every setting.
func TestClipFilterPassesTheImpactBand(t *testing.T) {
	for _, cutoff := range clipCutoffs {
		for _, freq := range []float64{20, 31.5, 63, 125, 250} {
			if got := clipGainDB(t, cutoff, freq); math.Abs(got) > 0.5 {
				t.Errorf("a %g Hz filter at %g Hz = %.2f dB, want 0 +/-0.5 dB", cutoff, freq, got)
			}
		}
	}
}

// The passband reaches to half the cutoff at every setting, so a wider
// cutoff really does keep more of the sound and not just a wider file.
func TestClipFilterPassesUpToHalfItsCutoff(t *testing.T) {
	for _, tc := range []struct{ cutoff, freq float64 }{
		{500, 250}, {1000, 500}, {2000, 1000}, {4000, 2000},
		{6000, 3000}, {8000, 4000}, {12000, 6000},
	} {
		if got := clipGainDB(t, tc.cutoff, tc.freq); math.Abs(got) > 0.5 {
			t.Errorf("a %g Hz filter at %g Hz = %.2f dB, want 0 +/-0.5 dB", tc.cutoff, tc.freq, got)
		}
	}
}

// Clip timestamps depend on Delay. The output produced on input sample i must
// match the input signal at time i/48000 - Delay. A 10 Hz sine moves 0.063 of
// its amplitude per millisecond, so a delay error of 0.2 ms fails this test.
func TestClipFilterDelayAlignsOutputWithInput(t *testing.T) {
	for _, cutoff := range clipCutoffs {
		f, err := NewClipFilter(cutoff)
		if err != nil {
			t.Fatalf("NewClipFilter(%g): %v", cutoff, err)
		}
		d := f.Delay()
		if d <= 0 {
			t.Fatalf("a %g Hz filter has Delay %v, want above zero", cutoff, d)
		}
		const freq = 10.0
		worst := 0.0
		for i := 0; i < 3*48000; i++ {
			x := math.Sin(2 * math.Pi * freq * float64(i) / 48000)
			y, ok := f.Process(x)
			if !ok || i < 48000 {
				continue
			}
			want := math.Sin(2 * math.Pi * freq * (float64(i)/48000 - d.Seconds()))
			worst = math.Max(worst, math.Abs(y-want))
		}
		if worst > 0.0125 {
			t.Errorf("a %g Hz filter differs from the delayed input by %.4f, want at most 0.0125 (Delay = %v)",
				cutoff, worst, d)
		}
	}
}
