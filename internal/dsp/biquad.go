package dsp

import (
	"math"
	"math/cmplx"
)

// Biquad is one second-order filter section in Direct Form II transposed.
// The coefficients are normalized so that a0 = 1:
//
//	y[n] = b0*x[n] + b1*x[n-1] + b2*x[n-2] - a1*y[n-1] - a2*y[n-2]
type Biquad struct {
	B0, B1, B2 float64
	A1, A2     float64

	s1, s2 float64
}

// Process filters one sample.
func (q *Biquad) Process(x float64) float64 {
	y := q.B0*x + q.s1
	q.s1 = q.B1*x - q.A1*y + q.s2
	q.s2 = q.B2*x - q.A2*y
	return y
}

// Reset clears the filter state. The coefficients do not change.
func (q *Biquad) Reset() {
	q.s1, q.s2 = 0, 0
}

// Response returns the complex frequency response at freq for sample rate fs.
func (q *Biquad) Response(freq, fs float64) complex128 {
	z1 := cmplx.Exp(complex(0, -2*math.Pi*freq/fs))
	z2 := z1 * z1
	num := complex(q.B0, 0) + complex(q.B1, 0)*z1 + complex(q.B2, 0)*z2
	den := 1 + complex(q.A1, 0)*z1 + complex(q.A2, 0)*z2
	return num / den
}

// bilinear maps the analog section (b2*s^2 + b1*s + b0) / (a2*s^2 + a1*s + a0)
// to a digital biquad with the bilinear transform s = 2fs*(z-1)/(z+1).
// The caller prewarps the analog frequencies if it needs to.
func bilinear(b2, b1, b0, a2, a1, a0, fs float64) Biquad {
	k := 2 * fs
	kk := k * k
	a0d := a2*kk + a1*k + a0
	return Biquad{
		B0: (b2*kk + b1*k + b0) / a0d,
		B1: 2 * (b0 - b2*kk) / a0d,
		B2: (b2*kk - b1*k + b0) / a0d,
		A1: 2 * (a0 - a2*kk) / a0d,
		A2: (a2*kk - a1*k + a0) / a0d,
	}
}

// Cascade is a series of biquad sections. The output of each section is the
// input of the next.
type Cascade struct {
	sections []Biquad
}

// NewCascade returns a cascade of copies of the given sections.
func NewCascade(sections ...Biquad) *Cascade {
	c := &Cascade{sections: make([]Biquad, len(sections))}
	copy(c.sections, sections)
	return c
}

// Process filters one sample. It does not allocate.
func (c *Cascade) Process(x float64) float64 {
	for i := range c.sections {
		x = c.sections[i].Process(x)
	}
	return x
}

// Reset clears the state of every section.
func (c *Cascade) Reset() {
	for i := range c.sections {
		c.sections[i].Reset()
	}
}

// Response returns the complex frequency response at freq for sample rate fs.
func (c *Cascade) Response(freq, fs float64) complex128 {
	h := complex(1, 0)
	for i := range c.sections {
		h *= c.sections[i].Response(freq, fs)
	}
	return h
}

// scale multiplies the overall gain of the cascade by g.
func (c *Cascade) scale(g float64) {
	s := &c.sections[0]
	s.B0 *= g
	s.B1 *= g
	s.B2 *= g
}

// HighShelf returns a shelving filter that lifts everything above fc by
// gainDB and leaves everything below it alone. At fc the lift is half of
// gainDB, which is how a shelf is defined.
//
// Playback uses one. A clip holds 20-450 Hz and most of its energy sits
// below 80 Hz, which a phone speaker cannot reproduce at all. Lifting the
// upper part moves the clip into the band a small speaker can make, without
// adding anything that was not recorded.
//
// The coefficients are the Audio EQ Cookbook high shelf with slope S = 1.
func HighShelf(fs, fc, gainDB float64) Biquad {
	a := math.Pow(10, gainDB/40)
	w := 2 * math.Pi * fc / fs
	cosw, sinw := math.Cos(w), math.Sin(w)
	alpha := sinw / 2 * math.Sqrt2
	sq := 2 * math.Sqrt(a) * alpha

	a0 := (a + 1) - (a-1)*cosw + sq
	return Biquad{
		B0: a * ((a + 1) + (a-1)*cosw + sq) / a0,
		B1: -2 * a * ((a - 1) + (a+1)*cosw) / a0,
		B2: a * ((a + 1) + (a-1)*cosw - sq) / a0,
		A1: 2 * ((a - 1) - (a+1)*cosw) / a0,
		A2: ((a + 1) - (a-1)*cosw - sq) / a0,
	}
}
