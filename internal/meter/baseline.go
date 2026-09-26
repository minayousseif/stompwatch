package meter

import (
	"math"
	"slices"
)

// Baseline is a low percentile of recent levels. It follows the ambient
// level through the day, so the detector compares against the room as it is
// now and not against a fixed number.
//
// It keeps the last window values in a ring and sorts a copy on every Add.
// At 600 values once per second that costs almost nothing.
type Baseline struct {
	ring       []float64
	sorted     []float64
	next       int
	n          int
	percentile float64
}

// NewBaseline returns a baseline over the last window values at the given
// percentile (0-100).
func NewBaseline(window int, percentile float64) *Baseline {
	return &Baseline{
		ring:       make([]float64, window),
		sorted:     make([]float64, 0, window),
		percentile: percentile,
	}
}

// Add stores v and returns the percentile of the values in the window.
// The percentile uses the nearest-rank method: the ceil(p*n/100)-th smallest.
func (b *Baseline) Add(v float64) float64 {
	b.ring[b.next] = v
	b.next = (b.next + 1) % len(b.ring)
	if b.n < len(b.ring) {
		b.n++
	}

	b.sorted = append(b.sorted[:0], b.ring[:b.n]...)
	slices.Sort(b.sorted)

	rank := int(math.Ceil(b.percentile * float64(b.n) / 100))
	rank = max(1, min(rank, b.n))
	return b.sorted[rank-1]
}

// Len is the number of values in the window.
func (b *Baseline) Len() int { return b.n }
