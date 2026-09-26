package clip

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/minayousseif/stompwatch/internal/dsp"
)

var (
	// ErrExists means a clip file for the event is already on disk. Clips are
	// evidence and are never overwritten.
	ErrExists = errors.New("clip: file already exists")
	// ErrNotReady means the audio up to the requested end time has not
	// arrived yet. Try again later.
	ErrNotReady = errors.New("clip: audio up to the end time has not arrived yet")
)

// How much audio the collector keeps for clips.
//
// A clip runs from pre_roll_s before an event to post_roll_s after it, so a
// clip of about a minute needs a pre-roll of about half that. BufferLength
// is what the collector builds the recorder with and MaxPreRoll is the
// longest pre-roll the config allows; the difference is headroom for the
// event itself while the clip is still being collected. A long event keeps
// its own start through a hold, so the buffer may grow past BufferLength
// while one is open.
//
// The cost is small at the default cutoff. The buffer holds filtered audio
// at 16 bytes a sample, so a 90-second buffer of 1 kHz audio is about
// 1.4 MB. It grows with clip_lowpass_hz: at the widest cutoff the same
// buffer is about 35 MB, which the box still has room for.
const (
	BufferLength = 90 * time.Second
	MaxPreRoll   = 60 * time.Second
)

// A chunk start further than this from the sample count is a new stream.
const streamStepLimit = 100 * time.Millisecond

// Clip describes a saved audio clip.
type Clip struct {
	Path     string
	SHA256   string // hex SHA-256 of the whole file
	Bytes    int64
	Start    time.Time // time of the first sample; sample k is one period later
	Duration time.Duration
	// Truncated is true when the clip starts later than requested, because
	// the audio was no longer in memory or there was a gap in capture.
	Truncated bool
	// Clipped counts samples that were outside the 16-bit range.
	Clipped int
}

type sample struct {
	t int64 // Unix nanoseconds
	v float64
}

// Recorder keeps recent audio for clips and writes clips to disk.
//
// Raw 48 kHz audio goes in through Process, which runs it through
// dsp.ClipFilter at once. Only the filtered output is stored, so no raw
// audio stays in memory and there is no path to write unfiltered audio to
// disk (SPEC.md section 3.2). The cutoff comes from clip_lowpass_hz and the stored
// rate is twice it.
//
// The Recorder keeps the last capacity of audio. A Hold keeps audio from a
// given time until Release, so an event longer than the capacity is not lost.
//
// Process and Save may run on different goroutines. Save copies the samples
// under the lock and writes the file after it releases the lock, so disk work
// never delays Process.
type Recorder struct {
	root      string
	capacity  time.Duration
	lowpassHz float64
	rate      int
	// outputPeriod is the time between two stored samples. gapLimit is how
	// far apart two of them have to be to be on either side of a gap, and
	// compactAfter is how many old samples may sit at the front of the
	// buffer before it is compacted. All three follow the rate.
	outputPeriod time.Duration
	gapLimit     time.Duration
	compactAfter int

	mu        sync.Mutex
	filter    *dsp.ClipFilter
	started   bool
	restart   bool      // the next chunk starts a new stream
	next      time.Time // expected start time of the next chunk
	validFrom int64     // outputs before this include filter start-up
	samples   []sample
	head      int // samples[head:] are live
	holds     map[int]int64
	nextHold  int
}

// NewRecorder returns a recorder that writes clips under root and keeps
// capacity of audio in memory.
func NewRecorder(root string, capacity time.Duration, lowpassHz float64) (*Recorder, error) {
	if root == "" {
		return nil, errors.New("clip: no clip directory set")
	}
	if capacity < time.Second {
		return nil, fmt.Errorf("clip: buffer capacity %v is too short", capacity)
	}
	f, err := dsp.NewClipFilter(lowpassHz)
	if err != nil {
		return nil, err
	}
	rate := f.OutputRate()
	period := time.Second / time.Duration(rate)
	return &Recorder{
		root:         root,
		capacity:     capacity,
		lowpassHz:    lowpassHz,
		rate:         rate,
		outputPeriod: period,
		gapLimit:     period * 3 / 2,
		compactAfter: rate,
		filter:       f,
		holds:        make(map[int]int64),
	}, nil
}

// OutputRate is the rate of the stored audio, which is twice the cutoff.
func (r *Recorder) OutputRate() int { return r.rate }

// Process filters a chunk of raw 48 kHz samples and stores the result at the
// recorder's own rate.
// start is the time of the first sample. If start is within 100 ms of the
// time given by the sample count, the sample count is used, so read jitter
// does not move samples. Otherwise the chunk starts a new stream: the filter
// restarts, and its start-up output is not stored.
func (r *Recorder) Process(x []float64, start time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started && !r.restart && absDuration(start.Sub(r.next)) <= streamStepLimit {
		start = r.next
	} else {
		if r.started {
			r.filter, _ = dsp.NewClipFilter(r.lowpassHz)
		}
		r.started, r.restart = true, false
		// An output needs a full FIR window of real input on both sides.
		r.validFrom = start.Add(2 * r.filter.Delay()).UnixNano()
	}

	delay := r.filter.Delay()
	for i, s := range x {
		v, ok := r.filter.Process(s)
		if !ok {
			continue
		}
		t := start.Add(time.Duration(i)*time.Second/dsp.ClipInputRate - delay).UnixNano()
		if t >= r.validFrom {
			r.samples = append(r.samples, sample{t, v})
		}
	}
	r.next = start.Add(time.Duration(len(x)) * time.Second / dsp.ClipInputRate)
	r.trim()
}

// Reset makes the next chunk start a new stream. Call it when chunks were
// dropped: a dropped 100 ms chunk moves the next start by exactly the step
// limit, so Process cannot see the gap from the time alone.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.restart = true
}

// Latest returns the time of the newest stored sample, or the zero time if
// there is none.
func (r *Recorder) Latest() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.head >= len(r.samples) {
		return time.Time{}
	}
	return time.Unix(0, r.samples[len(r.samples)-1].t).UTC()
}

// Hold keeps audio from time from onward until Release is called with the
// returned handle. Audio already dropped before the call cannot come back.
func (r *Recorder) Hold(from time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextHold++
	r.holds[r.nextHold] = from.UnixNano()
	return r.nextHold
}

// Release ends a hold.
func (r *Recorder) Release(h int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.holds, h)
	r.trim()
}

// trim drops audio older than the capacity and not covered by a hold.
func (r *Recorder) trim() {
	live := r.samples[r.head:]
	if len(live) == 0 {
		return
	}
	cutoff := live[len(live)-1].t - int64(r.capacity)
	for _, h := range r.holds {
		cutoff = min(cutoff, h)
	}
	for r.head < len(r.samples) && r.samples[r.head].t < cutoff {
		r.head++
	}
	if r.head >= r.compactAfter {
		n := copy(r.samples, r.samples[r.head:])
		r.samples = r.samples[:n]
		r.head = 0
	}
}

// Save writes the audio from from to to as the clip for eventID, at
// root/YYYY/MM/DD/<eventID>.wav (UTC date of from). It returns ErrNotReady
// if the audio up to to has not arrived, and ErrExists if the file exists.
func (r *Recorder) Save(eventID int64, from, to time.Time) (Clip, error) {
	if !to.After(from) {
		return Clip{}, fmt.Errorf("clip: end %v is not after start %v", to, from)
	}
	values, first, truncated, err := r.extract(from.UnixNano(), to.UnixNano())
	if err != nil {
		return Clip{}, err
	}

	dir := filepath.Join(r.root, from.UTC().Format("2006"), from.UTC().Format("01"), from.UTC().Format("02"))
	path := filepath.Join(dir, fmt.Sprintf("%d.wav", eventID))
	sum, size, clipped, err := writeFileOnce(dir, path, values, r.rate)
	if err != nil {
		return Clip{}, err
	}
	return Clip{
		Path:      path,
		SHA256:    sum,
		Bytes:     size,
		Start:     time.Unix(0, first).UTC(),
		Duration:  time.Duration(int64(len(values)) * int64(time.Second) / int64(r.rate)),
		Truncated: truncated,
		Clipped:   clipped,
	}, nil
}

// extract copies the continuous run of samples in [from, to) that ends at to.
func (r *Recorder) extract(from, to int64) (values []float64, first int64, truncated bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	live := r.samples[r.head:]
	if len(live) == 0 || live[len(live)-1].t < to-int64(r.outputPeriod) {
		return nil, 0, false, ErrNotReady
	}
	i := 0
	for i < len(live) && live[i].t < from {
		i++
	}
	j := i
	for j < len(live) && live[j].t < to {
		j++
	}
	sel := live[i:j]
	if len(sel) == 0 {
		return nil, 0, false, fmt.Errorf("clip: no audio between %v and %v", time.Unix(0, from), time.Unix(0, to))
	}

	truncated = sel[0].t-from >= int64(r.outputPeriod)
	for k := len(sel) - 1; k > 0; k-- {
		if d := sel[k].t - sel[k-1].t; d > int64(r.gapLimit) || d <= 0 {
			sel = sel[k:]
			truncated = true
			break
		}
	}

	values = make([]float64, len(sel))
	for k, s := range sel {
		values[k] = s.v
	}
	return values, sel[0].t, truncated, nil
}

// writeFileOnce writes the WAV to a temporary file, syncs it, and links it to
// path. The link fails if path exists, so an existing clip is never replaced.
func writeFileOnce(dir, path string, values []float64, rate int) (sum string, size int64, clipped int, err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", 0, 0, fmt.Errorf("clip: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", 0, 0, fmt.Errorf("clip: %w", err)
	}
	defer os.Remove(tmp.Name())

	h := sha256.New()
	cw := &countWriter{w: tmp, h: h}
	clipped, err = encodeWAV(cw, values, rate)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o400)
	}
	if err != nil {
		return "", 0, 0, fmt.Errorf("clip: writing %s: %w", path, err)
	}

	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", 0, 0, fmt.Errorf("%w: %s", ErrExists, path)
		}
		return "", 0, 0, fmt.Errorf("clip: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, clipped, nil
}

type countWriter struct {
	w interface{ Write([]byte) (int, error) }
	h interface{ Write([]byte) (int, error) }
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	return n, err
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
