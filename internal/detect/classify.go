package detect

import (
	"math"
	"slices"
	"time"

	"github.com/minayousseif/stompwatch/internal/dsp"
)

// Class is the kind of impact noise in an event.
type Class string

const (
	Unknown  Class = "unknown"
	Running  Class = "running"
	Jumping  Class = "jumping"
	Stomping Class = "stomping"
	// Impact is one short hit: a sharp rise in the low band, no cadence.
	Impact Class = "impact"
	// Steady is low-band sound that was already there or rose slowly, such
	// as a fan starting. It is not an impact.
	Steady Class = "steady"
	// Airborne is sound whose high band is as loud as its low band: a
	// television or a voice, not a hit through the ceiling.
	Airborne Class = "airborne"
)

// Gates on the levels around an event, measured on 51 owner-labeled events
// from 2026-09-13 to -15. A hit rose 17 dB over the 30 s before it; an air
// conditioner starting rose 15 dB but only 4 dB per second; a television sat
// at a ratio of -3 to -17 dB. A 0.4 s hit shares its second with 0.6 s of
// quiet, so its one-second rise reads 6-7 dB, and the rise gate sits at 5.
// A furniture drag rises 3.5 dB per second, like the fan, but its peak
// stands 7 dB over its own mean where the fan's stands 2.5 dB at most: the
// crest catches the slow events the rise cannot.
const (
	airborneMaxRatioDB = 0.0
	impactMinJumpDB    = 6.0
	impactMinRiseDB    = 5.0
	impactMinCrestDB   = 5.0
	impactMaxDuration  = 10 * time.Second
)

// Context is what the detector knows about an event beyond its envelope.
// CrestDB is LAmax minus LAeq: how far the peak stands over the event's own
// mean. Known is false when there were not 10 s of frames before the event;
// then only RatioDB is read.
type Context struct {
	RatioDB  float64
	JumpDB   float64
	RiseDB   float64
	CrestDB  float64
	Duration time.Duration
	Known    bool
}

// ClassifyEvent labels an event. The gates run first, in this order:
//
//  1. Airborne: the high band is at least as loud as the low band.
//  2. Steady: the peak is under 6 dB over the level before; or no second
//     rose 5 dB over the one before it and the peak is under 5 dB over the
//     event's mean. Needs Known.
//  3. The cadence rules of Classify.
//  4. Impact: Classify found nothing and the event is 10 s or shorter.
//     Needs Known.
//
// The confidence of airborne and steady grows with the distance from the
// gate and reaches 1 at twice it. Impact's is the smaller of the jump's
// distance and the better of the rise's and the crest's, on the same scale.
func ClassifyEvent(env []float64, c Context) (Class, float64) {
	if c.RatioDB <= airborneMaxRatioDB {
		return Airborne, math.Min(1, (airborneMaxRatioDB-c.RatioDB)/6)
	}
	if c.Known && c.JumpDB < impactMinJumpDB {
		return Steady, math.Min(1, (impactMinJumpDB-c.JumpDB)/impactMinJumpDB)
	}
	if c.Known && c.RiseDB < impactMinRiseDB && c.CrestDB < impactMinCrestDB {
		conf := math.Max((impactMinRiseDB-c.RiseDB)/impactMinRiseDB, (impactMinCrestDB-c.CrestDB)/impactMinCrestDB)
		return Steady, math.Min(1, conf)
	}
	class, conf := Classify(env)
	if class == Unknown && c.Known && c.Duration <= impactMaxDuration {
		jump := (c.JumpDB - impactMinJumpDB) / impactMinJumpDB
		rise := (c.RiseDB - impactMinRiseDB) / impactMinRiseDB
		crest := (c.CrestDB - impactMinCrestDB) / impactMinCrestDB
		return Impact, math.Min(1, math.Min(jump, math.Max(rise, crest)))
	}
	return class, conf
}

// Classification rules. All lags and widths are in envelope samples at
// dsp.EnvelopeRate (100 Hz), so 100 samples is 1 second.
const (
	// A cadence is a peak in the autocorrelation at a lag of 0.1-1 s with a
	// correlation of at least 0.5.
	cadenceLagMin = dsp.EnvelopeRate / 10
	cadenceLagMax = dsp.EnvelopeRate
	cadenceMinR   = 0.5

	// Running is a cadence of 2-3 Hz over a 3-second stretch.
	runLagMin = dsp.EnvelopeRate / 3 // 3 Hz
	runLagMax = dsp.EnvelopeRate / 2 // 2 Hz
	runWindow = 3 * dsp.EnvelopeRate
	runHop    = dsp.EnvelopeRate / 2

	// A peak is the highest point within +/-100 ms and at least 4 times the
	// median of the event envelope.
	peakHalfWidth = dsp.EnvelopeRate / 10
	peakFactor    = 4.0

	// Jumping is two or more peaks with every gap at least 1 s.
	jumpMinGap = dsp.EnvelopeRate
	// Stomping is three or more peaks with no cadence.
	stompMinPeaks = 3
)

// Classify labels an event from its impact-band envelope at 100 Hz. The rules
// are checked in this order:
//
//  1. Running: some 3-second stretch repeats at 2-3 Hz. The confidence is
//     the highest correlation found.
//  2. Unknown: the envelope repeats at some other rate between 1 Hz and
//     10 Hz, or repeats at 2-3 Hz for less than 3 seconds.
//  3. Jumping: two or more peaks, every gap at least 1 second.
//  4. Stomping: three or more peaks, some gap under 1 second.
//  5. Unknown: everything else.
//
// For jumping and stomping the confidence grows with how far the smallest
// peak stands above the median, and reaches 1 at 8 times the median.
// Unknown always has confidence 0. A wrong label is worse than no label.
func Classify(env []float64) (Class, float64) {
	if r, ok := runningCadence(env); ok {
		return Running, r
	}
	if _, _, ok := fundamental(env); ok {
		return Unknown, 0
	}

	peaks, minRatio := findPeaks(env)
	conf := math.Min(1, minRatio/(2*peakFactor))
	switch {
	case len(peaks) >= 2 && allGapsAtLeast(peaks, jumpMinGap):
		return Jumping, conf
	case len(peaks) >= stompMinPeaks:
		return Stomping, conf
	}
	return Unknown, 0
}

// runningCadence reports whether any 3-second window of env has its
// fundamental lag in the running range, and returns the best correlation.
func runningCadence(env []float64) (float64, bool) {
	best, found := 0.0, false
	for w := 0; w+runWindow <= len(env); w += runHop {
		lag, r, ok := fundamental(env[w : w+runWindow])
		if ok && lag >= runLagMin && lag <= runLagMax {
			best, found = math.Max(best, r), true
		}
	}
	return best, found
}

// fundamental returns the shortest lag between 0.1 s and 1 s where the
// autocorrelation of env has a local peak of at least 0.5. Requiring a local
// peak rejects slow swells, whose autocorrelation only falls with lag.
func fundamental(env []float64) (lag int, r float64, ok bool) {
	ac := make([]float64, cadenceLagMax+2)
	dsp.Autocorrelation(env, ac)
	for l := cadenceLagMin; l <= cadenceLagMax; l++ {
		if ac[l] >= cadenceMinR && ac[l] > ac[l-1] && ac[l] >= ac[l+1] {
			return l, ac[l], true
		}
	}
	return 0, 0, false
}

// findPeaks returns the indexes of peaks in env and the smallest ratio of a
// peak to the median. The ratio is +Inf when the median is 0.
func findPeaks(env []float64) ([]int, float64) {
	if len(env) == 0 {
		return nil, 0
	}
	sorted := slices.Clone(env)
	slices.Sort(sorted)
	median := sorted[len(sorted)/2]
	threshold := peakFactor * median

	var peaks []int
	minRatio := math.Inf(1)
	for i, v := range env {
		if v <= threshold || v <= 0 {
			continue
		}
		if isLocalMax(env, i) {
			peaks = append(peaks, i)
			minRatio = math.Min(minRatio, v/median)
		}
	}
	return peaks, minRatio
}

// isLocalMax reports whether env[i] is the highest value within
// +/-peakHalfWidth. On a flat top only the first sample counts.
func isLocalMax(env []float64, i int) bool {
	lo := max(0, i-peakHalfWidth)
	hi := min(len(env)-1, i+peakHalfWidth)
	for j := lo; j <= hi; j++ {
		if (j < i && env[j] >= env[i]) || (j > i && env[j] > env[i]) {
			return false
		}
	}
	return true
}

func allGapsAtLeast(peaks []int, gap int) bool {
	for i := 1; i < len(peaks); i++ {
		if peaks[i]-peaks[i-1] < gap {
			return false
		}
	}
	return true
}
