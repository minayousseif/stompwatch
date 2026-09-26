package audio

import (
	"errors"
	"io"
	"math"
)

// Capture format of the EM-01 (SPEC.md section 6.1): S24_3LE, 2 channels, 48 kHz.
// The device has no mono and no 32-bit mode.
const (
	SampleRate     = 48000
	Channels       = 2
	bytesPerSample = 3
	fullScale      = 1 << 23
)

// DecodeS24LE3 decodes whole interleaved S24_3LE frames from b and writes
// the given channel to out, scaled so that full scale is +/-1. It returns the
// number of samples written: the smaller of the whole frames in b and
// len(out).
//
// Each sample is three bytes, little-endian, sign-extended from bit 23.
func DecodeS24LE3(b []byte, channels, channel int, out []float64) int {
	frame := channels * bytesPerSample
	n := min(len(b)/frame, len(out))
	off := channel * bytesPerSample
	for i := 0; i < n; i++ {
		out[i] = decodeSample(b[i*frame+off:])
	}
	return n
}

func decodeSample(p []byte) float64 {
	v := int32(p[0]) | int32(p[1])<<8 | int32(p[2])<<16
	if v&0x800000 != 0 {
		v |= ^0xFFFFFF // sign-extend
	}
	return float64(v) / fullScale
}

// FrameReader reads raw S24_3LE audio and returns decoded samples of one
// channel. A read that ends inside a frame keeps the partial frame for the
// next call.
type FrameReader struct {
	r        io.Reader
	channels int
	channel  int
	buf      []byte
	have     int
}

// NewFrameReader returns a reader for interleaved audio with the given
// channel count that returns the given channel.
func NewFrameReader(r io.Reader, channels, channel int) *FrameReader {
	return &FrameReader{r: r, channels: channels, channel: channel}
}

// Read decodes up to len(out) samples. It makes one read from the source,
// and more only if it has less than one whole frame. At the end of the
// source it returns the source's error; if a partial frame is left over, it
// returns io.ErrUnexpectedEOF.
func (f *FrameReader) Read(out []float64) (int, error) {
	frame := f.channels * bytesPerSample
	need := max(len(out)*frame, frame)
	if cap(f.buf) < need {
		nb := make([]byte, need)
		copy(nb, f.buf[:f.have])
		f.buf = nb
	}
	f.buf = f.buf[:need]

	var err error
	if f.have < need {
		var m int
		m, err = f.r.Read(f.buf[f.have:])
		f.have += m
	}
	for f.have < frame && err == nil {
		var m int
		m, err = f.r.Read(f.buf[f.have:])
		f.have += m
	}

	n := DecodeS24LE3(f.buf[:f.have], f.channels, f.channel, out)
	used := n * frame
	f.have = copy(f.buf, f.buf[used:f.have])
	if n > 0 {
		return n, nil
	}
	if err != nil && f.have > 0 && errors.Is(err, io.EOF) {
		return 0, io.ErrUnexpectedEOF
	}
	return 0, err
}

// ChannelRMS returns the RMS level of each channel in whole S24_3LE frames,
// scaled so that full scale is 1. It is logged at startup so a wrong
// capture channel is visible at once.
func ChannelRMS(b []byte, channels int) []float64 {
	frame := channels * bytesPerSample
	frames := len(b) / frame
	rms := make([]float64, channels)
	if frames == 0 {
		return rms
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < channels; c++ {
			v := decodeSample(b[i*frame+c*bytesPerSample:])
			rms[c] += v * v
		}
	}
	for c := range rms {
		rms[c] = math.Sqrt(rms[c] / float64(frames))
	}
	return rms
}
