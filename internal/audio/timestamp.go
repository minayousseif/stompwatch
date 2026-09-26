package audio

import "time"

// Timestamper gives each chunk of audio a start time on the host clock.
//
// A read returns audio that the device captured a little earlier. That
// latency is never negative, and it jitters with scheduling. Each read
// implies a start time for the whole stream: the read time minus the samples
// received so far. The smallest implied start in a sliding window comes from
// the read with the least latency, so it is the best estimate. A chunk's
// stamp is that estimate plus the samples before the chunk. Stamps therefore
// move with the sample count, not with read jitter. Because the window
// slides, the estimate follows slow drift between the audio clock and the
// host clock.
//
// If a read implies a start more than stepLimit after the estimate, the host
// clock has jumped forward and the window starts again. A backward jump
// lowers the estimate at once.
type Timestamper struct {
	rate      int64
	window    int64 // ns
	stepLimit int64 // ns

	samples int64 // samples before the next chunk

	// obs is a queue of reads in the window, kept in increasing order of
	// implied start, so obs[head] holds the smallest.
	obs  []observation
	head int
}

type observation struct {
	read    int64 // Unix ns
	implied int64 // implied stream start, Unix ns
}

// NewTimestamper returns a timestamper for audio at rate samples per second.
func NewTimestamper(rate int, window, stepLimit time.Duration) *Timestamper {
	return &Timestamper{rate: int64(rate), window: int64(window), stepLimit: int64(stepLimit)}
}

// Reset starts a new stream.
func (t *Timestamper) Reset() {
	t.samples = 0
	t.obs = t.obs[:0]
	t.head = 0
}

// Stamp records a read of n samples that returned at read, and returns the
// start time of those n samples.
func (t *Timestamper) Stamp(read time.Time, n int) time.Time {
	r := read.UnixNano()
	end := t.samples + int64(n)
	implied := r - t.nanos(end)

	if t.head < len(t.obs) && implied-t.obs[t.head].implied > t.stepLimit {
		t.obs, t.head = t.obs[:0], 0
	}
	for t.head < len(t.obs) && r-t.obs[t.head].read > t.window {
		t.head++
	}
	for len(t.obs) > t.head && t.obs[len(t.obs)-1].implied >= implied {
		t.obs = t.obs[:len(t.obs)-1]
	}
	t.obs = append(t.obs, observation{read: r, implied: implied})
	if t.head >= 1024 {
		k := copy(t.obs, t.obs[t.head:])
		t.obs, t.head = t.obs[:k], 0
	}

	start := t.obs[t.head].implied + t.nanos(t.samples)
	t.samples = end
	return time.Unix(0, start).In(read.Location())
}

// nanos converts a sample count to nanoseconds. It splits whole seconds off
// first so the product cannot overflow on a stream that runs for weeks.
func (t *Timestamper) nanos(samples int64) int64 {
	sec, rem := samples/t.rate, samples%t.rate
	return sec*int64(time.Second) + rem*int64(time.Second)/t.rate
}
