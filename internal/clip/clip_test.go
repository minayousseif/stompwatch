package clip

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)

// feed sends raw 48 kHz audio in 100 ms chunks with exact start times.
// gen receives the time in seconds since t0.
func feed(r *Recorder, from time.Time, seconds float64, gen func(s float64) float64) {
	const chunk = 4800
	n := int(seconds * 48000)
	base := from.Sub(t0).Seconds()
	buf := make([]float64, chunk)
	for i := 0; i < n; i += chunk {
		m := min(chunk, n-i)
		for k := 0; k < m; k++ {
			buf[k] = gen(base + float64(i+k)/48000)
		}
		r.Process(buf[:m], from.Add(time.Duration(i)*time.Second/48000))
	}
}

func silence(float64) float64 { return 0 }

func newRecorder(t *testing.T) (*Recorder, string) {
	t.Helper()
	root := t.TempDir()
	r, err := NewRecorder(root, 30*time.Second, 500)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	return r, root
}

// readWAV checks the header of a 16-bit mono PCM WAV file and returns its
// sample rate and samples scaled to +/-1.
func readWAV(t *testing.T, path string) (int, []float64) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" ||
		string(b[12:16]) != "fmt " || string(b[36:40]) != "data" {
		t.Fatalf("%s: not a canonical WAV header", path)
	}
	le := binary.LittleEndian
	if f, ch, bits := le.Uint16(b[20:]), le.Uint16(b[22:]), le.Uint16(b[34:]); f != 1 || ch != 1 || bits != 16 {
		t.Fatalf("format %d, channels %d, bits %d; want PCM, mono, 16-bit", f, ch, bits)
	}
	n := int(le.Uint32(b[40:])) / 2
	if 44+2*n != len(b) {
		t.Fatalf("data size %d does not match file size %d", 2*n, len(b))
	}
	s := make([]float64, n)
	for i := range s {
		s[i] = float64(int16(le.Uint16(b[44+2*i:]))) / 32767
	}
	return int(le.Uint32(b[24:])), s
}

// Header bytes worked out by hand from the RIFF/WAVE layout.
// 0.5 x 32767 = 16383.5 rounds to 16384 (0x4000). 2.0 clips to 32767.
// The rate and the byte rate are in the header twice over, so a clip written
// at one rate can never be read back as another.
func TestEncodeWAVBytes(t *testing.T) {
	for _, tc := range []struct {
		rate         int
		rateB, byteB [4]byte
	}{
		{1000, [4]byte{0xE8, 0x03, 0, 0}, [4]byte{0xD0, 0x07, 0, 0}},
		{2000, [4]byte{0xD0, 0x07, 0, 0}, [4]byte{0xA0, 0x0F, 0, 0}},
	} {
		var buf bytes.Buffer
		clipped, err := encodeWAV(&buf, []float64{0.5, -1.0, 2.0}, tc.rate)
		if err != nil {
			t.Fatal(err)
		}
		want := []byte{
			'R', 'I', 'F', 'F', 0x2A, 0, 0, 0, 'W', 'A', 'V', 'E',
			'f', 'm', 't', ' ', 0x10, 0, 0, 0, 0x01, 0, 0x01, 0,
			tc.rateB[0], tc.rateB[1], tc.rateB[2], tc.rateB[3],
			tc.byteB[0], tc.byteB[1], tc.byteB[2], tc.byteB[3], 0x02, 0, 0x10, 0,
			'd', 'a', 't', 'a', 0x06, 0, 0, 0,
			0x00, 0x40, 0x01, 0x80, 0xFF, 0x7F,
		}
		if !bytes.Equal(buf.Bytes(), want) {
			t.Fatalf("%d Hz bytes:\n got % X\nwant % X", tc.rate, buf.Bytes(), want)
		}
		if clipped != 1 {
			t.Errorf("%d Hz: clipped = %d, want 1", tc.rate, clipped)
		}
	}
}

func TestSaveWritesDatedWAVWithHash(t *testing.T) {
	r, root := newRecorder(t)
	feed(r, t0, 20, func(s float64) float64 { return 0.5 * math.Sin(2*math.Pi*100*s) })

	c, err := r.Save(42, t0.Add(5*time.Second), t0.Add(15*time.Second))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want := filepath.Join(root, "2026", "09", "11", "42.wav"); c.Path != want {
		t.Errorf("path = %s, want %s", c.Path, want)
	}
	rate, samples := readWAV(t, c.Path)
	if rate != 1000 || len(samples) != 10000 {
		t.Errorf("rate %d with %d samples, want 1000 with 10000", rate, len(samples))
	}
	b, _ := os.ReadFile(c.Path)
	sum := sha256.Sum256(b)
	if c.SHA256 != hex.EncodeToString(sum[:]) || c.Bytes != int64(len(b)) {
		t.Errorf("clip reports sha256 %s and %d bytes; file has %x and %d bytes", c.SHA256, c.Bytes, sum, len(b))
	}
	if d := c.Start.Sub(t0.Add(5 * time.Second)); d < 0 || d >= time.Millisecond {
		t.Errorf("clip starts %v after the requested start, want 0 to 1 ms", d)
	}
	if c.Duration != 10*time.Second || c.Truncated {
		t.Errorf("duration %v truncated %v, want 10s and false", c.Duration, c.Truncated)
	}
	// The service user only: its group, audio, often holds the login user.
	info, err := os.Stat(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Errorf("mode %04o, want 0400", info.Mode().Perm())
	}
}

// Sample k of a clip must be the sound at c.Start + k ms. This depends on the
// filter delay being removed from the timestamps. A 5 Hz sine of amplitude
// 0.5 changes by 0.016 per millisecond, so a 31 ms error is far outside 0.01.
func TestClipSamplesAlignWithTimestamps(t *testing.T) {
	r, _ := newRecorder(t)
	sine := func(s float64) float64 { return 0.5 * math.Sin(2*math.Pi*5*s) }
	feed(r, t0, 10, sine)
	c, err := r.Save(1, t0.Add(2*time.Second), t0.Add(8*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, samples := readWAV(t, c.Path)
	worst := 0.0
	for k, v := range samples {
		ts := c.Start.Add(time.Duration(k) * time.Millisecond).Sub(t0).Seconds()
		worst = math.Max(worst, math.Abs(v-sine(ts)))
	}
	if worst > 0.01 {
		t.Fatalf("largest difference from the input at the sample time = %.4f, want <= 0.01", worst)
	}
}

// Read timestamps jitter by a few milliseconds. Stored samples must follow the
// sample count, so the clip has no false gaps and stays aligned.
func TestReadJitterDoesNotCreateGaps(t *testing.T) {
	r, _ := newRecorder(t)
	sine := func(s float64) float64 { return 0.5 * math.Sin(2*math.Pi*5*s) }
	const chunk = 4800
	buf := make([]float64, chunk)
	for k, i := 0, 0; i < 10*48000; k, i = k+1, i+chunk {
		for j := range buf {
			buf[j] = sine(float64(i+j) / 48000)
		}
		// 0, +2, -1, +1, -2 ms, repeating.
		jitter := time.Duration((k*7+2)%5-2) * time.Millisecond
		r.Process(buf, t0.Add(time.Duration(i)*time.Second/48000+jitter))
	}
	c, err := r.Save(1, t0.Add(2*time.Second), t0.Add(8*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if c.Truncated || c.Duration != 6*time.Second {
		t.Fatalf("truncated %v, duration %v; want false and 6s", c.Truncated, c.Duration)
	}
}

// SPEC.md section 3.2: a clip on disk cannot contain the speech band. Tones at
// 1013, 2017, and 3333 Hz must leave nothing above one 16-bit step.
func TestSavedClipContainsNoSoundAbove500Hz(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 10, func(s float64) float64 {
		return 0.3 * (math.Sin(2*math.Pi*1013*s) + math.Sin(2*math.Pi*2017*s) + math.Sin(2*math.Pi*3333*s))
	})
	c, err := r.Save(1, t0.Add(2*time.Second), t0.Add(9*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, samples := readWAV(t, c.Path)
	for k, v := range samples {
		if math.Abs(v) > 1.0/32767+1e-9 {
			t.Fatalf("sample %d = %d steps, want at most 1", k, int(math.Round(v*32767)))
		}
	}
}

func TestSaveRefusesToOverwrite(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 10, silence)
	c, err := r.Save(7, t0.Add(time.Second), t0.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(c.Path)
	_, err = r.Save(7, t0.Add(4*time.Second), t0.Add(8*time.Second))
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Save error = %v, want ErrExists", err)
	}
	after, _ := os.ReadFile(c.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("second Save changed the existing file")
	}
}

func TestSaveLeavesNoTemporaryFiles(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 5, silence)
	c, err := r.Save(3, t0.Add(time.Second), t0.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(c.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "3.wav" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want only 3.wav", names)
	}
}

// The post-roll comes after the event closes. Save must wait for it.
func TestSaveBeforeAudioArrivesIsNotReady(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 5, silence)
	if _, err := r.Save(1, t0.Add(time.Second), t0.Add(6*time.Second)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Save past the latest sample: error = %v, want ErrNotReady", err)
	}
}

func TestSaveBeforeOldestSampleIsTruncated(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 40, silence) // capacity is 30 s
	c, err := r.Save(1, t0.Add(2*time.Second), t0.Add(35*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || c.Start.Before(t0.Add(9*time.Second)) {
		t.Fatalf("truncated %v, start %v; want true and no earlier than 9 s", c.Truncated, c.Start.Sub(t0))
	}
}

// An event can run longer than the 30-second buffer. A hold keeps its audio.
func TestHoldKeepsAudioBeyondCapacity(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 5, silence)
	h := r.Hold(t0.Add(2 * time.Second))
	feed(r, t0.Add(5*time.Second), 55, silence)
	c, err := r.Save(1, t0.Add(2*time.Second), t0.Add(50*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if c.Truncated || c.Duration != 48*time.Second {
		t.Fatalf("held clip truncated %v, duration %v; want false and 48s", c.Truncated, c.Duration)
	}

	r.Release(h)
	feed(r, t0.Add(60*time.Second), 10, silence)
	c, err = r.Save(2, t0.Add(2*time.Second), t0.Add(65*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated {
		t.Fatal("after Release, audio older than the capacity was still kept")
	}
}

// A clip must be continuous so that sample k is at Start + k ms. After a gap
// in the audio, only the part after the gap is saved, and it is marked.
func TestGapTruncatesClipToAudioAfterGap(t *testing.T) {
	r, _ := newRecorder(t)
	feed(r, t0, 10, silence)
	feed(r, t0.Add(15*time.Second), 10, silence)
	c, err := r.Save(1, t0.Add(2*time.Second), t0.Add(24*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || c.Start.Before(t0.Add(15*time.Second)) {
		t.Fatalf("truncated %v, start %v; want true and no earlier than 15 s", c.Truncated, c.Start.Sub(t0))
	}
}

// A clip of about a minute needs both a long pre-roll and the memory to hold
// it. BufferLength is what the collector builds the recorder with, and
// MaxPreRoll is the longest pre-roll the config allows, so the two must fit
// together or the longest allowed setting would quietly produce a short clip.
func TestTheBufferHoldsTheLongestAllowedPreRoll(t *testing.T) {
	root := t.TempDir()
	r, err := NewRecorder(root, BufferLength, 500)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// Fill the buffer, then ask for the longest pre-roll the config allows,
	// ending a little before the newest sample so the audio has arrived.
	feed(r, t0, BufferLength.Seconds(), silence)
	to := t0.Add(BufferLength - time.Second)
	c, err := r.Save(1, to.Add(-MaxPreRoll), to)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if c.Truncated {
		t.Errorf("a %v pre-roll came back truncated from a %v buffer", MaxPreRoll, BufferLength)
	}
	if c.Duration < MaxPreRoll {
		t.Errorf("clip is %v long, want at least %v", c.Duration, MaxPreRoll)
	}
}

// The owner may widen the clip filter with clip_lowpass_hz (SPEC.md section 15
// decision 18). A clip recorded at a 1 kHz cutoff must be a 2 kHz file that
// really holds sound up to 1 kHz. The same 700 Hz tone must leave nothing at
// the default cutoff, so the two settings are proved apart and not merely
// labeled apart.
func TestAWiderCutoffWritesAWiderClip(t *testing.T) {
	tone := func(s float64) float64 { return 0.5 * math.Sin(2*math.Pi*700*s) }

	wide, err := NewRecorder(t.TempDir(), 30*time.Second, 1000)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if got := wide.OutputRate(); got != 2000 {
		t.Fatalf("OutputRate at a 1000 Hz cutoff = %d, want 2000", got)
	}
	feed(wide, t0, 10, tone)
	c, err := wide.Save(1, t0.Add(2*time.Second), t0.Add(9*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rate, samples := readWAV(t, c.Path)
	if rate != 2000 {
		t.Errorf("stored rate = %d, want 2000", rate)
	}
	if len(samples) != 14000 {
		t.Errorf("stored %d samples, want 14000: seven seconds at 2000 Hz", len(samples))
	}
	if c.Duration != 7*time.Second {
		t.Errorf("duration = %v, want 7s", c.Duration)
	}
	peak := 0.0
	for _, v := range samples {
		peak = math.Max(peak, math.Abs(v))
	}
	// The tone goes in at 0.5. The Butterworth takes 0.24 dB off it at
	// 0.7 of the cutoff, so 0.486 comes out.
	if peak < 0.44 || peak > 0.52 {
		t.Errorf("the 700 Hz tone peaks at %.3f in the wide clip, want about 0.486", peak)
	}

	narrow, _ := newRecorder(t)
	if got := narrow.OutputRate(); got != 1000 {
		t.Fatalf("OutputRate at the default cutoff = %d, want 1000", got)
	}
	feed(narrow, t0, 10, tone)
	c, err = narrow.Save(1, t0.Add(2*time.Second), t0.Add(9*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rate, samples = readWAV(t, c.Path)
	if rate != 1000 {
		t.Errorf("stored rate at the default cutoff = %d, want 1000", rate)
	}
	for k, v := range samples {
		if math.Abs(v) > 1.0/32767+1e-9 {
			t.Fatalf("the default cutoff kept the 700 Hz tone: sample %d = %d steps, want at most 1",
				k, int(math.Round(v*32767)))
		}
	}
}

// A cutoff that is not one of the choices must be refused before anything is
// recorded, and the message must say what may be used instead.
func TestNewRecorderRefusesACutoffThatIsNotAChoice(t *testing.T) {
	r, err := NewRecorder(t.TempDir(), 30*time.Second, 750)
	if err == nil {
		t.Fatal("NewRecorder with a 750 Hz cutoff returned a recorder, want an error")
	}
	if r != nil {
		t.Error("NewRecorder returned both a recorder and an error")
	}
	for _, want := range []string{"500", "12000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not list %s", err, want)
		}
	}
}

// A clip that ends one output period past the newest sample is ready: that
// end asks for everything that has arrived and nothing more. The pipeline
// leans on this when capture stops before the post-roll, and the period
// follows clip_lowpass_hz, so this has to hold at every rate.
func TestSaveIsReadyOnePeriodPastTheNewestSample(t *testing.T) {
	for _, tc := range []struct {
		cutoff float64
		period time.Duration
	}{
		{500, time.Millisecond}, {1000, 500 * time.Microsecond},
		{4000, 125 * time.Microsecond}, {12000, 41666 * time.Nanosecond},
	} {
		r, err := NewRecorder(t.TempDir(), 30*time.Second, tc.cutoff)
		if err != nil {
			t.Fatalf("NewRecorder(%g): %v", tc.cutoff, err)
		}
		feed(r, t0, 5, silence)
		latest := r.Latest()
		if latest.IsZero() {
			t.Fatalf("%g Hz: no audio was stored", tc.cutoff)
		}
		c, err := r.Save(1, t0.Add(time.Second), latest.Add(tc.period))
		if err != nil {
			t.Errorf("%g Hz: Save one period past the newest sample: %v", tc.cutoff, err)
			continue
		}
		if _, samples := readWAV(t, c.Path); len(samples) == 0 {
			t.Errorf("%g Hz: the clip is empty", tc.cutoff)
		}
	}
}
