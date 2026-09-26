package audio

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
	"testing/iotest"
)

// oddReader returns reads of changing sizes that end inside frames.
type oddReader struct {
	data  []byte
	sizes []int
	i     int
}

func (r *oddReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(r.sizes[r.i%len(r.sizes)], len(p), len(r.data))
	r.i++
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

// SPEC.md section 6.1: three bytes little-endian, sign-extended from bit 23.
// A sign-extension bug gives plausible audio at a wrong level.
func TestDecodeS24LE3KnownPatterns(t *testing.T) {
	tests := []struct {
		name string
		b    [3]byte
		want float64
	}{
		{"zero", [3]byte{0x00, 0x00, 0x00}, 0},
		{"smallest positive", [3]byte{0x01, 0x00, 0x00}, 1.0 / 8388608},
		{"largest positive 0x7FFFFF", [3]byte{0xFF, 0xFF, 0x7F}, 8388607.0 / 8388608},
		{"most negative 0x800000", [3]byte{0x00, 0x00, 0x80}, -1},
		{"minus one step 0xFFFFFF", [3]byte{0xFF, 0xFF, 0xFF}, -1.0 / 8388608},
		{"half 0x400000", [3]byte{0x00, 0x00, 0x40}, 0.5},
		{"minus half 0xC00000", [3]byte{0x00, 0x00, 0xC0}, -0.5},
		{"byte order 0x010203", [3]byte{0x03, 0x02, 0x01}, 66051.0 / 8388608},
	}
	for _, tc := range tests {
		out := make([]float64, 1)
		n := DecodeS24LE3(tc.b[:], 1, 0, out)
		if n != 1 || out[0] != tc.want {
			t.Errorf("%s: got %d samples, value %v; want 1 sample, %v", tc.name, n, out[0], tc.want)
		}
	}
}

// Frames are interleaved L, R. Channel 1 must read the second 3 bytes.
func TestDecodeS24LE3SelectsChannel(t *testing.T) {
	b := []byte{
		0x01, 0x00, 0x00, 0x00, 0x00, 0x80, // frame 0: L = 1 step, R = -1
		0x00, 0x00, 0x40, 0x00, 0x00, 0xC0, // frame 1: L = 0.5, R = -0.5
		0xFF, // part of the next frame
	}
	left := make([]float64, 4)
	right := make([]float64, 4)
	if n := DecodeS24LE3(b, 2, 0, left); n != 2 || left[0] != 1.0/8388608 || left[1] != 0.5 {
		t.Errorf("left: %d samples %v", n, left[:2])
	}
	if n := DecodeS24LE3(b, 2, 1, right); n != 2 || right[0] != -1 || right[1] != -0.5 {
		t.Errorf("right: %d samples %v", n, right[:2])
	}
	if n := DecodeS24LE3(b, 2, 0, left[:1]); n != 1 {
		t.Errorf("with room for 1 sample, decoded %d", n)
	}
}

// Pipe reads can end inside a frame. The reader must carry the partial frame
// to the next read, not drop or misalign it.
func TestFrameReaderCarriesPartialFrames(t *testing.T) {
	var raw []byte
	var want []float64
	for i := 0; i < 1000; i++ {
		l := int32(i*7919%16777216) - 8388608
		r := int32(-i)
		raw = append(raw, byte(l), byte(l>>8), byte(l>>16), byte(r), byte(r>>8), byte(r>>16))
		want = append(want, float64(l)/8388608)
	}

	for name, src := range map[string]io.Reader{
		"one byte per read": iotest.OneByteReader(bytes.NewReader(raw)),
		"odd read sizes":    &oddReader{data: raw, sizes: []int{7, 5, 13, 1, 20}},
	} {
		fr := NewFrameReader(src, 2, 0)
		var got []float64
		buf := make([]float64, 64)
		var err error
		for err == nil {
			var n int
			n, err = fr.Read(buf)
			got = append(got, buf[:n]...)
		}
		if err != io.EOF {
			t.Errorf("%s: final error = %v, want io.EOF", name, err)
		}
		if len(got) != len(want) {
			t.Errorf("%s: got %d samples, want %d", name, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: sample %d = %v, want %v", name, i, got[i], want[i])
				break
			}
		}
	}
}

// A stream that ends inside a frame lost data. That must be an error, not a
// quiet end.
func TestFrameReaderReportsTruncatedFrameAtEnd(t *testing.T) {
	raw := []byte{0x00, 0x00, 0x40, 0x00, 0x00, 0x00, 0x7F} // one frame, then 1 byte
	fr := NewFrameReader(bytes.NewReader(raw), 2, 0)
	buf := make([]float64, 8)
	if n, err := fr.Read(buf); n != 1 || err != nil || buf[0] != 0.5 {
		t.Fatalf("first Read = %d, %v, %v; want 1 sample of 0.5", n, err, buf[0])
	}
	if n, err := fr.Read(buf); n != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("second Read = %d, %v; want 0 and io.ErrUnexpectedEOF", n, err)
	}
}

// Per-channel RMS is logged at startup so a wrong capture_channel is visible.
func TestChannelRMS(t *testing.T) {
	var b []byte
	for i := 0; i < 100; i++ {
		b = append(b, 0x00, 0x00, 0x40) // L = 0.5
		b = append(b, 0x00, 0x00, 0x00) // R = 0
	}
	rms := ChannelRMS(b, 2)
	if len(rms) != 2 || math.Abs(rms[0]-0.5) > 1e-12 || rms[1] != 0 {
		t.Fatalf("ChannelRMS = %v, want [0.5 0]", rms)
	}
}
