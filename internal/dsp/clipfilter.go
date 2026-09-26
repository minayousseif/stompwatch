package dsp

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ClipInputRate is the capture rate the clip filter reads.
const ClipInputRate = 48000

// DefaultClipLowpassHz is what a fresh install uses.
//
// SPEC.md section 3.2 set it at 500 Hz so a clip could not hold intelligible
// speech. The owner raised it to 1000 Hz on 12 September 2026, because a
// 500 Hz clip of an impact is too muffled to tell one kind of noise from
// another, and the microphone is inside their own home where only impact
// noise arrives through the ceiling. SPEC.md section 15 records the decision.
const DefaultClipLowpassHz = 1000.0

// SpeechSafeClipLowpassHz is the cutoff below which a clip cannot hold
// intelligible speech. The collector warns at every start when it is set
// above this, whatever the default happens to be, because that is a
// privacy decision and the owner should never be able to forget it.
const SpeechSafeClipLowpassHz = 500.0

// ClipLowpassChoices are the cutoffs a clip filter may use. Each gives an
// output rate of twice the cutoff that divides 48000 exactly, so the
// decimation stays a whole number of samples.
var ClipLowpassChoices = []float64{500, 1000, 2000, 4000, 6000, 8000, 12000}

const (
	// The FIR passband edge sits at this fraction of the cutoff. The
	// stopband edge is the cutoff itself, which is the output Nyquist
	// frequency, so nothing that survives can alias.
	clipPassFraction = 0.8

	// The requirement is 90 dB. The design aims 5 dB higher because Kaiser's
	// length formula is an estimate.
	clipDesignAttenDB = 95.0
)

// ClipLowpassChoicesText lists the cutoffs in a form a person can read, so
// every message that refuses a cutoff names the same set.
func ClipLowpassChoicesText() string {
	out := make([]string, len(ClipLowpassChoices))
	for i, hz := range ClipLowpassChoices {
		out[i] = strconv.FormatFloat(hz, 'f', -1, 64)
	}
	return strings.Join(out, ", ")
}

// ClipFilter turns 48 kHz audio into audio at twice its cutoff that cannot
// hold any signal above that cutoff. At the default cutoff this removes
// speech intelligibility from clips (SPEC.md section 3.2).
type ClipFilter struct {
	lowpassHz float64
	rate      int
	factor    int
	lowpass   *Cascade
	taps      []float64
	hist      []float64 // each sample is stored twice, so a window is one slice
	pos       int
	phase     int
	delay     time.Duration
}

// NewClipFilter returns a filter for 48 kHz input at the given cutoff. It
// returns an error for a cutoff that is not in ClipLowpassChoices.
func NewClipFilter(lowpassHz float64) (*ClipFilter, error) {
	if !slices.Contains(ClipLowpassChoices, lowpassHz) {
		return nil, fmt.Errorf("dsp: a clip low-pass of %g Hz is not one of %s",
			lowpassHz, ClipLowpassChoicesText())
	}
	// The FIR scales with the cutoff, so a wider setting is a wider filter
	// and not a weaker one. A wider transition band needs fewer taps.
	taps := kaiserLowpass(clipPassFraction*lowpassHz, lowpassHz, clipDesignAttenDB, ClipInputRate)

	// Delay is the FIR center plus the Butterworth group delay at low
	// frequencies. For each section that group delay is 1 / (Q*w0).
	fir := float64(len(taps)-1) / 2 / ClipInputRate
	w0 := 2 * ClipInputRate * math.Tan(math.Pi*lowpassHz/ClipInputRate)
	iir := 0.0
	for k := 1; k <= 2; k++ {
		iir += 1 / (butterworthQ(4, k) * w0)
	}

	rate := int(2 * lowpassHz)
	return &ClipFilter{
		lowpassHz: lowpassHz,
		rate:      rate,
		factor:    ClipInputRate / rate,
		lowpass:   NewButterworthLowpass4(lowpassHz, ClipInputRate),
		taps:      taps,
		hist:      make([]float64, 2*len(taps)),
		delay:     time.Duration((fir + iir) * float64(time.Second)),
	}, nil
}

// OutputRate is the rate of the filtered audio, which is twice the cutoff.
func (f *ClipFilter) OutputRate() int { return f.rate }

// LowpassHz is the cutoff this filter was built with.
func (f *ClipFilter) LowpassHz() float64 { return f.lowpassHz }

// Process adds one 48 kHz sample. Once in every decimation step it returns
// an output sample and true. Otherwise it returns false. It does not
// allocate.
func (f *ClipFilter) Process(x float64) (float64, bool) {
	x = f.lowpass.Process(x)
	n := len(f.taps)
	f.hist[f.pos] = x
	f.hist[f.pos+n] = x
	f.pos++
	if f.pos == n {
		f.pos = 0
	}
	f.phase++
	if f.phase < f.factor {
		return 0, false
	}
	f.phase = 0
	window := f.hist[f.pos : f.pos+n]
	var y float64
	for i, t := range f.taps {
		y += t * window[i]
	}
	return y, true
}

// Delay is the time between an input sample and its appearance in the
// output. An output produced on the input sample at time t shows the
// signal at time t - Delay.
func (f *ClipFilter) Delay() time.Duration {
	return f.delay
}

// kaiserLowpass designs a linear-phase FIR low-pass filter with the window
// method. The cutoff is halfway between pass and stop. The length and the
// Kaiser beta come from Kaiser's formulas for attenuation attenDB. The length is
// odd, so the delay is a whole number of samples. The taps sum to 1.
func kaiserLowpass(pass, stop, attenDB, fs float64) []float64 {
	dw := 2 * math.Pi * (stop - pass) / fs
	n := int(math.Ceil((attenDB-7.95)/(2.285*dw))) + 1
	if n%2 == 0 {
		n++
	}
	beta := 0.1102 * (attenDB - 8.7)

	fc := (pass + stop) / 2 / fs // cycles per sample
	m := float64(n-1) / 2
	taps := make([]float64, n)
	sum := 0.0
	for i := range taps {
		t := float64(i) - m
		ideal := 2 * fc
		if t != 0 {
			ideal = math.Sin(2*math.Pi*fc*t) / (math.Pi * t)
		}
		r := t / m
		taps[i] = ideal * besselI0(beta*math.Sqrt(1-r*r)) / besselI0(beta)
		sum += taps[i]
	}
	for i := range taps {
		taps[i] /= sum
	}
	return taps
}

// besselI0 is the modified Bessel function of the first kind, order 0,
// computed from its power series sum ((x/2)^k / k!)^2.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	half := x / 2
	for k := 1; k < 500; k++ {
		term *= half / float64(k)
		sq := term * term
		sum += sq
		if sq < 1e-17*sum {
			break
		}
	}
	return sum
}
