package dsp

import "math"

// Impact band edges in Hz. Structure-borne impact noise (footfall, jumps)
// carries most of its energy here.
const (
	ImpactBandLow  = 20.0
	ImpactBandHigh = 120.0
)

// EnvelopeRate is the output sample rate of Envelope, in Hz.
const EnvelopeRate = 100

// NewImpactBand returns a band-pass filter for the impact band: a 4th-order
// Butterworth high-pass at 20 Hz followed by a 4th-order Butterworth low-pass
// at 120 Hz.
func NewImpactBand(fs float64) *Cascade {
	hp := NewButterworthHighpass4(ImpactBandLow, fs)
	lp := NewButterworthLowpass4(ImpactBandHigh, fs)
	sections := append(append([]Biquad{}, hp.sections...), lp.sections...)
	return NewCascade(sections...)
}

// Envelope follows the loudness of the impact band over time, at 100 samples
// per second. The classifier reads cadence and isolated peaks from it.
//
// The path is: impact band-pass, full-wave rectification, a 4th-order
// Butterworth low-pass at 10 Hz, then one output for every fs/100 inputs.
// The 10 Hz low-pass is 56 dB down at 50 Hz, the Nyquist frequency at 100 Hz.
type Envelope struct {
	band   *Cascade
	smooth *Cascade
	factor int
	phase  int
}

// NewEnvelope returns an envelope follower for input sample rate fs.
// fs must be a whole multiple of 100.
func NewEnvelope(fs float64) *Envelope {
	return &Envelope{
		band:   NewImpactBand(fs),
		smooth: NewButterworthLowpass4(10, fs),
		factor: int(math.Round(fs / EnvelopeRate)),
	}
}

// Process adds one input sample. Once in every fs/100 calls it returns an
// envelope sample and true. Otherwise it returns false. It does not allocate.
func (e *Envelope) Process(x float64) (float64, bool) {
	y := e.smooth.Process(math.Abs(e.band.Process(x)))
	e.phase++
	if e.phase < e.factor {
		return 0, false
	}
	e.phase = 0
	return y, true
}
