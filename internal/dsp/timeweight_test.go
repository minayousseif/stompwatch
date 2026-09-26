package dsp

import (
	"math"
	"testing"
)

// An exponential time weighting with time constant tau reaches 1 - 1/e = 0.6321
// of a step after tau seconds.
func TestTimeWeightingStepReachesOneMinusInverseEAfterTau(t *testing.T) {
	w := NewTimeWeighting(0.125, testFS)
	var y float64
	for i := 0; i < 6000; i++ { // 0.125 s at 48 kHz
		y = w.Process(1)
	}
	if math.Abs(y-0.6321) > 0.0005 {
		t.Fatalf("value after tau = %.4f, want 0.6321", y)
	}
}

// IEC 61672-1 specifies the Fast decay rate as 34.7 dB/s:
// 10*log10(e) / 0.125 s = 34.74 dB/s.
func TestTimeWeightingFastDecaysAt34_7DBPerSecond(t *testing.T) {
	w := NewTimeWeighting(0.125, testFS)
	for i := 0; i < 48000*3; i++ {
		w.Process(1)
	}
	var y float64
	for i := 0; i < 48000; i++ {
		y = w.Process(0)
	}
	if got := 10 * math.Log10(y); math.Abs(got+34.74) > 0.05 {
		t.Fatalf("level after 1 s of silence = %.2f dB, want -34.74 dB", got)
	}
}

func TestTimeWeightingProcessDoesNotAllocate(t *testing.T) {
	w := NewTimeWeighting(0.125, testFS)
	if a := testing.AllocsPerRun(1000, func() { w.Process(0.5) }); a != 0 {
		t.Fatalf("Process allocates %v times per call, want 0", a)
	}
}
