package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/minayousseif/stompwatch/internal/audio"
	"github.com/minayousseif/stompwatch/internal/clip"
	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/dsp"
	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/meter"
	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/testsignal"
)

var t0 = time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testSettings(dir string) config.Config {
	s := config.Default()
	s.DBPath = filepath.Join(dir, "noise.db")
	s.ClipDir = filepath.Join(dir, "clips")
	s.ExpectedCaptureGain = "none"
	// Pin the clip lengths rather than following the defaults. These tests
	// are about the pipeline, and the synthetic audio is only two minutes
	// long, so a default-length pre-roll would run off the front of it.
	s.PreRoll = 10 * time.Second
	s.PostRoll = 5 * time.Second
	return s
}

type env struct {
	dir      string
	settings config.Config
	store    *store.Store
	recorder *clip.Recorder
	chunks   chan audio.Chunk
	pipe     *Pipeline
}

func newEnv(t *testing.T) *env { return newEnvWith(t, 16, nil) }

// newEnvWith builds a pipeline with a chunk queue of the given size and any
// change to the settings. A queue of 0 makes the hand-off between the test and
// the DSP loop a rendezvous, so a test can change a setting between two chunks
// and know which chunk sees it first.
func newEnvWith(t *testing.T, chunkCap int, change func(*config.Config)) *env {
	t.Helper()
	dir := t.TempDir()
	settings := testSettings(dir)
	if change != nil {
		change(&settings)
	}
	e := &env{dir: dir, settings: settings, chunks: make(chan audio.Chunk, chunkCap)}
	var err error
	if e.store, err = store.Open(e.settings.DBPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.store.Close() })
	if e.recorder, err = clip.NewRecorder(e.settings.ClipDir, 30*time.Second, dsp.DefaultClipLowpassHz); err != nil {
		t.Fatal(err)
	}
	e.pipe, err = New(Config{
		Settings:    e.settings,
		Store:       e.store,
		Recorder:    e.recorder,
		Calibration: meter.NoCalibration{},
		Chunks:      e.chunks,
		Status:      health.NewStatus(t0),
		Log:         quietLog(),
		Now:         func() time.Time { return t0 },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func (e *env) query(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.settings.DBPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// SPEC.md section 5 and section 11: the whole pipeline on a synthetic WAV file, run under
// the race detector. The signal is 125 s of pink noise with 40 s of running
// (longer than the 30 s audio buffer) and four jumps.
func TestPipelineEndToEndFromWAV(t *testing.T) {
	e := newEnv(t)

	x := testsignal.Pink(125*testsignal.Rate, 0.001, 42)
	for k := 0; k < 100; k++ {
		testsignal.AddThump(x, 45+0.4*float64(k), 0.5)
	}
	testsignal.AddThumps(x, 0.5, 100, 102, 104, 106)
	wavPath := filepath.Join(e.dir, "in.wav")
	var buf bytes.Buffer
	if err := testsignal.WriteWAV24(&buf, x); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wavPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := audio.NewFileSource(wavPath, 0, t0, e.chunks)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	srcErr := make(chan error, 1)
	go func() { srcErr <- src.Run(ctx); close(e.chunks) }()

	if err := e.pipe.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := <-srcErr; err != nil {
		t.Fatalf("file source: %v", err)
	}

	db := e.query(t)
	if n := count(t, db, `SELECT count(*) FROM samples_1s`); n != 125 {
		t.Errorf("samples_1s has %d rows, want 125", n)
	}
	if n := count(t, db, `SELECT count(*) FROM samples_1m`); n != 3 {
		t.Errorf("samples_1m has %d rows, want 3", n)
	}
	if n := count(t, db, `SELECT count(*) FROM system_health WHERE kind IN ('write_error', 'frame_drop', 'loop_restart')`); n != 0 {
		t.Errorf("system_health has %d failure rows, want 0", n)
	}

	rows, err := db.Query(`SELECT e.id, e.started_ms, e.class, m.path, m.sha256, m.duration_ms,
			m.started_ms, m.truncated
		FROM events e LEFT JOIN event_media m ON m.event_id = e.id AND m.kind = 'audio' ORDER BY e.started_ms`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type result struct {
		id, started             int64
		class                   string
		path, sum               sql.NullString
		durMs, clipStart, trunc sql.NullInt64
	}
	var got []result
	for rows.Next() {
		var r result
		if err := rows.Scan(&r.id, &r.started, &r.class, &r.path, &r.sum, &r.durMs,
			&r.clipStart, &r.trunc); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	for i, want := range []struct {
		startS   float64
		class    string
		minDurMs int64
	}{
		{45, "running", 50000}, // 10 s pre-roll + 40 s of running + 5 s post-roll
		{100, "jumping", 20000},
	} {
		r := got[i]
		if d := float64(r.started-t0.UnixMilli())/1000 - want.startS; d < -0.3 || d > 0.3 {
			t.Errorf("event %d starts at %.2f s, want %.0f s", i, float64(r.started-t0.UnixMilli())/1000, want.startS)
		}
		if r.class != want.class {
			t.Errorf("event %d class %q, want %q", i, r.class, want.class)
		}
		if !r.path.Valid {
			t.Errorf("event %d has no audio clip", i)
			continue
		}
		b, err := os.ReadFile(r.path.String)
		if err != nil {
			t.Errorf("event %d clip: %v", i, err)
			continue
		}
		if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != r.sum.String {
			t.Errorf("event %d clip hash does not match event_media", i)
		}
		if r.durMs.Int64 < want.minDurMs || r.trunc.Int64 != 0 {
			t.Errorf("event %d clip is %d ms, truncated %d; want at least %d ms and not truncated",
				i, r.durMs.Int64, r.trunc.Int64, want.minDurMs)
		}
		// The clip holds the 10 s of pre-roll the default settings ask for,
		// so its first sample is 10 s before the event started.
		if d := r.clipStart.Int64 - (r.started - 10_000); d < -300 || d > 300 {
			t.Errorf("event %d clip starts at %d, %d ms from the 10 s of pre-roll before %d",
				i, r.clipStart.Int64, d, r.started)
		}
	}
}

func silentChunk(start time.Time, offset int64, n int) audio.Chunk {
	return audio.Chunk{Samples: make([]float64, n), Start: start, Stream: 1, Offset: offset}
}

// A dropped chunk shows as a gap in the offsets. It must be recorded with
// the number of samples lost.
func TestDroppedChunksAreRecorded(t *testing.T) {
	e := newEnv(t)
	go func() {
		e.chunks <- silentChunk(t0, 0, 48000)
		e.chunks <- silentChunk(t0.Add(1100*time.Millisecond), 52800, 48000) // 4800 samples missing
		close(e.chunks)
	}()
	if err := e.pipe.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := e.query(t)
	var detail string
	if err := db.QueryRow(`SELECT detail FROM system_health WHERE kind = 'frame_drop'`).Scan(&detail); err != nil {
		t.Fatalf("no frame_drop health row: %v", err)
	}
	if !strings.Contains(detail, "4800") {
		t.Errorf("frame_drop detail %q does not give the 4800 lost samples", detail)
	}
	if got := e.pipe.Stats().DroppedSamples; got != 4800 {
		t.Errorf("Stats().DroppedSamples = %d, want 4800", got)
	}
}

// fakeStore fails the first failBins calls to InsertBins.
type fakeStore struct {
	mu       sync.Mutex
	failBins int
	calls    int
	bins     []meter.Bin
	health   []string
}

func (f *fakeStore) InsertBins(_ context.Context, b []meter.Bin) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failBins {
		return 0, errors.New("disk I/O error")
	}
	f.bins = append(f.bins, b...)
	return 0, nil
}
func (f *fakeStore) InsertEvent(context.Context, detect.Event, time.Time) (int64, error) {
	return 1, nil
}
func (f *fakeStore) InsertMedia(context.Context, store.Media) error { return nil }
func (f *fakeStore) AddHealth(_ context.Context, _ time.Time, kind, detail string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health = append(f.health, kind+": "+detail)
	return nil
}
func (f *fakeStore) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// SPEC.md section 6.9: a failed write is never swallowed. The bins stay queued and
// are written on a later commit, each failure is counted, and repeated
// failures raise an alert.
func TestFailedCommitsAreRetriedCountedAndAlerted(t *testing.T) {
	dir := t.TempDir()
	settings := testSettings(dir)
	fs := &fakeStore{failBins: 3}
	rec, err := clip.NewRecorder(settings.ClipDir, 30*time.Second, dsp.DefaultClipLowpassHz)
	if err != nil {
		t.Fatal(err)
	}
	chunks := make(chan audio.Chunk, 64)
	tick := make(chan time.Time)
	var alertMu sync.Mutex
	var alerts []string
	p, err := New(Config{
		Settings: settings, Store: fs, Recorder: rec, Calibration: meter.NoCalibration{},
		Chunks: chunks, Status: health.NewStatus(t0), Log: quietLog(),
		Now:        func() time.Time { return t0 },
		CommitTick: tick,
		Alert:      func(reason string) { alertMu.Lock(); alerts = append(alerts, reason); alertMu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ { // 3 s
		chunks <- silentChunk(t0.Add(time.Duration(i)*100*time.Millisecond), int64(i)*4800, 4800)
	}
	done := make(chan error, 1)
	go func() { done <- p.Run(context.Background()) }()

	deadline := time.After(10 * time.Second)
	for fs.callCount() < 3 {
		select {
		case tick <- t0:
		case <-deadline:
			t.Fatalf("only %d commit attempts in 10 s", fs.callCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(chunks)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if got := p.Stats().WriteFailures; got != 3 {
		t.Errorf("WriteFailures = %d, want 3", got)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.bins) != 3 {
		t.Errorf("stored %d bins after the retries, want all 3", len(fs.bins))
	}
	alertMu.Lock()
	defer alertMu.Unlock()
	if len(alerts) != 1 || !strings.Contains(alerts[0], "disk I/O error") {
		t.Errorf("alerts = %q, want one alert that includes the write error", alerts)
	}
}

func TestSuperviseRestartsAfterPanic(t *testing.T) {
	var restarts []string
	runs := 0
	err := supervise(context.Background(), "dsp", time.Millisecond,
		func(name string, cause any) { restarts = append(restarts, name) },
		func(context.Context) error {
			runs++
			if runs <= 2 {
				panic("boom")
			}
			return nil
		})
	if err != nil || runs != 3 || len(restarts) != 2 {
		t.Fatalf("supervise = %v after %d runs and %d restarts; want nil, 3, 2", err, runs, len(restarts))
	}
}

func TestSuperviseStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := supervise(ctx, "x", time.Millisecond, func(string, any) {}, func(ctx context.Context) error {
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("supervise = %v, want context.Canceled", err)
	}
}

func TestNewRejectsMissingParts(t *testing.T) {
	dir := t.TempDir()
	settings := testSettings(dir)
	rec, _ := clip.NewRecorder(settings.ClipDir, 30*time.Second, dsp.DefaultClipLowpassHz)
	good := Config{
		Settings: settings, Store: &fakeStore{}, Recorder: rec, Calibration: meter.NoCalibration{},
		Chunks: make(chan audio.Chunk), Status: health.NewStatus(t0), Log: quietLog(),
	}
	if _, err := New(good); err != nil {
		t.Fatalf("New with a complete config: %v", err)
	}
	for name, change := range map[string]func(*Config){
		"no store":       func(c *Config) { c.Store = nil },
		"no recorder":    func(c *Config) { c.Recorder = nil },
		"no calibration": func(c *Config) { c.Calibration = nil },
		"no chunks":      func(c *Config) { c.Chunks = nil },
		"no status":      func(c *Config) { c.Status = nil },
	} {
		c := good
		change(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: New returned no error", name)
		}
	}
}
