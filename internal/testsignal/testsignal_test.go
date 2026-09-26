package testsignal

import (
	"bytes"
	"math"
	"testing"
)

func TestPinkHasRequestedRMS(t *testing.T) {
	x := Pink(Rate, 0.001, 1)
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	if rms := math.Sqrt(sum / float64(len(x))); math.Abs(rms-0.001) > 1e-9 {
		t.Fatalf("RMS = %g, want 0.001", rms)
	}
}

// 0.5 x 8388607 = 4194303.5 rounds to 4194304 = 0x400000.
func TestWriteWAV24SampleBytes(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteWAV24(&buf, []float64{0.5, -1}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) != 44+12 {
		t.Fatalf("file is %d bytes, want 56", len(b))
	}
	want := []byte{0x00, 0x00, 0x40, 0x00, 0x00, 0x40, 0x01, 0x00, 0x80, 0x01, 0x00, 0x80}
	if !bytes.Equal(b[44:], want) {
		t.Fatalf("samples % X, want % X", b[44:], want)
	}
}
