package meter

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Calibration corrects the microphone's own frequency response. The meter
// passes every raw sample through Correct before any weighting.
//
// Phase 1 uses ScalarCalibration, one broadband gain. A per-band FIR
// correction can implement the same interface later without changes to the
// meter.
type Calibration interface {
	Correct(x float64) float64
}

// NoCalibration applies no correction. Use it only when no calibration file
// is configured, and log that the readings are uncorrected.
type NoCalibration struct{}

// Correct returns x unchanged.
func (NoCalibration) Correct(x float64) float64 { return x }

// CalPoint is one row of a calibration file.
type CalPoint struct {
	FreqHz   float64
	MagDB    float64
	PhaseDeg float64
}

// ParseREW reads a calibration file in REW text format: one row per line,
// "freq_hz magnitude_db [phase_deg]", separated by spaces, tabs, or commas.
// Blank lines and lines that start with * or # are skipped. Header lines
// before the first data row are skipped too, because some files start with
// column names, such as the EM-01's "X Y" and "Hz dB".
//
// After the first data row, any line that is not a data row stops parsing
// with an error that names the line. Such a line may mean the file is in a
// different format, and guessing would give wrong levels.
func ParseREW(r io.Reader) ([]CalPoint, error) {
	sc := bufio.NewScanner(r)
	var points []CalPoint
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\uFEFF"))
		if s == "" || strings.HasPrefix(s, "*") || strings.HasPrefix(s, "#") {
			continue
		}
		fields := strings.FieldsFunc(s, func(r rune) bool {
			return r == ',' || unicode.IsSpace(r)
		})
		p, err := parsePoint(fields)
		if err != nil {
			if len(points) == 0 {
				continue // a header line before the data
			}
			return nil, fmt.Errorf("calibration file line %d: %w", line, err)
		}
		points = append(points, p)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading calibration file: %w", err)
	}
	if len(points) == 0 {
		return nil, errors.New("calibration file has no data rows")
	}
	return points, nil
}

// parsePoint reads one data row: 2 or 3 numbers.
func parsePoint(fields []string) (CalPoint, error) {
	if len(fields) < 2 || len(fields) > 3 {
		return CalPoint{}, fmt.Errorf("want 2 or 3 numbers, got %d fields", len(fields))
	}
	var v [3]float64
	for i, f := range fields {
		n, err := strconv.ParseFloat(f, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return CalPoint{}, fmt.Errorf("%q is not a number", f)
		}
		v[i] = n
	}
	return CalPoint{FreqHz: v[0], MagDB: v[1], PhaseDeg: v[2]}, nil
}

// The frequency range whose mean deviation sets the broadband correction.
const (
	calBandLowHz  = 800.0
	calBandHighHz = 1250.0
)

// ScalarCalibration removes the mean deviation of the microphone between
// 800 Hz and 1250 Hz as one broadband gain.
type ScalarCalibration struct {
	offsetDB float64
	gain     float64
}

// NewScalarCalibration builds the correction from calibration file rows.
// It returns an error if no row falls between 800 Hz and 1250 Hz.
func NewScalarCalibration(points []CalPoint) (*ScalarCalibration, error) {
	var sum float64
	var n int
	for _, p := range points {
		if p.FreqHz >= calBandLowHz && p.FreqHz <= calBandHighHz {
			sum += p.MagDB
			n++
		}
	}
	if n == 0 {
		return nil, fmt.Errorf("calibration file has no rows between %g Hz and %g Hz", calBandLowHz, calBandHighHz)
	}
	offset := -sum / float64(n)
	return &ScalarCalibration{offsetDB: offset, gain: math.Pow(10, offset/20)}, nil
}

// Correct applies the broadband gain to one sample.
func (c *ScalarCalibration) Correct(x float64) float64 { return x * c.gain }

// OffsetDB is the correction in dB. It is the negative of the mean deviation.
func (c *ScalarCalibration) OffsetDB() float64 { return c.offsetDB }
