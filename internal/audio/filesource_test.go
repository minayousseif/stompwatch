package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type wavSpec struct {
	riff     string
	rate     int
	channels int
	bits     int
	list     bool // add an odd-sized LIST chunk before the data
	frames   int  // stereo 24-bit frames with L = i, R = -i
}

func writeTestWAV(t *testing.T, s wavSpec) string {
	t.Helper()
	le := binary.LittleEndian
	var b []byte
	u16 := func(v int) { b = le.AppendUint16(b, uint16(v)) }
	u32 := func(v int) { b = le.AppendUint32(b, uint32(v)) }

	b = append(b, s.riff...)
	u32(0) // size, fixed below
	b = append(b, "WAVEfmt "...)
	u32(16)
	u16(1)
	u16(s.channels)
	u32(s.rate)
	u32(s.rate * s.channels * s.bits / 8)
	u16(s.channels * s.bits / 8)
	u16(s.bits)
	if s.list {
		b = append(b, "LIST"...)
		u32(5)
		b = append(b, "INFOx"...)
		b = append(b, 0) // pad byte for the odd size
	}
	b = append(b, "data"...)
	u32(s.frames * s.channels * s.bits / 8)
	for i := 0; i < s.frames; i++ {
		for _, v := range []int32{int32(i), int32(-i)}[:s.channels] {
			b = append(b, byte(v), byte(v>>8), byte(v>>16))
		}
	}
	le.PutUint32(b[4:], uint32(len(b)-8))

	path := filepath.Join(t.TempDir(), "in.wav")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileSourceStreamsChunksWithSampleClock(t *testing.T) {
	path := writeTestWAV(t, wavSpec{riff: "RIFF", rate: 48000, channels: 2, bits: 24, list: true, frames: 10000})
	out := make(chan Chunk, 100)
	start := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	src, err := NewFileSource(path, 1, start, out)
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	if err := src.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(out)

	var got []float64
	for ch := range out {
		if ch.Offset != int64(len(got)) || ch.Stream != 1 {
			t.Fatalf("chunk offset %d stream %d, want %d and 1", ch.Offset, ch.Stream, len(got))
		}
		if want := start.Add(time.Duration(ch.Offset) * time.Second / SampleRate); !ch.Start.Equal(want) {
			t.Fatalf("chunk at offset %d starts %v, want %v", ch.Offset, ch.Start, want)
		}
		got = append(got, ch.Samples...)
	}
	if len(got) != 10000 {
		t.Fatalf("got %d samples, want 10000", len(got))
	}
	for i, v := range got {
		if want := -float64(i) / (1 << 23); v != want {
			t.Fatalf("sample %d = %v, want %v", i, v, want)
		}
	}
}

func TestFileSourceRejectsOtherFormats(t *testing.T) {
	for name, s := range map[string]wavSpec{
		"44.1 kHz": {riff: "RIFF", rate: 44100, channels: 2, bits: 24, frames: 10},
		"16-bit":   {riff: "RIFF", rate: 48000, channels: 2, bits: 16, frames: 10},
		"mono":     {riff: "RIFF", rate: 48000, channels: 1, bits: 24, frames: 10},
		"not RIFF": {riff: "RIFX", rate: 48000, channels: 2, bits: 24, frames: 10},
	} {
		if _, err := NewFileSource(writeTestWAV(t, s), 0, time.Now(), make(chan Chunk)); err == nil {
			t.Errorf("%s: NewFileSource returned no error", name)
		}
	}
}

func TestFileSourceStopsWhenCancelled(t *testing.T) {
	path := writeTestWAV(t, wavSpec{riff: "RIFF", rate: 48000, channels: 2, bits: 24, frames: 48000})
	src, err := NewFileSource(path, 0, time.Now(), make(chan Chunk)) // nobody reads
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
