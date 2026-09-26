package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// Chunk is a block of decoded samples from one channel.
type Chunk struct {
	Samples []float64 // full scale is +/-1
	Start   time.Time // host time of the first sample
	Stream  int64     // 1 for the first arecord run, one more per restart
	// Offset is the number of samples before this chunk in its stream.
	// A chunk whose Offset is not the end of the previous chunk means
	// chunks were dropped in between.
	Offset int64
}

// CaptureEventKind names what happened to the capture process.
type CaptureEventKind int

const (
	CaptureStarted CaptureEventKind = iota // the first audio of a stream arrived
	CaptureExited                          // arecord ended; a restart follows
	CaptureStuck                           // the audio stopped changing
)

func (k CaptureEventKind) String() string {
	switch k {
	case CaptureStarted:
		return "started"
	case CaptureExited:
		return "exited"
	case CaptureStuck:
		return "stuck"
	}
	return fmt.Sprintf("CaptureEventKind(%d)", int(k))
}

// CaptureEvent reports a change in the capture process.
type CaptureEvent struct {
	Kind   CaptureEventKind
	At     time.Time
	Stream int64
	// Gap is set on CaptureStarted after a restart: the time from the end of
	// the previous stream's last sample to this stream's first sample.
	Gap time.Duration
	// Detail describes an exit or a stuck stream, with arecord's last
	// error output.
	Detail string
}

// CaptureConfig sets up a Capture.
type CaptureConfig struct {
	Device  string // must be a hw: device
	Channel int    // 0 or 1

	// Command is the program and any leading arguments. The capture
	// arguments from Args are added after them. nil means arecord.
	Command []string
	Env     []string // extra environment variables

	ChunkSize int // most samples per chunk; 0 means 4800 (100 ms)

	// Out receives chunks. A full channel drops the chunk and counts it;
	// the reader never waits (SPEC.md section 3.5).
	Out chan<- Chunk
	// OnEvent is called on the reader goroutine and must return quickly.
	OnEvent func(CaptureEvent)

	MinBackoff time.Duration // 0 means 1 s
	MaxBackoff time.Duration // 0 means 60 s
	StuckLimit time.Duration // 0 means 5 s
	Now        func() time.Time
}

const (
	// A stream that ran this long resets the restart wait.
	healthyRun = 30 * time.Second
	stderrKeep = 2048
)

var errStuck = errors.New("audio stopped changing")

// Capture runs arecord, decodes its output, and restarts it when it exits or
// its audio gets stuck.
type Capture struct {
	cfg     CaptureConfig
	dropped atomic.Int64
}

// NewCapture checks the config. It refuses any device that is not hw:.
func NewCapture(cfg CaptureConfig) (*Capture, error) {
	if err := ValidateDevice(cfg.Device); err != nil {
		return nil, err
	}
	if cfg.Channel < 0 || cfg.Channel >= Channels {
		return nil, fmt.Errorf("audio: capture channel %d does not exist; use 0 or 1", cfg.Channel)
	}
	if cfg.Out == nil {
		return nil, errors.New("audio: capture has no output channel")
	}
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"arecord"}
	}
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = SampleRate / 10
	}
	if cfg.OnEvent == nil {
		cfg.OnEvent = func(CaptureEvent) {}
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = time.Minute
	}
	if cfg.StuckLimit == 0 {
		cfg.StuckLimit = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Capture{cfg: cfg}, nil
}

// Args returns the arecord arguments for raw S24_3LE stereo 48 kHz capture
// from device (SPEC.md section 6.1). The buffer and period times ask ALSA for a
// 20 ms period, which keeps read latency and its jitter small.
func Args(device string) []string {
	return []string{
		"-q", "-D", device, "-f", "S24_3LE", "-r", "48000", "-c", "2", "-t", "raw",
		"--buffer-time=200000", "--period-time=20000",
	}
}

// Dropped is the number of chunks dropped because Out was full.
func (c *Capture) Dropped() int64 { return c.dropped.Load() }

// Run captures until ctx is done, and then returns ctx.Err(). Every exit of
// arecord is reported and followed by a restart after a wait that doubles on
// each quick failure, up to MaxBackoff.
func (c *Capture) Run(ctx context.Context) error {
	backoff := c.cfg.MinBackoff
	var lastEnd time.Time
	for stream := int64(1); ; stream++ {
		began := c.cfg.Now()
		detail := c.runStream(ctx, stream, &lastEnd)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.cfg.OnEvent(CaptureEvent{Kind: CaptureExited, At: c.cfg.Now(), Stream: stream, Detail: detail})

		if c.cfg.Now().Sub(began) >= healthyRun {
			backoff = c.cfg.MinBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, c.cfg.MaxBackoff)
	}
}

// runStream runs arecord once and returns a description of how it ended.
func (c *Capture) runStream(ctx context.Context, stream int64, lastEnd *time.Time) string {
	sctx, kill := context.WithCancel(ctx)
	defer kill()

	args := append(slices.Clone(c.cfg.Command[1:]), Args(c.cfg.Device)...)
	cmd := exec.CommandContext(sctx, c.cfg.Command[0], args...)
	cmd.Env = append(os.Environ(), c.cfg.Env...)
	cmd.WaitDelay = 2 * time.Second
	stderr := &tailBuffer{keep: stderrKeep}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "opening pipe: " + err.Error()
	}
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("starting %s: %v", c.cfg.Command[0], err)
	}

	stamps := NewTimestamper(SampleRate, 30*time.Second, time.Second)
	frames := NewFrameReader(stdout, Channels, c.cfg.Channel)
	stuck := NewStuckDetector(SampleRate, c.cfg.StuckLimit)
	var offset int64
	var readErr error
	for {
		buf := make([]float64, c.cfg.ChunkSize)
		n, err := frames.Read(buf)
		if n > 0 {
			now := c.cfg.Now()
			start := stamps.Stamp(now, n)
			if offset == 0 {
				var gap time.Duration
				if !lastEnd.IsZero() {
					gap = start.Sub(*lastEnd)
				}
				c.cfg.OnEvent(CaptureEvent{Kind: CaptureStarted, At: now, Stream: stream, Gap: gap})
			}
			*lastEnd = start.Add(time.Duration(n) * time.Second / SampleRate)

			isStuck := stuck.Observe(buf[:n])
			select {
			case c.cfg.Out <- Chunk{Samples: buf[:n], Start: start, Stream: stream, Offset: offset}:
			default:
				c.dropped.Add(1)
			}
			offset += int64(n)

			if isStuck {
				c.cfg.OnEvent(CaptureEvent{Kind: CaptureStuck, At: now, Stream: stream,
					Detail: fmt.Sprintf("audio unchanged for more than %v", c.cfg.StuckLimit)})
				readErr = errStuck
				kill()
				break
			}
		}
		if err != nil {
			readErr = err
			break
		}
	}
	waitErr := cmd.Wait()
	return describeExit(readErr, waitErr, stderr.String())
}

func describeExit(readErr, waitErr error, stderr string) string {
	var parts []string
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		parts = append(parts, "read: "+readErr.Error())
	}
	if waitErr != nil {
		parts = append(parts, "exit: "+waitErr.Error())
	} else {
		parts = append(parts, "exited with status 0")
	}
	if s := strings.TrimSpace(stderr); s != "" {
		parts = append(parts, "stderr: "+s)
	}
	return strings.Join(parts, "; ")
}

// tailBuffer keeps the last keep bytes written to it.
type tailBuffer struct {
	keep int
	buf  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.keep; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.buf) }
