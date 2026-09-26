package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// FileSource streams a 48 kHz, 24-bit, stereo PCM WAV file as chunks, so the
// pipeline can run on recorded or synthetic audio. Start times come from the
// sample count after start. Unlike live capture, it waits when the output
// channel is full: a file can wait, and nothing is dropped.
type FileSource struct {
	f       *os.File
	data    io.Reader
	channel int
	start   time.Time
	out     chan<- Chunk
}

// NewFileSource opens path and checks that it is PCM, 2 channels, 48000 Hz,
// 24-bit.
func NewFileSource(path string, channel int, start time.Time, out chan<- Chunk) (*FileSource, error) {
	if channel < 0 || channel >= Channels {
		return nil, fmt.Errorf("audio: channel %d does not exist; use 0 or 1", channel)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("audio: %w", err)
	}
	data, err := findPCMData(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("audio: %s: %w", path, err)
	}
	return &FileSource{f: f, data: data, channel: channel, start: start, out: out}, nil
}

// findPCMData reads the RIFF header and chunks up to the data chunk, checks
// the format, and returns a reader for the sample data.
func findPCMData(r io.Reader) (io.Reader, error) {
	le := binary.LittleEndian
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF WAVE file")
	}
	formatOK := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, fmt.Errorf("no data chunk: %w", err)
		}
		id, size := string(ch[0:4]), int64(le.Uint32(ch[4:]))
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("fmt chunk is %d bytes, want at least 16", size)
			}
			b := make([]byte, size)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, fmt.Errorf("reading fmt chunk: %w", err)
			}
			format, channels := le.Uint16(b[0:]), le.Uint16(b[2:])
			rate, bits := le.Uint32(b[4:]), le.Uint16(b[14:])
			if format == 0xFFFE && size >= 26 { // WAVE_FORMAT_EXTENSIBLE: the sub-format holds the code
				format = le.Uint16(b[24:])
			}
			if format != 1 || channels != Channels || rate != SampleRate || bits != 24 {
				return nil, fmt.Errorf("format %d, %d channels, %d Hz, %d-bit; want PCM (1), 2 channels, 48000 Hz, 24-bit",
					format, channels, rate, bits)
			}
			formatOK = true
		case "data":
			if !formatOK {
				return nil, errors.New("data chunk comes before the fmt chunk")
			}
			return io.LimitReader(r, size), nil
		default:
			if _, err := io.CopyN(io.Discard, r, size); err != nil {
				return nil, fmt.Errorf("skipping %q chunk: %w", id, err)
			}
		}
		if size%2 == 1 { // chunks are padded to an even size
			if _, err := io.CopyN(io.Discard, r, 1); err != nil {
				return nil, fmt.Errorf("skipping pad byte: %w", err)
			}
		}
	}
}

// Run sends the whole file as chunks of up to 100 ms and returns nil at the
// end of the data, or ctx.Err() if ctx is done first.
func (s *FileSource) Run(ctx context.Context) error {
	defer s.f.Close()
	frames := NewFrameReader(s.data, Channels, s.channel)
	var offset int64
	for {
		buf := make([]float64, SampleRate/10)
		n, err := frames.Read(buf)
		if n > 0 {
			chunk := Chunk{Samples: buf[:n], Start: s.start.Add(samplesDuration(offset)), Stream: 1, Offset: offset}
			select {
			case s.out <- chunk:
			case <-ctx.Done():
				return ctx.Err()
			}
			offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("audio: reading WAV data: %w", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// samplesDuration converts a sample count to a duration without overflow.
func samplesDuration(n int64) time.Duration {
	return time.Duration(n/SampleRate)*time.Second + time.Duration(n%SampleRate)*time.Second/SampleRate
}
