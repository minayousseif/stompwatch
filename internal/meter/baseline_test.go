package meter

import (
	"math"
	"testing"
)

// Nearest rank: the p-th percentile of n values is the ceil(p*n/100)-th
// smallest. For p = 10 and n = 24, p*n/100 = 2.4, so it is the 3rd smallest.
// Rounding down or to nearest would give the 2nd.
func TestBaselineUsesNearestRankPercentile(t *testing.T) {
	b := NewBaseline(600, 10)
	var got float64
	for v := 24; v >= 1; v-- {
		got = b.Add(float64(v))
	}
	if got != 3 {
		t.Fatalf("10th percentile of 1..24 = %v, want 3", got)
	}
}

func TestBaselineForgetsValuesOutsideWindow(t *testing.T) {
	b := NewBaseline(5, 10)
	var got float64
	for v := 1; v <= 7; v++ {
		got = b.Add(float64(v))
	}
	if got != 3 {
		t.Fatalf("10th percentile of the last 5 of 1..7 = %v, want 3", got)
	}
}

// A ramp from 30 to 40 dB over one hour, one value per second. With a
// 600-value window, the 10th percentile is the 60th smallest, which is the
// value from 540 s ago: ramp(t) - 1.5 dB.
func TestBaselineTracksSlowRamp(t *testing.T) {
	b := NewBaseline(600, 10)
	ramp := func(t int) float64 { return 30 + 10*float64(t)/3600 }
	for s := 0; s < 3600; s++ {
		got := b.Add(ramp(s))
		if s == 1000 || s == 2000 || s == 3599 {
			if want := ramp(s - 540); math.Abs(got-want) > 1e-9 {
				t.Errorf("baseline at %d s = %.4f, want %.4f", s, got, want)
			}
		}
	}
}

// With 600 values the 60th smallest decides. Loud values do not move the
// baseline while at least 60 quiet values remain in the window.
func TestBaselineIgnoresLoudPeriodUntilQuietValuesRunOut(t *testing.T) {
	b := NewBaseline(600, 10)
	for i := 0; i < 600; i++ {
		b.Add(30)
	}
	var got float64
	for i := 1; i <= 540; i++ {
		got = b.Add(80)
	}
	if got != 30 {
		t.Fatalf("baseline after 540 loud values = %v, want 30", got)
	}
	if got = b.Add(80); got != 80 {
		t.Fatalf("baseline after 541 loud values = %v, want 80", got)
	}
}

func TestBaselineLenCountsValuesUpToWindow(t *testing.T) {
	b := NewBaseline(5, 10)
	for i, want := range []int{1, 2, 3, 4, 5, 5, 5} {
		b.Add(float64(i))
		if got := b.Len(); got != want {
			t.Fatalf("Len after %d values = %d, want %d", i+1, got, want)
		}
	}
}
