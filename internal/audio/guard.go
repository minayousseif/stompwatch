package audio

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hwDevice = regexp.MustCompile(`^hw:[A-Za-z0-9_=,]+$`)

// ValidateDevice accepts only a raw ALSA hardware device such as hw:EM01,0
// (SPEC.md section 3.1). The default device, plug devices, and sound servers can
// add echo cancellation, noise suppression, or resampling without notice.
func ValidateDevice(dev string) error {
	if !hwDevice.MatchString(dev) {
		return fmt.Errorf("audio: capture device %q is not a raw hw: device; "+
			"default, plug, and sound-server devices can change the signal, so use a device like hw:EM01,0", dev)
	}
	return nil
}

// CaptureGain is the state of an ALSA capture control.
type CaptureGain struct {
	Raw int    // raw control value
	DB  string // gain in dB as amixer prints it, or empty
	On  bool   // capture switch; true when the control has no switch
	// Present is false when the device has no capture control at all, as on
	// a mic whose gain is fixed in hardware.
	Present bool
}

func (g CaptureGain) String() string {
	if !g.Present {
		return "no capture control"
	}
	s := strconv.Itoa(g.Raw)
	if g.DB != "" {
		s += " (" + g.DB + ")"
	}
	if !g.On {
		s += " off"
	}
	return s
}

// A capture line from amixer, for example:
//
//	Front Left: Capture 100 [79%] [23.00dB] [on]
var captureLine = regexp.MustCompile(`^\s*[^:]+:\s*Capture\s+(-?\d+)(?:\s+\[[^\]]*%\])?(?:\s+\[(-?[\d.]+dB)\])?(?:\s+\[(on|off)\])?\s*$`)

// CheckGain reads the output of "amixer -c CARD sget CONTROL" and compares
// the capture gain with expected, the raw value from expected_capture_gain.
// Use "none" for a device with no capture control.
//
// A gain change invalidates every measurement on both sides of it, so any
// difference, a switched-off capture, or channels with different gains is
// an error (SPEC.md section 6.2).
func CheckGain(out, expected string) (CaptureGain, error) {
	if expected == "" {
		return CaptureGain{}, errors.New("audio: expected_capture_gain is not set; " +
			"set it to the raw capture value that amixer shows, or to none")
	}

	var chans []CaptureGain
	for _, line := range strings.Split(out, "\n") {
		m := captureLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		raw, err := strconv.Atoi(m[1])
		if err != nil {
			return CaptureGain{}, fmt.Errorf("audio: reading capture gain from %q: %w", line, err)
		}
		chans = append(chans, CaptureGain{Raw: raw, DB: m[2], On: m[3] != "off", Present: true})
	}

	if len(chans) == 0 {
		if expected == "none" {
			return CaptureGain{}, nil
		}
		return CaptureGain{}, fmt.Errorf("audio: amixer shows no capture control, but expected_capture_gain is %q", expected)
	}
	g := chans[0]
	if expected == "none" {
		return g, fmt.Errorf("audio: the device has a capture control at %s, but expected_capture_gain is none", g)
	}
	for _, c := range chans[1:] {
		if c.Raw != g.Raw {
			return g, fmt.Errorf("audio: capture channels differ (%d and %d); set them to the same gain", g.Raw, c.Raw)
		}
	}
	for _, c := range chans {
		if !c.On {
			return g, errors.New("audio: capture is switched off in the mixer")
		}
	}
	want, err := strconv.Atoi(expected)
	if err != nil {
		return g, fmt.Errorf("audio: expected_capture_gain %q is not a whole number or none", expected)
	}
	if g.Raw != want {
		return g, fmt.Errorf("audio: capture gain is %s but expected_capture_gain is %d; "+
			"a gain change invalidates every measurement, so recalibrate or restore the gain", g, want)
	}
	return g, nil
}

// StuckDetector reports audio that has stopped changing: the same sample
// value, or the same 100 ms block repeated, for longer than a limit. A mic
// that has stopped otherwise looks like a very quiet night (SPEC.md section 6.1).
type StuckDetector struct {
	limit int64 // samples
	block int

	last     uint64
	haveLast bool
	runLen   int64

	hash      uint64
	pos       int
	prevHash  uint64
	havePrev  bool
	repeatLen int64
}

const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

// NewStuckDetector returns a detector for audio at rate samples per second.
func NewStuckDetector(rate int, limit time.Duration) *StuckDetector {
	return &StuckDetector{
		limit: int64(float64(rate) * limit.Seconds()),
		block: rate / 10,
		hash:  fnvOffset,
	}
}

// Observe adds samples and reports whether the audio is stuck now.
func (d *StuckDetector) Observe(x []float64) bool {
	for _, v := range x {
		bits := math.Float64bits(v)
		if d.haveLast && bits == d.last {
			d.runLen++
		} else {
			d.last, d.haveLast, d.runLen = bits, true, 1
		}

		for k := 0; k < 8; k++ {
			d.hash ^= bits >> (8 * k) & 0xFF
			d.hash *= fnvPrime
		}
		d.pos++
		if d.pos == d.block {
			if d.havePrev && d.hash == d.prevHash {
				d.repeatLen += int64(d.block)
			} else {
				d.repeatLen = 0
			}
			d.prevHash, d.havePrev = d.hash, true
			d.hash, d.pos = fnvOffset, 0
		}
	}
	return d.runLen > d.limit || d.repeatLen > d.limit
}
