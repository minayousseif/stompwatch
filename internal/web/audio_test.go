package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/store"
)

// The tests build and read WAV files with their own code, so nothing here
// checks the server against the server.

// wavBytes builds a 16-bit mono PCM WAV.
func wavBytes(rate int, samples []int16) []byte {
	le := binary.LittleEndian
	b := make([]byte, 44+2*len(samples))
	copy(b[0:], "RIFF")
	le.PutUint32(b[4:], uint32(36+2*len(samples)))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	le.PutUint32(b[16:], 16)
	le.PutUint16(b[20:], 1)
	le.PutUint16(b[22:], 1)
	le.PutUint32(b[24:], uint32(rate))
	le.PutUint32(b[28:], uint32(rate*2))
	le.PutUint16(b[32:], 2)
	le.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	le.PutUint32(b[40:], uint32(2*len(samples)))
	for i, v := range samples {
		le.PutUint16(b[44+2*i:], uint16(v))
	}
	return b
}

// parseWAV reads a 16-bit mono WAV and returns its rate and its samples.
func parseWAV(t *testing.T, b []byte) (int, []int16) {
	t.Helper()
	le := binary.LittleEndian
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("the body is not a RIFF WAVE file: %d bytes", len(b))
	}
	if string(b[12:16]) != "fmt " || le.Uint16(b[20:]) != 1 {
		t.Fatalf("the body is not PCM")
	}
	if ch := le.Uint16(b[22:]); ch != 1 {
		t.Fatalf("channels = %d, want 1", ch)
	}
	if bits := le.Uint16(b[34:]); bits != 16 {
		t.Fatalf("bits per sample = %d, want 16", bits)
	}
	if string(b[36:40]) != "data" {
		t.Fatalf("the data chunk is not where a 44-byte header puts it")
	}
	n := int(le.Uint32(b[40:])) / 2
	if 44+2*n > len(b) {
		t.Fatalf("the data chunk says %d samples but the body holds %d bytes", n, len(b))
	}
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(le.Uint16(b[44+2*i:]))
	}
	return int(le.Uint32(b[24:])), out
}

// addClip writes a clip file for an event and stores its media row.
func (e *env) addClip(id int64, rate int, samples []int16) []byte {
	e.t.Helper()
	body := wavBytes(rate, samples)
	path := filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	err := e.store.InsertMedia(e.t.Context(), store.Media{
		EventID: id, Kind: store.KindAudio, Path: path, Bytes: int64(len(body)),
		Duration: time.Duration(len(samples)) * time.Second / time.Duration(rate),
		SHA256:   hex.EncodeToString(sum[:]),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return body
}

// addClippedClip stores a clip whose media row says n samples hit the limit.
func (e *env) addClippedClip(id int64, n int) {
	e.t.Helper()
	body := wavBytes(1000, []int16{1, 2, 3, 4})
	path := filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	err := e.store.InsertMedia(e.t.Context(), store.Media{
		EventID: id, Kind: store.KindAudio, Path: path, Bytes: int64(len(body)),
		Duration: 4 * time.Millisecond, SHA256: hex.EncodeToString(sum[:]), Clipped: n,
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// clipEvent stores an event with a clip and returns its number.
func (e *env) clipEvent(samples []int16) int64 {
	e.t.Helper()
	id := e.addEvent(0, time.Second, 60, detect.Running)
	e.addClip(id, 1000, samples)
	return id
}

func audioURL(id int64) string { return "/api/events/" + strconv.FormatInt(id, 10) + "/audio" }

func waveformURL(id int64) string {
	return "/api/events/" + strconv.FormatInt(id, 10) + "/waveform"
}

// --- playback ---

// Browsers refuse to play audio below 3 kHz, so a stored 1 kHz clip is
// served at 8 kHz. The stored file does not change.
func TestPlaybackIsServedAtEightTimesTheStoredRate(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent([]int16{0, 800, -800, 0})

	w := e.get(audioURL(id))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type = %q, want audio/wav", ct)
	}
	rate, got := parseWAV(t, w.Body.Bytes())
	if rate != 8000 {
		t.Errorf("sample rate = %d, want 8000", rate)
	}
	if len(got) != 32 {
		t.Errorf("samples = %d, want 32: eight for each of the four stored", len(got))
	}
}

// The output must carry the stored samples through untouched and put real
// values between them, not repeat each one eight times.
func TestPlaybackInterpolatesBetweenStoredSamples(t *testing.T) {
	// A ramp of 100 per stored sample. In the middle of it the answer is
	// arithmetic anyone can check: the interpolation kernel is symmetric
	// about the half-way phase, so the half-way output is the mid-point.
	in := make([]int16, 40)
	for i := range in {
		in[i] = int16(100 * i)
	}
	// The interpolation is tested on its own, at unity gain. The handler
	// raises a quiet clip, which is a separate decision with its own tests.
	got := quantize(upsample(tilt(in, "flat", 1000), 8), 1)
	if len(got) != 320 {
		t.Fatalf("samples = %d, want 320", len(got))
	}
	// Every eighth output lands on a stored sample and must equal it.
	for i := 5; i < 35; i++ {
		if got[8*i] != int16(100*i) {
			t.Errorf("output %d = %d, want the stored sample %d", 8*i, got[8*i], 100*i)
		}
	}
	// The half-way output between stored sample 20 (2000) and 21 (2100) is
	// 2050.
	if got[8*20+4] != 2050 {
		t.Errorf("output %d = %d, want 2050, half way between 2000 and 2100", 8*20+4, got[8*20+4])
	}
	// A quarter of the way is nearer 2000 than 2100, and is neither.
	q := got[8*20+2]
	if q <= 2000 || q >= 2050 {
		t.Errorf("output %d = %d, want a value between 2000 and 2050, not a repeat", 8*20+2, q)
	}
}

// Speech is kept out of clips by a 500 Hz filter before anything is stored.
// Raising the rate for playback must not put energy back above 500 Hz.
func TestPlaybackAddsNothingAudibleAbove500Hz(t *testing.T) {
	e := newEnv(t)
	// One second of a 200 Hz tone, stored at 1 kHz. 200 whole cycles fit in
	// the second, so the tone lands on one frequency and nothing smears.
	in := make([]int16, 1000)
	for i := range in {
		in[i] = int16(math.Round(0.9 * 32767 * math.Sin(2*math.Pi*200*float64(i)/1000)))
	}
	id := e.clipEvent(in)

	rate, got := parseWAV(t, e.get(audioURL(id)).Body.Bytes())
	if rate != 8000 || len(got) != 8000 {
		t.Fatalf("the playback clip is %d samples at %d Hz, want 8000 at 8000", len(got), rate)
	}
	// Measure the middle quarter, well clear of both ends, where the kernel
	// reads only real samples. 2000 samples hold 50 whole cycles of 200 Hz.
	const n = 2000
	window := got[2000 : 2000+n]
	power := make([]float64, n/2+1)
	for k := range power {
		var re, im float64
		for i, v := range window {
			si, co := math.Sincos(2 * math.Pi * float64(k) * float64(i) / n)
			re += float64(v) * co
			im -= float64(v) * si
		}
		power[k] = re*re + im*im
	}
	// A bin is 4 Hz wide, so 200 Hz is bin 50 and 500 Hz is bin 125.
	peak := power[50]
	if peak == 0 {
		t.Fatalf("the 200 Hz tone is not in the output at all")
	}
	var above float64
	for k := 126; k < len(power); k++ {
		above += power[k]
	}
	db := 10 * math.Log10(above/peak)
	t.Logf("energy above 500 Hz is %.1f dB below the 200 Hz peak", db)
	if db > -40 {
		t.Errorf("energy above 500 Hz is only %.1f dB below the 200 Hz peak, want 40 dB or more", -db)
	}
}

// Without range requests a browser downloads the whole clip before it plays
// a note, and reviewing a five minute event becomes unusable.
func TestPlaybackAnswersARangeRequest(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent([]int16{0, 800, -800, 0})

	whole := e.get(audioURL(id)).Body.Bytes()
	w := e.get(audioURL(id), "Range", "bytes=0-9")
	if w.Code != 206 {
		t.Fatalf("status = %d, want 206 for a range request: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Range"); got == "" {
		t.Errorf("there is no Content-Range header")
	}
	if got := w.Body.Bytes(); len(got) != 10 || string(got) != string(whole[:10]) {
		t.Errorf("the range gave %d bytes, want the first 10 of the clip", len(got))
	}
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
}

// The cache holds one entry per event. Two events must never hear each
// other's clip.
func TestPlaybackCacheKeepsEventsApart(t *testing.T) {
	e := newEnv(t)
	first := e.clipEvent([]int16{0, 1000, 2000, 3000})
	second := e.addEvent(time.Minute, time.Second, 60, detect.Running)
	e.addClip(second, 1000, []int16{0, -1000, -2000, -3000})
	// Both files are given the same modification time. Two clips written in
	// the same second look alike to anything but the event number, so only
	// the key can keep them apart.
	for _, id := range []int64{first, second} {
		path := filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")
		if err := os.Chtimes(path, t0, t0); err != nil {
			t.Fatal(err)
		}
	}

	_, a := parseWAV(t, e.get(audioURL(first)).Body.Bytes())
	_, b := parseWAV(t, e.get(audioURL(second)).Body.Bytes())
	_, again := parseWAV(t, e.get(audioURL(first)).Body.Bytes())
	if a[8] == b[8] {
		t.Errorf("both events play the same sample %d; the cache is keyed wrongly", a[8])
	}
	if a[8] != again[8] {
		t.Errorf("the second look at event %d gave %d, not %d", first, again[8], a[8])
	}
	if b[8] >= 0 {
		t.Errorf("the second event plays %d, want the negative ramp it was given", b[8])
	}
}

// A clip that has vanished must surface loudly, not answer 200 with silence.
func TestPlaybackOfAMissingClipIs404AndRecordedOnce(t *testing.T) {
	e := newEnv(t)
	var kinds []string
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, _ string, _ time.Duration) {
		kinds = append(kinds, kind)
	}
	id := e.clipEvent([]int16{0, 800})
	if err := os.Remove(filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")); err != nil {
		t.Fatal(err)
	}

	wantError(t, e.get(audioURL(id)), 404, "")
	wantError(t, e.get(audioURL(id)), 404, "")
	if len(kinds) != 1 || kinds[0] != "media_missing" {
		t.Errorf("health records = %v, want one media_missing", kinds)
	}
}

func TestPlaybackOfAnEventWithNoClipIs404(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(0, time.Second, 60, detect.Running)
	wantError(t, e.get(audioURL(id)), 404, "")
	wantError(t, e.get(audioURL(9999)), 404, "")
}

// --- the original file ---

// The download is evidence. It must be the stored bytes, unchanged.
func TestOriginalIsTheStoredBytesAsAnAttachment(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent([]int16{0, 800, -800, 0})
	stored := wavBytes(1000, []int16{0, 800, -800, 0})

	w := e.get(audioURL(id) + "/original")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != string(stored) {
		t.Errorf("the download is %d bytes, want the %d stored", len(got), len(stored))
	}
	want := `attachment; filename="event-` + strconv.FormatInt(id, 10) + `.wav"`
	if got := w.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type = %q, want audio/wav", ct)
	}
	rate, got := parseWAV(t, w.Body.Bytes())
	if rate != 1000 || len(got) != 4 {
		t.Errorf("the download is %d samples at %d Hz, want the stored 4 at 1000", len(got), rate)
	}
}

// A clip that no longer matches its hash cannot be used as evidence. The
// answer must be a refusal, not a body that looks fine.
func TestOriginalRefusesAClipThatNoLongerMatchesItsHash(t *testing.T) {
	e := newEnv(t)
	var kinds []string
	e.srv.cfg.RecordHealth = func(_ time.Time, kind, _ string, _ time.Duration) {
		kinds = append(kinds, kind)
	}
	id := e.clipEvent([]int16{0, 800, -800, 0})
	path := filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")
	changed := wavBytes(1000, []int16{0, 801, -800, 0})
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}

	w := e.get(audioURL(id) + "/original")
	wantError(t, w, 409, "")
	if got := w.Body.String(); contains(got, "800") || contains(got, ".wav") {
		t.Errorf("the refusal %q gives away the file", got)
	}
	if len(kinds) != 1 || kinds[0] != "write_error" {
		t.Errorf("health records = %v, want one write_error", kinds)
	}
}

func TestOriginalOfAMissingClipIs404(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent([]int16{0, 800})
	if err := os.Remove(filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.get(audioURL(id)+"/original"), 404, "")
}

// --- the waveform ---

// The peaks come from the stored 1 kHz clip, so four stored samples give
// four peaks however many buckets were asked for.
func TestWaveformOfAShortClipGivesOneBucketPerSample(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent([]int16{0, 16384, -8192, 24576})

	m := e.getJSON(waveformURL(id))
	if got := num(t, m, "buckets"); got != 4 {
		t.Errorf("buckets = %v, want the 4 stored samples", got)
	}
	if got := num(t, m, "duration_ms"); got != 4 {
		t.Errorf("duration_ms = %v, want 4: four samples at 1 kHz", got)
	}
	peaks, ok := m["peaks"].([]any)
	if !ok || len(peaks) != 4 {
		t.Fatalf("peaks = %v, want 4 pairs", m["peaks"])
	}
	want := [][2]float64{{0, 0}, {0.5, 0.5}, {-0.25, -0.25}, {0.75, 0.75}}
	for i, p := range peaks {
		pair, ok := p.([]any)
		if !ok || len(pair) != 2 {
			t.Fatalf("peak %d = %v, want a pair", i, p)
		}
		if pair[0] != want[i][0] || pair[1] != want[i][1] {
			t.Errorf("peak %d = %v, want [%v %v]", i, pair, want[i][0], want[i][1])
		}
	}
}

func TestWaveformBucketsTheStoredSamples(t *testing.T) {
	e := newEnv(t)
	in := make([]int16, 1000)
	in[5] = 16384    // in the first of a hundred buckets of ten samples
	in[995] = -24576 // in the last
	id := e.clipEvent(in)

	m := e.getJSON(waveformURL(id) + "?buckets=100")
	if got := num(t, m, "buckets"); got != 100 {
		t.Errorf("buckets = %v, want 100", got)
	}
	if got := num(t, m, "duration_ms"); got != 1000 {
		t.Errorf("duration_ms = %v, want 1000", got)
	}
	peaks, ok := m["peaks"].([]any)
	if !ok || len(peaks) != 100 {
		t.Fatalf("peaks = %v pairs, want 100", len(peaks))
	}
	pair := func(i int) []any {
		p, ok := peaks[i].([]any)
		if !ok || len(p) != 2 {
			t.Fatalf("peak %d = %v, want a pair", i, peaks[i])
		}
		return p
	}
	if p := pair(0); p[0] != 0.0 || p[1] != 0.5 {
		t.Errorf("peak 0 = %v, want [0 0.5]", p)
	}
	if p := pair(99); p[0] != -0.75 || p[1] != 0.0 {
		t.Errorf("peak 99 = %v, want [-0.75 0]", p)
	}
	if p := pair(50); p[0] != 0.0 || p[1] != 0.0 {
		t.Errorf("peak 50 = %v, want [0 0], a bucket of silence", p)
	}
}

func TestWaveformUsesEightHundredBucketsByDefault(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent(make([]int16, 1000))
	m := e.getJSON(waveformURL(id))
	if got := num(t, m, "buckets"); got != 800 {
		t.Errorf("buckets = %v, want the default 800", got)
	}
}

func TestWaveformRejectsBucketsOutOfRange(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent(make([]int16, 1000))
	wantError(t, e.get(waveformURL(id)+"?buckets=99"), 400, "buckets")
	wantError(t, e.get(waveformURL(id)+"?buckets=4001"), 400, "buckets")
	wantError(t, e.get(waveformURL(id)+"?bucket=800"), 400, "bucket")
}

// No response names a file or a directory, not even this one.
func TestClipAddressesNeverNameAFile(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent([]int16{0, 800})
	if err := os.Remove(filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{audioURL(id), audioURL(id) + "/original", waveformURL(id)} {
		w := e.get(u)
		if contains(w.Body.String(), e.clipDir) || contains(w.Body.String(), e.dir) {
			t.Errorf("GET %s names a place on disk: %s", u, w.Body.String())
		}
		for k, vs := range w.Header() {
			for _, v := range vs {
				if contains(v, "/") && k != "Content-Type" {
					t.Errorf("GET %s sent header %s: %q, which names a path", u, k, v)
				}
			}
		}
	}
}

// SPEC.md section 9 requires the resolved media path to stay inside the media root.
// Only the collector writes these rows today, so this is a second line of
// defense: a database restored from elsewhere must not be able to read the
// rest of the disk.
func TestAClipPathOutsideTheClipDirectoryIsRefused(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Minute, time.Second, 60, detect.Running)

	// The file sits next to the clip directory, so "../secret.wav" really
	// reaches it. A path that merely looks like an escape but resolves to
	// nothing would pass whether the check exists or not.
	outside := filepath.Join(filepath.Dir(e.clipDir), "secret.wav")
	body := wavBytes(1000, []int16{1, 2, 3, 4})
	if err := os.WriteFile(outside, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the test file is not where the escape points: %v", err)
	}
	sum := sha256.Sum256(body)
	err := e.store.InsertMedia(t.Context(), store.Media{
		EventID: id, Kind: store.KindAudio,
		Path:  filepath.Join("..", "secret.wav"),
		Bytes: int64(len(body)), Duration: 4 * time.Millisecond,
		SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"audio", "audio/original", "waveform"} {
		target := fmt.Sprintf("/api/events/%d/%s", id, name)
		w := e.get(target)
		if w.Code == http.StatusOK {
			t.Errorf("%s served a clip from outside the clip directory", target)
		}
		if contains(w.Body.String(), "secret.wav") {
			t.Errorf("%s named the file in its answer: %s", target, w.Body.String())
		}
	}
}

// A household impact reads tens of dB below full scale, so a faithful clip
// is close to inaudible on a phone. Playback is raised to a level a person
// can hear, while the stored file and its hash stay as they were.
func TestQuietClipIsRaisedForPlayback(t *testing.T) {
	e := newEnv(t)
	// A 100 Hz tone peaking at 1/100 of full scale, about -40 dBFS.
	quiet := make([]int16, 4000)
	for i := range quiet {
		quiet[i] = int16(math.Round(327 * math.Sin(2*math.Pi*250*float64(i)/1000)))
	}
	id := e.clipEvent(quiet)

	_, out := parseWAV(t, e.get(audioURL(id)).Body.Bytes())
	peak := 0
	for _, v := range out {
		peak = max(peak, int(math.Abs(float64(v))))
	}
	// The target is -1 dBFS, which is 29205 of 32768. Allow for the
	// interpolation landing a little either side of a stored sample.
	if peak < 27000 || peak > 32767 {
		t.Errorf("playback peaks at %d (%.1f dBFS), want about -1 dBFS",
			peak, 20*math.Log10(float64(peak)/32768))
	}
}

// A clip that already reaches full scale is brought down, not raised. The
// tilt lifts it further, and clipping the result would add the harmonics of
// a squared-off sine to a recording that never had them.
func TestLoudClipIsBroughtDownRatherThanClipped(t *testing.T) {
	e := newEnv(t)
	loud := make([]int16, 4000)
	for i := range loud {
		loud[i] = int16(math.Round(32000 * math.Sin(2*math.Pi*250*float64(i)/1000)))
	}
	id := e.clipEvent(loud)

	if got := num(t, clipOf(e, id), "playback_gain_db"); got >= 0 {
		t.Errorf("playback_gain_db = %v for a clip at full scale, want a reduction", got)
	}
	_, out := parseWAV(t, e.get(audioURL(id)).Body.Bytes())
	for i, v := range out {
		if v >= 32767 || v <= -32768 {
			t.Fatalf("playback sample %d is against the limit at %d", i, v)
		}
	}
}

// The gain is reported, so nobody mistakes a raised clip for a loud one.
func TestThePlaybackGainAndPeakAreReported(t *testing.T) {
	e := newEnv(t)
	quiet := make([]int16, 2000)
	for i := range quiet {
		quiet[i] = int16(math.Round(327 * math.Sin(2*math.Pi*250*float64(i)/1000)))
	}
	id := e.clipEvent(quiet)

	clip := clipOf(e, id)
	// 327 of 32768 is -40.02 dBFS. That is the stored clip's own peak, and
	// it is reported as it is, whatever playback then does to it.
	if got := num(t, clip, "peak_dbfs"); math.Abs(got+40.02) > 0.1 {
		t.Errorf("peak_dbfs = %.2f, want about -40.02", got)
	}
	// The gain is what playback applies after the tilt, so it is smaller
	// than the 39 dB the stored peak alone would need. It must still be a
	// real rise, and inside the cap.
	gain := num(t, clip, "playback_gain_db")
	if gain < 15 || gain >= 40 {
		t.Errorf("playback_gain_db = %.2f, want a rise between 15 and 40 dB", gain)
	}
	// And the stream it describes must land on the target.
	_, out := parseWAV(t, e.get(audioURL(id)).Body.Bytes())
	peak := 0.0
	for _, v := range out {
		peak = math.Max(peak, math.Abs(float64(v)))
	}
	if db := 20 * math.Log10(peak/32768); math.Abs(db+1) > 0.3 {
		t.Errorf("the playback stream peaks at %.2f dBFS, want -1", db)
	}
}

// Raising a near-silent clip would amplify nothing but the quantization
// step, which sounds like hiss and says nothing about the night.
func TestTheGainIsCapped(t *testing.T) {
	e := newEnv(t)
	// A single step of the 16-bit scale, -90.3 dBFS.
	faint := make([]int16, 2000)
	for i := range faint {
		faint[i] = int16(i % 2)
	}
	id := e.clipEvent(faint)

	if got := num(t, clipOf(e, id), "playback_gain_db"); got != 40 {
		t.Errorf("playback_gain_db = %v for a near-silent clip, want the 40 dB cap", got)
	}
}

// The stored file is the evidence. Raising the playback must not touch it.
func TestRaisingPlaybackLeavesTheStoredFileAlone(t *testing.T) {
	e := newEnv(t)
	quiet := make([]int16, 2000)
	for i := range quiet {
		quiet[i] = int16(math.Round(327 * math.Sin(2*math.Pi*250*float64(i)/1000)))
	}
	id := e.clipEvent(quiet)
	stored := e.get(audioURL(id) + "/original")

	// Fetch the playback stream first, so any shared buffer would be caught.
	e.get(audioURL(id))
	again := e.get(audioURL(id) + "/original")

	if again.Code != 200 {
		t.Fatalf("the stored file came back %d: %s", again.Code, again.Body.String())
	}
	if !bytes.Equal(stored.Body.Bytes(), again.Body.Bytes()) {
		t.Error("the stored file changed after the playback stream was built")
	}
	_, samples := parseWAV(t, again.Body.Bytes())
	if samples[250] != quiet[250] {
		t.Errorf("stored sample 250 = %d, want %d", samples[250], quiet[250])
	}
}

// A clip with samples against the limit is distorted, and the screen must
// be able to say so instead of leaving it in the health log.
func TestTheClippedSampleCountReachesTheDashboard(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	e.addClippedClip(id, 197)

	if got := num(t, clipOf(e, id), "clipped"); got != 197 {
		t.Errorf("clipped = %v, want 197", got)
	}
}

// A clip holds 20-450 Hz and most of its energy is below 80 Hz, which a
// phone speaker cannot make. The default playback lifts the upper part so a
// small speaker reproduces it; tone=flat leaves the balance alone.
func TestPlaybackTiltsForASmallSpeakerUnlessAskedNotTo(t *testing.T) {
	e := newEnv(t)
	// Equal amounts of 40 Hz and 400 Hz, at a level that leaves headroom.
	in := make([]int16, 4000)
	for i := range in {
		low := math.Sin(2 * math.Pi * 40 * float64(i) / 1000)
		high := math.Sin(2 * math.Pi * 400 * float64(i) / 1000)
		in[i] = int16(math.Round(4000 * (low + high)))
	}
	id := e.clipEvent(in)

	ratio := func(target string) float64 {
		_, out := parseWAV(t, e.get(audioURL(id)+target).Body.Bytes())
		return power(out, 400, 8000) / power(out, 40, 8000)
	}
	flat := ratio("?tone=flat")
	tilted := ratio("")

	// The stored clip has the two tones at the same level, so flat playback
	// keeps them within a decibel of each other.
	if db := 10 * math.Log10(flat); math.Abs(db) > 1 {
		t.Errorf("flat playback has 400 Hz %.1f dB from 40 Hz, want them level", db)
	}
	// The default lifts 400 Hz by about 12 dB relative to 40 Hz.
	if db := 10 * math.Log10(tilted); db < 8 || db > 16 {
		t.Errorf("tilted playback has 400 Hz %.1f dB above 40 Hz, want about 12", db)
	}
}

func TestPlaybackRejectsAnUnknownTone(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent(make([]int16, 1000))
	wantError(t, e.get(audioURL(id)+"?tone=warm"), 400, "tone")
}

// power is the energy of one frequency in a signal, by a single-bin DFT.
func power(x []int16, freq, rate float64) float64 {
	var re, im float64
	for i, v := range x {
		// A Hann window keeps a neighboring tone from leaking into the bin.
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(len(x)))
		ang := 2 * math.Pi * freq * float64(i) / rate
		re += w * float64(v) * math.Cos(ang)
		im -= w * float64(v) * math.Sin(ang)
	}
	return re*re + im*im
}

// A database holds clips of more than one rate, because clip_lowpass_hz may
// be changed and old clips keep their own rate (SPEC.md section 15 decision 18).
// The factor is chosen per clip: the smallest power of two that reaches
// 8 kHz, which is above the 3 kHz a browser refuses to play below.
func TestPlaybackRaisesEveryStoredRateToAtLeast8000(t *testing.T) {
	for _, tc := range []struct{ stored, served, factor int }{
		{1000, 8000, 8}, {2000, 8000, 4}, {4000, 8000, 2},
		{8000, 8000, 1}, {12000, 12000, 1}, {16000, 16000, 1}, {24000, 24000, 1},
	} {
		e := newEnv(t)
		id := e.addEvent(0, time.Second, 60, detect.Running)
		e.addClip(id, tc.stored, []int16{0, 800, -800, 0})

		w := e.get(audioURL(id))
		if w.Code != 200 {
			t.Fatalf("a %d Hz clip: status %d: %s", tc.stored, w.Code, w.Body.String())
		}
		rate, got := parseWAV(t, w.Body.Bytes())
		if rate != tc.served {
			t.Errorf("a %d Hz clip is served at %d Hz, want %d", tc.stored, rate, tc.served)
		}
		if len(got) != 4*tc.factor {
			t.Errorf("a %d Hz clip gave %d samples, want %d: %d for each of the four stored",
				tc.stored, len(got), 4*tc.factor, tc.factor)
		}
	}
}

// The tilt shelf must run at the clip's own rate. Its corner is 200 Hz and
// it lifts 12 dB, so at the corner it lifts exactly half of that: 6.02 dB.
// A shelf built for one rate and run at another puts its corner elsewhere,
// and then the 6 dB is not there.
func TestTiltCornerStaysAt200HzAtEveryStoredRate(t *testing.T) {
	for _, rate := range []int{1000, 2000, 4000, 8000, 24000} {
		in := make([]int16, rate)
		for i := range in {
			in[i] = int16(math.Round(8000 * math.Sin(2*math.Pi*200*float64(i)/float64(rate))))
		}
		// The second half only, so the shelf has settled.
		flat := rmsOf(tilt(in, "flat", rate)[rate/2:])
		lifted := rmsOf(tilt(in, "tilt", rate)[rate/2:])
		got := 20 * math.Log10(lifted/flat)
		if math.Abs(got-6.02) > 0.4 {
			t.Errorf("at %d Hz the tilt lifts a 200 Hz tone by %.2f dB, want 6.02 dB", rate, got)
		}
	}
}

func rmsOf(x []float64) float64 {
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(x)))
}

// The interface must be able to say what a clip is instead of assuming
// 1 kHz, so the rate comes from the stored file itself.
func TestMediaSaysTheStoredRate(t *testing.T) {
	for _, rate := range []int{1000, 2000, 24000} {
		e := newEnv(t)
		id := e.addEvent(0, time.Second, 60, detect.Running)
		e.addClip(id, rate, []int16{0, 800, -800, 0})
		m := clipOf(e, id)
		got, ok := m["rate_hz"].(float64)
		if !ok {
			t.Fatalf("a %d Hz clip has rate_hz = %v, want a number", rate, m["rate_hz"])
		}
		if int(got) != rate {
			t.Errorf("rate_hz = %v, want %d", got, rate)
		}
	}
}

// A clip whose file has gone has no rate to report. Null says "not known",
// which the interface must be able to tell from a real rate.
func TestMediaRateIsNullWhenTheFileIsGone(t *testing.T) {
	e := newEnv(t)
	id := e.clipEvent([]int16{0, 800})
	if err := os.Remove(filepath.Join(e.clipDir, strconv.FormatInt(id, 10)+".wav")); err != nil {
		t.Fatal(err)
	}
	m := clipOf(e, id)
	v, present := m["rate_hz"]
	if !present || v != nil {
		t.Errorf("rate_hz = %v, want null", v)
	}
}
