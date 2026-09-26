package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	// ErrExists means a clip file for the event is already on disk. Clips
	// are evidence and are never overwritten.
	ErrExists = errors.New("video: clip file already exists")
	// ErrNoVideo means no segment in the ring holds any of the window.
	ErrNoVideo = errors.New("video: no segment covers the event")
)

// Source is what a Writer reads from the ring.
type Source interface {
	Segments() ([]Segment, error)
	Outages() []Outage
	SegmentLength() time.Duration
}

// Clip describes a saved video clip.
type Clip struct {
	Path     string
	SHA256   string // hex SHA-256 of the whole file
	Bytes    int64
	Start    time.Time // start of the first segment, so at or before the window
	Duration time.Duration
	// Truncated is true when the segments do not hold the whole window:
	// the ring did not reach back far enough, the last segment was still
	// open, or the camera was away for part of it.
	Truncated bool
	// CameraAudio is true when the clip file really carries the camera's own
	// audio track. It is read back from the file that was just written, not
	// taken from the setting: the setting says what was asked for, and only
	// the file says what is there. A clip keeps what it was recorded with, so
	// this is stored with the clip and never read from the setting later
	// (SPEC.md section 15 decision 22).
	CameraAudio bool
	// AudioProblem says why a clip cut with camera_audio on does not carry a
	// usable audio track, or why that could not be found out. It is empty
	// when the setting was off, and when the track is there. A clip with a
	// problem is still written: it is evidence of the event either way, and
	// the owner is told what it is missing.
	AudioProblem string
}

// WriterConfig sets up a Writer.
type WriterConfig struct {
	Dir     string // clips go under Dir/YYYY/MM/DD
	Source  Source
	Command Command
	Env     []string
	// Audio keeps the camera's own audio track in the clip. It is
	// camera_audio, and false drops it as SPEC.md section 7 says.
	Audio bool
}

// Writer cuts event clips from the ring. It joins the copied segments that
// span the event window into one MP4 and writes it once, the way the audio
// recorder writes a clip: temporary file, sync, hard link, hash recorded.
type Writer struct {
	cfg WriterConfig
	// beforeRun sees the concat list before ffmpeg does. Tests use it.
	beforeRun func(list string)
}

// NewWriter checks the config.
func NewWriter(cfg WriterConfig) (*Writer, error) {
	switch {
	case cfg.Dir == "":
		return nil, errors.New("video: no clip directory")
	case cfg.Source == nil:
		return nil, errors.New("video: no segment source")
	}
	return &Writer{cfg: cfg}, nil
}

// Ready reports whether the segment that holds to has been closed, which
// is when a later one exists. A clip cut before then would stop short.
func (w *Writer) Ready(to time.Time) bool {
	segs, err := w.cfg.Source.Segments()
	if err != nil || len(segs) == 0 {
		return false
	}
	return !segs[len(segs)-1].Start.Before(to)
}

// Save writes the clip that spans from to to as root/YYYY/MM/DD/<id>.mp4,
// on the UTC date of from. It returns ErrNoVideo when the ring holds none
// of the window and ErrExists when the clip is already there.
func (w *Writer) Save(ctx context.Context, eventID int64, from, to time.Time) (Clip, error) {
	if !to.After(from) {
		return Clip{}, fmt.Errorf("video: end %v is not after start %v", to, from)
	}
	segs, err := w.cfg.Source.Segments()
	if err != nil {
		return Clip{}, err
	}
	nominal := w.cfg.Source.SegmentLength()
	sel, covered := Select(segs, from, to, nominal)
	if len(sel) == 0 {
		return Clip{}, ErrNoVideo
	}
	truncated := !covered
	for _, o := range w.cfg.Source.Outages() {
		if o.From.Before(to) && (o.To.IsZero() || o.To.After(from)) {
			truncated = true
		}
	}

	dir := filepath.Join(w.cfg.Dir, from.UTC().Format("2006"), from.UTC().Format("01"), from.UTC().Format("02"))
	path := filepath.Join(dir, fmt.Sprintf("%d.mp4", eventID))
	if _, err := os.Stat(path); err == nil {
		return Clip{}, fmt.Errorf("%w: %s", ErrExists, path)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Clip{}, fmt.Errorf("video: %w", err)
	}

	list, err := os.CreateTemp(dir, ".list.*.txt")
	if err != nil {
		return Clip{}, fmt.Errorf("video: %w", err)
	}
	defer os.Remove(list.Name())
	if _, err := list.WriteString(ConcatList(sel)); err != nil {
		list.Close()
		return Clip{}, fmt.Errorf("video: %w", err)
	}
	if err := list.Close(); err != nil {
		return Clip{}, fmt.Errorf("video: %w", err)
	}
	if w.beforeRun != nil {
		w.beforeRun(list.Name())
	}

	// ffmpeg writes the temporary file itself. It is in the clip's own
	// directory so the link at the end stays on one filesystem.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return Clip{}, fmt.Errorf("video: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	duration, err := w.concat(ctx, list.Name(), tmp.Name())
	if err != nil {
		return Clip{}, err
	}
	end := end(segs, slices.Index(segs, sel[len(sel)-1]), nominal)
	if duration == 0 {
		duration = end.Sub(sel[0].Start)
	}
	// What the file holds, read back from the file, before it is sealed.
	hasAudio, problem := w.checkAudio(ctx, tmp.Name())

	sum, size, err := finishFile(tmp.Name(), path, dir)
	if err != nil {
		return Clip{}, err
	}
	return Clip{
		Path:         path,
		SHA256:       sum,
		Bytes:        size,
		Start:        sel[0].Start,
		Duration:     duration,
		Truncated:    truncated,
		CameraAudio:  hasAudio,
		AudioProblem: problem,
	}, nil
}

// checkAudio reads the clip that was just written and says whether it really
// carries a usable audio track, and what is wrong when it does not.
//
// Nothing noticed the first time this broke. The ring copied the camera's
// audio, the AAC configuration that arrives out of band never reached a
// segment, ffmpeg dropped the track while joining, and every log line said
// the clip was fine. The file is the only witness, so the file is asked.
//
// With camera_audio off there is no audio to look for and no process is run.
func (w *Writer) checkAudio(ctx context.Context, path string) (bool, string) {
	if !w.cfg.Audio {
		return false, ""
	}
	a, err := w.inspect(ctx, path)
	switch {
	case err != nil:
		return false, "the clip could not be read back, so whether it carries the camera's audio is not known: " + err.Error()
	case !a.Present:
		return false, "camera_audio is on, and the clip carries no audio track at all"
	case a.RateHz == 0:
		return false, "the clip's audio track does not give its sample rate, so nothing can decode it"
	}
	return true, ""
}

// inspect runs ffmpeg over a file on disk and returns the audio track it
// lists, if any.
func (w *Writer) inspect(ctx context.Context, path string) (AudioInfo, error) {
	args := append(slices.Clone(w.cfg.Command.leading()), InspectArgs(path)...)
	cmd := exec.CommandContext(ctx, w.cfg.Command.program(), args...)
	cmd.Env = childEnv(w.cfg.Env)
	cmd.WaitDelay = 3 * time.Second
	stderr := &tailBuffer{keep: stderrKeep}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return AudioInfo{}, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	info, ok := ParseStreamInfo(stderr.String())
	if !ok {
		return AudioInfo{}, errors.New("ffmpeg listed no video stream in it")
	}
	return info.Audio, nil
}

// concat runs ffmpeg over the list into out and returns the length it
// reported.
func (w *Writer) concat(ctx context.Context, list, out string) (time.Duration, error) {
	args := append(slices.Clone(w.cfg.Command.leading()), ConcatArgs(list, out, w.cfg.Audio)...)
	cmd := exec.CommandContext(ctx, w.cfg.Command.program(), args...)
	cmd.Env = childEnv(w.cfg.Env)
	cmd.WaitDelay = 3 * time.Second
	var progress bytes.Buffer
	stderr := &tailBuffer{keep: stderrKeep}
	cmd.Stdout = &progress
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("video: joining segments: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	d, _ := ParseProgress(progress.String())
	return d, nil
}

// finishFile syncs the temporary file, makes it read-only, links it to
// path, and returns its hash and size. The link fails if path exists, so an
// existing clip is never replaced. The temporary file is removed by the
// caller in every case.
func finishFile(tmp, path, dir string) (sum string, size int64, err error) {
	f, err := os.Open(tmp)
	if err != nil {
		return "", 0, fmt.Errorf("video: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if size, err = io.Copy(h, f); err != nil {
		return "", 0, fmt.Errorf("video: hashing %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return "", 0, fmt.Errorf("video: syncing %s: %w", path, err)
	}
	if err := os.Chmod(tmp, 0o400); err != nil {
		return "", 0, fmt.Errorf("video: %w", err)
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", 0, fmt.Errorf("%w: %s", ErrExists, path)
		}
		return "", 0, fmt.Errorf("video: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
