package clip

import (
	"encoding/binary"
	"io"
	"math"
)

// encodeWAV writes samples as a 16-bit mono PCM WAV file at rate. A sample
// of 1.0 is 32767. Samples outside the 16-bit range, and NaN, are clipped;
// the count of clipped samples is returned.
func encodeWAV(w io.Writer, samples []float64, rate int) (int, error) {
	const (
		channels      = 1
		bitsPerSample = 16
		blockAlign    = channels * bitsPerSample / 8
	)
	le := binary.LittleEndian
	dataSize := len(samples) * blockAlign

	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	le.PutUint32(hdr[4:], uint32(36+dataSize))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	le.PutUint32(hdr[16:], 16) // fmt chunk size
	le.PutUint16(hdr[20:], 1)  // PCM
	le.PutUint16(hdr[22:], channels)
	le.PutUint32(hdr[24:], uint32(rate))
	le.PutUint32(hdr[28:], uint32(rate*blockAlign))
	le.PutUint16(hdr[32:], blockAlign)
	le.PutUint16(hdr[34:], bitsPerSample)
	copy(hdr[36:], "data")
	le.PutUint32(hdr[40:], uint32(dataSize))
	if _, err := w.Write(hdr); err != nil {
		return 0, err
	}

	body := make([]byte, dataSize)
	clipped := 0
	for i, v := range samples {
		q := math.Round(v * 32767)
		switch {
		case math.IsNaN(q):
			q = 0
			clipped++
		case q > 32767:
			q = 32767
			clipped++
		case q < -32768:
			q = -32768
			clipped++
		}
		le.PutUint16(body[i*blockAlign:], uint16(int16(q)))
	}
	_, err := w.Write(body)
	return clipped, err
}
