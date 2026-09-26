package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/minayousseif/stompwatch/internal/dsp"
	"github.com/minayousseif/stompwatch/internal/media"
	"github.com/minayousseif/stompwatch/internal/store"
)

const (
	// playbackMinRate is the rate a clip is raised to for playback.
	// Browsers refuse to play below 3 kHz, so every clip goes to at least
	// this, whatever rate it was stored at.
	playbackMinRate = 8000
	// maxPlaybackFactor caps the rise. Nothing this collector writes needs
	// more than eight, and the cap keeps the kernel table small.
	maxPlaybackFactor = 32
	// waveformBuckets is the default number of peaks in a waveform.
	waveformBuckets = 800
	// fullScale is the value a sample of 1.0 is stored as. The peaks are
	// divided by it so a waveform runs from -1 to 1.
	fullScale = 32768
	// playbackTargetDB is the peak level playback is raised to. A clip is
	// a faithful recording of a quiet room, so a household impact sits tens
	// of dB below full scale and cannot be heard on a phone. Raising it to
	// just under the limit leaves a decibel of headroom, because the
	// interpolation between two stored samples can overshoot both of them.
	playbackTargetDB = -1.0
	// maxPlaybackGainDB caps the rise. A clip with nothing in it but the
	// quantization step would otherwise be amplified into hiss, which says
	// nothing about the night and sounds like a fault.
	maxPlaybackGainDB = 40.0
	// playbackTiltHz and playbackTiltDB shape the clip for a small speaker.
	// Most of a clip's energy sits below 80 Hz, which a phone speaker cannot
	// reproduce at all. Lifting the upper part moves the clip into the band
	// such a speaker can make. It adds nothing that was not recorded, and
	// tone=flat turns it off.
	playbackTiltHz = 200.0
	playbackTiltDB = 12.0
)

// playbackTones are the shapes the playback stream may be asked for.
var playbackTones = []string{"tilt", "flat"}

// tilt lifts the upper part of a clip for a small speaker, and returns the
// recorded balance unchanged when the caller asked for it.
//
// The result is float64 and is never clamped. A 12 dB lift on a clip that
// is already loud goes past the 16-bit limit, and rounding it here would be
// a second limiter that distorts the clip before the real one scales it to
// fit. Everything stays in floating point until the last step.
//
// The shelf runs at the clip's own rate. A clip recorded before
// clip_lowpass_hz was changed keeps its old rate, so the rate has to come
// from the clip and not from a setting (SPEC.md section 15 decision 18).
func tilt(in []int16, tone string, rate int) []float64 {
	out := make([]float64, len(in))
	if tone == "flat" {
		for i, v := range in {
			out[i] = float64(v)
		}
		return out
	}
	q := dsp.HighShelf(float64(rate), playbackTiltHz, playbackTiltDB)
	for i, v := range in {
		out[i] = q.Process(float64(v))
	}
	return out
}

// peakDBFS is the loudest sample of a signal, in dBFS. It is minus infinity
// for digital silence, which has no peak.
func peakDBFS[T ~int16 | ~float64](x []T) float64 {
	peak := 0.0
	for _, v := range x {
		peak = math.Max(peak, math.Abs(float64(v)))
	}
	if peak == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(peak/fullScale)
}

// gainToTarget is the change in dB that puts a peak on the playback target.
//
// It goes down as well as up. A clip that is already loud, lifted 12 dB by
// the tilt, would otherwise run into the limiter and come out with the
// harmonics of a clipped sine in it. Bringing it down instead costs
// nothing: this is the stream for listening, and the stored file and the
// measured level are untouched.
func gainToTarget(peakDB float64) float64 {
	if math.IsInf(peakDB, -1) {
		return maxPlaybackGainDB // digital silence: nothing to scale
	}
	return math.Max(math.Min(playbackTargetDB-peakDB, maxPlaybackGainDB), -maxPlaybackGainDB)
}

// --- playback ---

func (s *Server) handleEventAudio(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the event number must be a whole number above zero.")
		return
	}
	p := parseQuery(r, "tone")
	tone := p.one("tone", "tilt", playbackTones...)
	if !p.ok(w) {
		return
	}
	m, path, okFile := s.clipFile(w, r, id)
	if !okFile {
		return
	}
	body, mod, err := s.playbackClip(m, path, tone)
	if err != nil {
		s.serverError(w, "preparing a clip for playback", err)
		return
	}
	// ServeContent answers a range request, so the browser can seek in a long
	// clip instead of downloading all of it before the first note.
	w.Header().Set("Content-Type", "audio/wav")
	http.ServeContent(w, r, "", mod, bytes.NewReader(body))
}

// playbackClip returns the resampled clip, from the cache when it is there.
func (s *Server) playbackClip(m store.Media, path, tone string) ([]byte, time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	// The two tones are different bodies, so they are cached apart. The id
	// is doubled rather than made a struct key, because the cache is keyed
	// by one number and a clip of each tone is still only two entries.
	key := m.EventID * 2
	if tone == "flat" {
		key++
	}
	if body, ok := s.clips.get(key, fi.ModTime()); ok {
		return body, fi.ModTime(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	rate, samples, err := decodeWAV(raw)
	if err != nil {
		return nil, time.Time{}, err
	}
	body, _ := buildPlayback(samples, rate, tone)
	s.clips.put(key, fi.ModTime(), body)
	return body, fi.ModTime(), nil
}

// buildPlayback shapes a stored clip for listening and returns the WAV and
// the gain it applied.
//
// The level is measured on the finished signal, after the tilt and after
// the interpolation. Measuring the stored samples instead would miss both:
// the shelf rings at the abrupt start of an extract, and the interpolation
// overshoots a sharp edge. Either would push the clip into the limiter.
func buildPlayback(stored []int16, rate int, tone string) ([]byte, float64) {
	factor := playbackFactorFor(rate)
	raised := upsample(tilt(stored, tone, rate), factor)
	gainDB := gainToTarget(peakDBFS(raised))
	return encodeWAV(quantize(raised, math.Pow(10, gainDB/20)), rate*factor), gainDB
}

// playbackFactorFor is the smallest power of two that brings a stored rate
// up to the rate a browser will play. A 1 kHz clip needs eight; a clip
// recorded at a wider cutoff needs less, and an 8 kHz one needs none.
func playbackFactorFor(rate int) int {
	factor := 1
	for rate*factor < playbackMinRate && factor < maxPlaybackFactor {
		factor *= 2
	}
	return factor
}

// quantize scales a signal and rounds it to 16 bits. Scaling here rather
// than before the interpolation keeps a very quiet clip at full precision
// until the last step.
func quantize(x []float64, gain float64) []int16 {
	out := make([]int16, len(x))
	for i, v := range x {
		out[i] = clamp16(v * gain)
	}
	return out
}

// kernelHalf is how many stored samples the interpolation reads on each side
// of the output sample it is working out. Twelve taps at eight phases is a
// 96-tap filter, which puts the first image of a 1 kHz clip, at 800 Hz, well
// inside the stop band. A clip stored at a higher rate needs a smaller
// factor and so fewer taps, and its images start further out anyway.
const kernelHalf = 6

// playbackKernels holds one set of taps per output phase, for each factor a
// clip may need. Straight linear interpolation was measured first and left
// the images only 23 dB below the signal, which is not enough to keep
// speech out of a clip the collector already filtered. A windowed sinc is a
// real low-pass filter and was measured holding them 84 dB down.
//
// The kernel depends on the factor, so the table is built once at start
// rather than per clip. There are only six of them and the largest is 384
// numbers.
var playbackKernels = buildPlaybackKernels()

func buildPlaybackKernels() map[int][][2 * kernelHalf]float64 {
	out := make(map[int][][2 * kernelHalf]float64)
	for factor := 1; factor <= maxPlaybackFactor; factor *= 2 {
		k := make([][2 * kernelHalf]float64, factor)
		for p := range factor {
			frac := float64(p) / float64(factor)
			var sum float64
			for idx := range 2 * kernelHalf {
				// Tap idx reads stored sample i+idx-(kernelHalf-1), which
				// sits this far from the output sample, in stored samples.
				u := float64(idx-(kernelHalf-1)) - frac
				k[p][idx] = sinc(u) * blackman(u)
				sum += k[p][idx]
			}
			// Normalize so a steady level comes out at the level it went in.
			for idx := range k[p] {
				k[p][idx] /= sum
			}
		}
		out[factor] = k
	}
	return out
}

func sinc(u float64) float64 {
	if u == 0 {
		return 1
	}
	return math.Sin(math.Pi*u) / (math.Pi * u)
}

// blackman tapers the sinc to nothing at the ends of the kernel, which is
// what buys the stop band.
func blackman(u float64) float64 {
	if u <= -kernelHalf || u >= kernelHalf {
		return 0
	}
	return 0.42 + 0.5*math.Cos(math.Pi*u/kernelHalf) + 0.08*math.Cos(2*math.Pi*u/kernelHalf)
}

// upsample raises the rate by factor. It returns float64 so a very quiet
// clip is not rounded to 16 bits before it is scaled.
//
// Output sample i*factor+p sits p factor-ths of the way between stored
// sample i and stored sample i+1, and is worked out from the stored samples
// around it. Past either end of the clip there is nothing to read, so the
// end sample is held: the last outputs run off the end and settle on the
// final stored sample. A factor of one copies the clip through, because
// then the only phase is zero and the kernel is a single tap.
func upsample(in []float64, factor int) []float64 {
	kernel := playbackKernels[factor]
	out := make([]float64, len(in)*factor)
	for i := range in {
		for p := range factor {
			var acc float64
			for idx, h := range kernel[p] {
				acc += h * held(in, i+idx-(kernelHalf-1))
			}
			out[i*factor+p] = acc
		}
	}
	return out
}

// held reads a stored sample, holding the first and last one beyond the ends
// of the clip.
func held(in []float64, i int) float64 {
	return in[min(max(i, 0), len(in)-1)]
}

func clamp16(v float64) int16 {
	q := math.Round(v)
	switch {
	case math.IsNaN(q):
		return 0
	case q > 32767:
		return 32767
	case q < -32768:
		return -32768
	}
	return int16(q)
}

// --- the original file ---

func (s *Server) handleEventAudioOriginal(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the event number must be a whole number above zero.")
		return
	}
	if p := parseQuery(r); !p.ok(w) {
		return
	}
	m, path, okFile := s.clipFile(w, r, id)
	if !okFile {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.clipIsMissing(w, m)
		return
	}
	defer f.Close()

	// The hash is checked before a single byte of the body goes out. A
	// mismatch found half way through a response cannot be reported: the
	// status line has already said 200 and the browser has already saved
	// part of the file.
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		s.serverError(w, "reading a clip", err)
		return
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != m.SHA256 {
		s.log.Error("a clip no longer matches the hash recorded when it was written",
			"event", m.EventID, "kind", m.Kind)
		s.cfg.RecordHealth(s.now(), store.HealthWriteError,
			"event "+itoa64(m.EventID)+": the "+m.Kind+" clip no longer matches its hash", 0)
		fail(w, http.StatusConflict,
			"that clip no longer matches the hash recorded when it was written, "+
				"so it cannot be used as evidence. It is in the health log.")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.serverError(w, "reading a clip", err)
		return
	}
	fi, err := f.Stat()
	if err != nil {
		s.serverError(w, "reading a clip", err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="event-%d.wav"`, m.EventID))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// --- the waveform ---

type waveformJSON struct {
	Buckets    int          `json:"buckets"`
	DurationMS int64        `json:"duration_ms"`
	Peaks      [][2]float64 `json:"peaks"`
}

func (s *Server) handleEventWaveform(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the event number must be a whole number above zero.")
		return
	}
	p := parseQuery(r, "buckets")
	buckets := p.intIn("buckets", waveformBuckets, 100, 4000)
	if !p.ok(w) {
		return
	}
	_, path, okFile := s.clipFile(w, r, id)
	if !okFile {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		s.serverError(w, "reading a clip", err)
		return
	}
	// The peaks come from the stored clip, not the one raised for playback.
	// The drawing must show what was measured.
	rate, samples, err := decodeWAV(raw)
	if err != nil {
		s.serverError(w, "reading a clip", err)
		return
	}
	out := waveformJSON{
		DurationMS: int64(len(samples)) * 1000 / int64(rate),
		Peaks:      peaks(samples, buckets),
	}
	out.Buckets = len(out.Peaks)
	writeJSON(w, http.StatusOK, out)
}

// peaks reduces samples to at most n pairs of the lowest and highest value in
// each bucket. Fewer samples than buckets gives one bucket per sample: an
// empty bucket would draw as silence the clip does not hold.
func peaks(in []int16, n int) [][2]float64 {
	if len(in) < n {
		n = len(in)
	}
	out := make([][2]float64, 0, n)
	for b := range n {
		lo, hi := b*len(in)/n, (b+1)*len(in)/n
		low, high := in[lo], in[lo]
		for _, v := range in[lo:hi] {
			low, high = min(low, v), max(high, v)
		}
		// Four decimal places is as fine as a drawing can show, and it keeps
		// a 4000-bucket response small.
		out = append(out, [2]float64{round4(float64(low) / fullScale), round4(float64(high) / fullScale)})
	}
	return out
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// --- shared ---

// clipFile finds the clip of an event and reports a missing one. It never
// lets the path reach the caller.
func (s *Server) clipFile(w http.ResponseWriter, r *http.Request, id int64) (store.Media, string, bool) {
	m, err := s.cfg.Store.MediaFile(r.Context(), id, store.KindAudio)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "there is no audio clip for that event.")
		return store.Media{}, "", false
	}
	if err != nil {
		s.serverError(w, "reading the clip of an event", err)
		return store.Media{}, "", false
	}
	if m.Purged != nil {
		s.clipWasPurged(w, m)
		return store.Media{}, "", false
	}
	path, err := resolveMedia(s.cfg.ClipDir, m.Path)
	if err != nil {
		s.serverError(w, "finding the clip of an event", err)
		return store.Media{}, "", false
	}
	if _, err := os.Stat(path); err != nil {
		s.clipIsMissing(w, m)
		return store.Media{}, "", false
	}
	return m, path, true
}

// resolveMedia turns a stored media path into a full path under root and
// refuses one that leads outside it. The check lives in the media package,
// because retention deletes with the same one: a path bug there deletes
// the owner's evidence, and the check that stops it must be the check the
// clip handlers have already proved. Audio is resolved against clip_dir
// and video against video_dir, so neither can read the other's files.
func resolveMedia(rootDir, stored string) (string, error) {
	return media.Resolve(rootDir, stored)
}

// mediaRoot is the directory a kind of media is served from.
func (s *Server) mediaRoot(kind string) string {
	if kind == store.KindVideo {
		return s.cfg.VideoDir
	}
	return s.cfg.ClipDir
}

// clipIsMissing answers 404 and records the loss once per event per run. A
// clip that has vanished must surface, not hide behind an empty answer.
func (s *Server) clipIsMissing(w http.ResponseWriter, m store.Media) {
	s.mediaMissing(m)
	fail(w, http.StatusNotFound,
		"the clip for that event is no longer stored. The loss is in the health log.")
}

// clipWasPurged answers 410 Gone for a recording the owner deleted. It is
// not 404, which would mean the dashboard never heard of it, and it writes
// no health record: a purge is something the owner did on purpose, and the
// health log must not fill with alarms about it.
func (s *Server) clipWasPurged(w http.ResponseWriter, m store.Media) {
	// The date is the owner's own day. The purge is stored in UTC, and a
	// message in UTC would tell somebody deleting a clip at 8pm in New York
	// that they did it tomorrow.
	msg := "the " + m.Kind + " recording for that event was deleted on " +
		m.Purged.At.In(s.loc).Format("2 January 2006")
	if m.Purged.By != "" {
		msg += " by " + m.Purged.By
	}
	fail(w, http.StatusGone, msg+". The event and its measurements are unchanged.")
}

// --- WAV ---

// decodeWAV reads a 16-bit mono PCM WAV and returns its rate and samples. It
// walks the chunks rather than assuming a 44-byte header, because a file
// written by another tool may carry extra chunks.
func decodeWAV(b []byte) (int, []int16, error) {
	le := binary.LittleEndian
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, nil, errors.New("web: the clip is not a RIFF WAVE file")
	}
	var rate, channels, bits int
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(le.Uint32(b[off+4:]))
		body := off + 8
		if size < 0 || body+size > len(b) {
			size = len(b) - body // a clip cut short by a power loss still plays
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return 0, nil, errors.New("web: the clip has a short format chunk")
			}
			channels = int(le.Uint16(b[body+2:]))
			rate = int(le.Uint32(b[body+4:]))
			bits = int(le.Uint16(b[body+14:]))
		case "data":
			data = b[body : body+size]
		}
		off = body + size + size%2 // chunks are padded to an even length
	}
	switch {
	case rate <= 0:
		return 0, nil, errors.New("web: the clip has no sample rate")
	case channels != 1 || bits != 16:
		return 0, nil, fmt.Errorf("web: the clip is %d channels at %d bits, not mono 16-bit", channels, bits)
	}
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(le.Uint16(data[2*i:]))
	}
	return rate, out, nil
}

// encodeWAV writes 16-bit mono PCM samples with a 44-byte header.
func encodeWAV(samples []int16, rate int) []byte {
	le := binary.LittleEndian
	size := 2 * len(samples)
	b := make([]byte, 44+size)
	copy(b[0:], "RIFF")
	le.PutUint32(b[4:], uint32(36+size))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	le.PutUint32(b[16:], 16)
	le.PutUint16(b[20:], 1) // PCM
	le.PutUint16(b[22:], 1) // mono
	le.PutUint32(b[24:], uint32(rate))
	le.PutUint32(b[28:], uint32(rate*2))
	le.PutUint16(b[32:], 2)
	le.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	le.PutUint32(b[40:], uint32(size))
	for i, v := range samples {
		le.PutUint16(b[44+2*i:], uint16(v))
	}
	return b
}
