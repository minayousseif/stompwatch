package dsp

import (
	"math"
	"math/cmplx"
	"testing"
)

// The expected impulse responses are worked out by hand from
// y[n] = b0*x[n] + b1*x[n-1] + b2*x[n-2] - a1*y[n-1] - a2*y[n-2].
func TestBiquadImpulseResponse(t *testing.T) {
	tests := []struct {
		name string
		q    Biquad
		want []float64
	}{
		{
			name: "feedforward only",
			q:    Biquad{B0: 1, B1: 2, B2: 1},
			want: []float64{1, 2, 1, 0, 0},
		},
		{
			name: "a1 feedback sign",
			q:    Biquad{B0: 1, A1: -0.5},
			want: []float64{1, 0.5, 0.25, 0.125, 0.0625},
		},
		{
			name: "a2 feedback sign",
			q:    Biquad{B0: 1, A2: 0.25},
			want: []float64{1, 0, -0.25, 0, 0.0625},
		},
		{
			name: "mixed b and a",
			q:    Biquad{B0: 0.5, B1: 0.25, A1: -0.5},
			want: []float64{0.5, 0.5, 0.25, 0.125, 0.0625},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.q
			for i, want := range tc.want {
				x := 0.0
				if i == 0 {
					x = 1
				}
				if got := q.Process(x); math.Abs(got-want) > 1e-12 {
					t.Fatalf("y[%d] = %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestBiquadResetClearsState(t *testing.T) {
	q := Biquad{B0: 1, A1: -0.5}
	q.Process(1)
	q.Process(0)
	q.Reset()
	if got := q.Process(0); got != 0 {
		t.Fatalf("output after Reset with zero input = %v, want 0", got)
	}
}

// A cascade of two sections must equal the second applied to the output of
// the first: 1, 0.5, 0.25 ... convolved with 1, 0.5, 0.25 ... gives (n+1)/2^n.
func TestCascadeAppliesSectionsInSeries(t *testing.T) {
	c := NewCascade(Biquad{B0: 1, A1: -0.5}, Biquad{B0: 1, A1: -0.5})
	want := []float64{1, 1, 0.75, 0.5, 0.3125}
	for i, w := range want {
		x := 0.0
		if i == 0 {
			x = 1
		}
		if got := c.Process(x); math.Abs(got-w) > 1e-12 {
			t.Fatalf("y[%d] = %v, want %v", i, got, w)
		}
	}
}

func TestCascadeProcessDoesNotAllocate(t *testing.T) {
	c := NewAWeighting(testFS)
	allocs := testing.AllocsPerRun(1000, func() {
		c.Process(0.1)
	})
	if allocs != 0 {
		t.Fatalf("Process allocates %v times per call, want 0", allocs)
	}
}

// A high shelf lifts everything above its corner by a set amount and leaves
// everything below it alone. Playback uses one so a small speaker, which
// cannot make 20-80 Hz at all, reproduces the part of a clip it can.
func TestHighShelfLiftsTheTopAndLeavesTheBottom(t *testing.T) {
	const fs = 1000
	q := HighShelf(fs, 200, 12)

	cases := []struct {
		freq, want float64 // dB
	}{
		{10, 0},   // far below the corner: untouched
		{25, 0},   // still below
		{200, 6},  // at the corner: half the lift, as a shelf is defined
		{450, 12}, // well above: the full lift
	}
	for _, c := range cases {
		got := 20 * math.Log10(cmplx.Abs(q.Response(c.freq, fs)))
		if math.Abs(got-c.want) > 1.0 {
			t.Errorf("%g Hz: %.2f dB, want %.2f dB", c.freq, got, c.want)
		}
	}
}

// A shelf with no lift must pass the signal through unchanged, so the flat
// option costs nothing.
func TestHighShelfWithNoGainIsFlat(t *testing.T) {
	q := HighShelf(1000, 200, 0)
	for _, f := range []float64{10, 100, 200, 450} {
		got := 20 * math.Log10(cmplx.Abs(q.Response(f, 1000)))
		if math.Abs(got) > 1e-9 {
			t.Errorf("%g Hz: %.3g dB, want 0", f, got)
		}
	}
}
