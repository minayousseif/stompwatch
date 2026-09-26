package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// EventKind names what happened to the ffmpeg process.
type EventKind int

const (
	Started     EventKind = iota // the first segment of a stream appeared
	Exited                       // ffmpeg ended; a restart follows
	Stuck                        // no segment grew for the stall limit
	PruneFailed                  // an old segment could not be removed
	Paused                       // ffmpeg was stopped for the daily recording pause
)

func (k EventKind) String() string {
	switch k {
	case Started:
		return "started"
	case Exited:
		return "exited"
	case Stuck:
		return "stuck"
	case PruneFailed:
		return "prune_failed"
	case Paused:
		return "paused"
	}
	return fmt.Sprintf("EventKind(%d)", int(k))
}

// Event reports a change in the ffmpeg process. It has the same shape as
// audio.CaptureEvent, so the command handles both the same way.
type Event struct {
	Kind   EventKind
	At     time.Time
	Stream int64
	// Gap is set on Started after a restart: the time from the last data
	// of the previous stream to the first segment of this one.
	Gap time.Duration
	// Detail describes an exit or a stall, with ffmpeg's last error output,
	// password masked.
	Detail string
	// AfterPause is set on Started when the stream before it was stopped
	// for the daily recording pause. The time between the two is a choice
	// the owner made, not a gap in the video, and must not be reported as
	// one.
	AfterPause bool
}

// Outage is a span with no video.
type Outage struct {
	From, To time.Time
}

// RingConfig sets up a Ring.
type RingConfig struct {
	Dir string // the ring directory; created if missing
	// Streams are the URLs to try. Each restart moves to the next one, so
	// a camera on older firmware is found by its second path without a
	// probe having to succeed first (SPEC.md section 7).
	Streams        []Stream
	SegmentSeconds int
	Keep           time.Duration // how much of the ring to keep
	// Audio records the camera's own audio track into the segments. It is
	// camera_audio, and false drops the audio as SPEC.md section 7 says.
	Audio bool

	Command Command  // nil means ffmpeg
	Env     []string // extra environment variables for ffmpeg

	// OnEvent is called on the supervisor goroutine and must return quickly.
	OnEvent func(Event)

	// Paused reports whether an instant is inside the daily recording
	// pause. Inside it the ring runs no ffmpeg, so nothing from the camera
	// reaches the disk. It is read at every poll, so a change from the
	// dashboard applies at once. nil means the ring is never paused.
	Paused func(time.Time) bool

	MinBackoff time.Duration // 0 means 1 s
	MaxBackoff time.Duration // 0 means 60 s
	StallLimit time.Duration // 0 means three segments
	Poll       time.Duration // 0 means 1 s
	Now        func() time.Time
}

const (
	// A stream that ran this long resets the restart wait.
	healthyRun = 30 * time.Second
	stderrKeep = 2048
	// keepOutages bounds the outage list. A clip only needs the recent ones.
	keepOutages = 100
)

var errStalled = errors.New("no segment grew")

// Ring keeps the last few minutes of the camera's sub-stream on disk. It
// runs ffmpeg, restarts it when it exits or stalls, prunes the ring, and
// keeps count of how long the camera was actually delivering video.
type Ring struct {
	cfg RingConfig

	mu             sync.Mutex
	path           string // the RTSP path the current stream uses
	connected      bool
	connectedSince time.Time
	uptime         time.Duration // of streams that have ended
	disconnects    int64
	lastData       time.Time // when a segment last grew
	outageFrom     time.Time // set while no stream delivers
	outages        []Outage
	afterPause     bool // the last stream was stopped for the pause
}

// RingStatus is what the system page shows.
type RingStatus struct {
	// Path is the RTSP path the ring is using now. With camera_rtsp_path
	// empty the ring works through the defaults in turn, so this is the
	// only way to say which one is in force.
	Path           string
	Connected      bool
	ConnectedSince time.Time // zero when not connected
	Uptime         time.Duration
	Disconnects    int64
	LastSegment    time.Time // zero before the first one
	Segments       int
	Bytes          int64
}

// NewRing checks the config and creates the ring directory.
func NewRing(cfg RingConfig) (*Ring, error) {
	switch {
	case cfg.Dir == "":
		return nil, errors.New("video: no ring directory")
	case len(cfg.Streams) == 0:
		return nil, errors.New("video: no camera stream")
	case cfg.SegmentSeconds < 1:
		return nil, errors.New("video: segment length must be at least 1 s")
	case cfg.Keep < time.Duration(cfg.SegmentSeconds)*time.Second:
		return nil, errors.New("video: the ring must keep at least one segment")
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("video: %w", err)
	}
	if cfg.OnEvent == nil {
		cfg.OnEvent = func(Event) {}
	}
	if cfg.MinBackoff == 0 {
		cfg.MinBackoff = time.Second
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = time.Minute
	}
	if cfg.StallLimit == 0 {
		cfg.StallLimit = 3 * time.Duration(cfg.SegmentSeconds) * time.Second
	}
	if cfg.Poll == 0 {
		cfg.Poll = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Ring{cfg: cfg, path: cfg.Streams[0].Path}, nil
}

// Dir is the ring directory.
func (r *Ring) Dir() string { return r.cfg.Dir }

// SegmentLength is the nominal length of one segment.
func (r *Ring) SegmentLength() time.Duration {
	return time.Duration(r.cfg.SegmentSeconds) * time.Second
}

// Segments lists the ring, oldest first.
func (r *Ring) Segments() ([]Segment, error) { return ListSegments(r.cfg.Dir) }

// RecordsAudio reports whether the command line this ring runs really records
// an audio track that can be decoded later. The collector says at start that
// the camera's audio is being recorded, and that sentence must come from here
// rather than from the setting: for a day it came from the setting, and every
// clip was silent while the log said otherwise.
func (r *Ring) RecordsAudio() bool {
	return keepsUsableAudio(RingArgs(r.streamFor(1), r.cfg.SegmentSeconds, SegmentPattern, r.cfg.Audio))
}

// Status reports the camera's state now.
func (r *Ring) Status() RingStatus {
	r.mu.Lock()
	st := RingStatus{
		Path:        r.path,
		Connected:   r.connected,
		Uptime:      r.uptime,
		Disconnects: r.disconnects,
		LastSegment: r.lastData,
	}
	if r.connected {
		st.ConnectedSince = r.connectedSince
		st.Uptime += r.cfg.Now().Sub(r.connectedSince)
	}
	r.mu.Unlock()
	segs, _ := r.Segments()
	st.Segments = len(segs)
	for _, s := range segs {
		st.Bytes += s.Size
	}
	return st
}

// Outages returns the spans with no video since the ring started, newest
// last. A span that is still open has a zero To.
func (r *Ring) Outages() []Outage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.outages)
	if !r.outageFrom.IsZero() {
		out = append(out, Outage{From: r.outageFrom})
	}
	return out
}

// Run records until ctx is done, and then returns ctx.Err(). Every exit of
// ffmpeg is reported and followed by a restart after a wait that doubles on
// each quick failure, up to MaxBackoff.
func (r *Ring) Run(ctx context.Context) error {
	backoff := r.cfg.MinBackoff
	for stream := int64(1); ; stream++ {
		if err := r.waitOutPause(ctx); err != nil {
			return err
		}
		began := r.cfg.Now()
		detail, forPause := r.runStream(ctx, stream)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if forPause {
			// A stop for the pause is not a failure, so it neither waits
			// nor grows the restart backoff.
			backoff = r.cfg.MinBackoff
			continue
		}
		r.cfg.OnEvent(Event{Kind: Exited, At: r.cfg.Now(), Stream: stream, Detail: detail})

		if r.cfg.Now().Sub(began) >= healthyRun {
			backoff = r.cfg.MinBackoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, r.cfg.MaxBackoff)
	}
}

// paused reports whether the ring should be stopped at now. It asks about
// now and about one poll ahead, so ffmpeg is stopped before the pause begins
// rather than up to one poll after it, and is started again only once the
// pause is over. Either way no video from inside the pause is written.
func (r *Ring) paused(now time.Time) bool {
	return r.cfg.Paused != nil && (r.cfg.Paused(now) || r.cfg.Paused(now.Add(r.cfg.Poll)))
}

// waitOutPause returns at once outside the pause. Inside it, it waits,
// polling, until the pause is over or ctx is done.
func (r *Ring) waitOutPause(ctx context.Context) error {
	if !r.paused(r.cfg.Now()) {
		return nil
	}
	tick := time.NewTicker(r.cfg.Poll)
	defer tick.Stop()
	for r.paused(r.cfg.Now()) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	return nil
}

// runStream runs ffmpeg once and returns a description of how it ended, and
// whether it was stopped for the daily recording pause.
func (r *Ring) runStream(ctx context.Context, stream int64) (string, bool) {
	sctx, kill := context.WithCancel(ctx)
	defer kill()

	src := r.streamFor(stream)
	r.mu.Lock()
	r.path = src.Path
	r.mu.Unlock()
	args := append(slices.Clone(r.cfg.Command.leading()),
		RingArgs(src, r.cfg.SegmentSeconds, filepath.Join(r.cfg.Dir, SegmentPattern), r.cfg.Audio)...)
	cmd := exec.CommandContext(sctx, r.cfg.Command.program(), args...)
	cmd.Env = childEnv(r.cfg.Env)
	// An interrupt lets ffmpeg close the segment it is writing. The wait
	// delay kills it if it does not.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 3 * time.Second
	stderr := &tailBuffer{keep: stderrKeep}
	cmd.Stderr = stderr
	cmd.Stdout = nil
	if err := cmd.Start(); err != nil {
		return src.Redact(fmt.Sprintf("%s: starting %s: %v", src, r.cfg.Command.program(), err)), false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// The stream is watched through the ring directory: a segment that is
	// growing is video arriving. ffmpeg's exit status alone cannot say that.
	var runErr error
	started, forPause := false, false
	var newest string
	var newestSize int64
	lastGrowth := r.cfg.Now()
	tick := time.NewTicker(r.cfg.Poll)
	defer tick.Stop()
watch:
	for {
		select {
		case waitErr := <-done:
			runErr = waitErr
			break watch
		case <-tick.C:
			now := r.cfg.Now()
			if r.paused(now) {
				kill()
				runErr = <-done
				forPause = true
				break watch
			}
			name, size := r.newest()
			if name != "" && (name != newest || size != newestSize) {
				if !started {
					started = true
					r.streamStarted(now, stream)
				} else {
					r.mu.Lock()
					r.lastData = now
					r.mu.Unlock()
				}
				if name != newest {
					// A new segment closes the previous one: prune now,
					// while the newest is fresh.
					if _, err := Prune(r.cfg.Dir, r.cfg.Keep); err != nil {
						r.cfg.OnEvent(Event{Kind: PruneFailed, At: now, Stream: stream, Detail: err.Error()})
					}
				}
				newest, newestSize, lastGrowth = name, size, now
				continue
			}
			if now.Sub(lastGrowth) > r.cfg.StallLimit {
				r.cfg.OnEvent(Event{Kind: Stuck, At: now, Stream: stream,
					Detail: fmt.Sprintf("no segment grew for more than %v", r.cfg.StallLimit)})
				kill()
				runErr = <-done
				if runErr == nil {
					runErr = errStalled
				}
				break watch
			}
		}
	}
	if forPause {
		r.streamPaused(r.cfg.Now(), started)
		r.cfg.OnEvent(Event{Kind: Paused, At: r.cfg.Now(), Stream: stream,
			Detail: "stopped for the daily recording pause"})
		return "", true
	}
	r.streamEnded(r.cfg.Now(), started)
	return src.Redact(fmt.Sprintf("%s: %s", src, describeExit(runErr, stderr.String()))), false
}

// streamFor is the URL the given stream number uses.
func (r *Ring) streamFor(stream int64) Stream {
	return r.cfg.Streams[(stream-1)%int64(len(r.cfg.Streams))]
}

// newest returns the name and size of the newest segment, or "" and 0.
func (r *Ring) newest() (string, int64) {
	entries, err := os.ReadDir(r.cfg.Dir)
	if err != nil {
		return "", 0
	}
	name := ""
	for _, e := range entries {
		if _, ok := ParseSegmentName(e.Name()); ok && e.Name() > name {
			name = e.Name()
		}
	}
	if name == "" {
		return "", 0
	}
	info, err := os.Stat(filepath.Join(r.cfg.Dir, name))
	if err != nil {
		return "", 0
	}
	return name, info.Size()
}

func (r *Ring) streamStarted(now time.Time, stream int64) {
	r.mu.Lock()
	var gap time.Duration
	if !r.outageFrom.IsZero() {
		gap = now.Sub(r.outageFrom)
		r.outages = append(r.outages, Outage{From: r.outageFrom, To: now})
		if len(r.outages) > keepOutages {
			r.outages = r.outages[len(r.outages)-keepOutages:]
		}
		r.outageFrom = time.Time{}
	}
	r.connected, r.connectedSince, r.lastData = true, now, now
	afterPause := r.afterPause
	r.afterPause = false
	r.mu.Unlock()
	r.cfg.OnEvent(Event{Kind: Started, At: now, Stream: stream, Gap: gap, AfterPause: afterPause})
}

// streamPaused ends a stream that was stopped for the daily recording pause.
// Its uptime counts, but it is not a disconnect and it opens no outage: no
// clip covers the pause, so no clip can fall into this gap. An outage that
// was already open, from a camera that was down before the pause, is closed
// here so it is still on the list.
func (r *Ring) streamPaused(now time.Time, started bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if started {
		r.uptime += now.Sub(r.connectedSince)
	}
	if !r.outageFrom.IsZero() {
		r.outages = append(r.outages, Outage{From: r.outageFrom, To: now})
		if len(r.outages) > keepOutages {
			r.outages = r.outages[len(r.outages)-keepOutages:]
		}
		r.outageFrom = time.Time{}
	}
	r.connected = false
	r.afterPause = true
}

func (r *Ring) streamEnded(now time.Time, started bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !started {
		if r.outageFrom.IsZero() {
			r.outageFrom = now
		}
		return
	}
	r.uptime += now.Sub(r.connectedSince)
	r.connected = false
	r.disconnects++
	r.outageFrom = r.lastData
}

func describeExit(waitErr error, stderr string) string {
	var parts []string
	switch {
	case errors.Is(waitErr, errStalled):
		parts = append(parts, "stopped after a stall")
	case waitErr != nil:
		parts = append(parts, "exit: "+waitErr.Error())
	default:
		parts = append(parts, "exited with status 0")
	}
	if s := strings.TrimSpace(stderr); s != "" {
		parts = append(parts, "stderr: "+s)
	}
	return strings.Join(parts, "; ")
}
