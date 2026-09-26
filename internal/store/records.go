package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/meter"
)

// Media kinds.
const (
	KindAudio = "audio"
	KindVideo = "video"
)

// Review statuses.
const (
	StatusVerified = "verified"
	StatusRejected = "rejected"
	StatusUnsure   = "unsure"
)

// Health record kinds.
const (
	HealthCaptureGap    = "capture_gap"
	HealthStuckStream   = "stuck_stream"
	HealthFrameDrop     = "frame_drop"
	HealthClockStep     = "clock_step"
	HealthClockDrift    = "clock_drift"
	HealthGainChange    = "gain_change"
	HealthWriteError    = "write_error"
	HealthDiskLow       = "disk_low"
	HealthLoopRestart   = "loop_restart"
	HealthClipTruncated = "clip_truncated"
	// HealthClipNoAudio marks a video clip that was cut with camera_audio on
	// and came out with no usable audio track. It is its own kind, not a
	// truncated clip: the clip holds the whole window, and what is missing is
	// the sound. The owner has to learn this from the instrument
	// (SPEC.md section 15 decision 22).
	HealthClipNoAudio  = "clip_no_audio"
	HealthMediaMissing = "media_missing"
	// HealthMediaPurge marks recordings the owner deleted on purpose. Rule 3
	// of SPEC.md section 3 says every deletion is logged, and a purge is the
	// only deletion this program performs.
	HealthMediaPurge       = "media_purge"
	HealthCameraDisconnect = "camera_disconnect"
	// HealthSettingsChange marks a settings change that interrupted the
	// measurement, such as a baseline that had to start again. A quiet hour
	// after a change is then explainable rather than mysterious.
	HealthSettingsChange = "settings_change"
	// HealthTailscaleFunnel marks the dashboard being published to the
	// public internet by Tailscale Funnel. auth_mode trusts an identity
	// header that is only safe while tailscale serve is the sole path to
	// the loopback listener (SPEC.md section 9.0), so Funnel being on
	// invalidates the whole access story and has to be seen.
	HealthTailscaleFunnel = "tailscale_funnel"
	// HealthDataReset marks the record that stompwatch reset removed the
	// recorded data: when, by which user, and what went. It is written into
	// the new database the reset leaves behind, so the audit trail is the one
	// thing that survives a wipe (SPEC.md section 3 rule 3).
	HealthDataReset = "data_reset"
	// HealthRecordingPause marks the start and the end of the daily
	// recording pause, in which no event opens and no clip or video is
	// taken. A stretch with no events is then a choice the owner made and
	// not a silent failure (SPEC.md section 3 rule 4).
	HealthRecordingPause = "recording_pause"
	// HealthSettingsChanged records who changed which settings from the
	// dashboard, and each value before and after. It is the audit record,
	// and it is not HealthSettingsChange, which marks a change that
	// interrupted the measurement. system_health rows are never edited,
	// which is what an audit record needs.
	HealthSettingsChanged = "settings_changed"
	// HealthMuteWindowAdded and HealthMuteWindowRemoved record who added or
	// removed a mute window, and which window. A mute window takes events
	// out of the headline counts, so who did it has to be on the record.
	HealthMuteWindowAdded   = "mute_window_added"
	HealthMuteWindowRemoved = "mute_window_removed"
)

// InsertBins stores one-second bins in a single transaction and updates the
// minute rollup. A bin whose second is already stored is skipped and not
// counted in the rollup; the number skipped is returned so the caller can
// record it. The rest of the batch is still stored.
func (s *Store) InsertBins(ctx context.Context, bins []meter.Bin) (int, error) {
	if len(bins) == 0 {
		return 0, nil
	}
	var conflicts int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		conflicts = 0
		insert := tx.StmtContext(ctx, s.insertBin)
		get := tx.StmtContext(ctx, s.getMinute)
		put := tx.StmtContext(ctx, s.putMinute)
		for _, b := range bins {
			sec := b.Start.Unix()
			res, err := insert.ExecContext(ctx, sec, b.LAeq, b.LAmax, b.LowBand, b.HighBand, b.Baseline)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n == 0 {
				conflicts++
				continue
			}
			if err := addToMinute(ctx, get, put, sec-sec%60, b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: inserting %d bins: %w", len(bins), err)
	}
	return conflicts, nil
}

// addToMinute folds one bin into its minute row. The mean is an energy mean:
// the stored mean times n gives back the energy sum.
func addToMinute(ctx context.Context, get, put *sql.Stmt, minute int64, b meter.Bin) error {
	var lmin, lmean, lmax, amax float64
	var n int64
	err := get.QueryRowContext(ctx, minute).Scan(&lmin, &lmean, &lmax, &amax, &n)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		lmin, lmean, lmax, amax, n = b.LAeq, b.LAeq, b.LAeq, b.LAmax, 1
	case err != nil:
		return err
	default:
		energy := math.Pow(10, lmean/10)*float64(n) + math.Pow(10, b.LAeq/10)
		n++
		lmean = 10 * math.Log10(energy/float64(n))
		lmin, lmax, amax = math.Min(lmin, b.LAeq), math.Max(lmax, b.LAeq), math.Max(amax, b.LAmax)
	}
	_, err = put.ExecContext(ctx, minute, lmin, lmean, lmax, amax, n)
	return err
}

// InsertEvent stores an event and returns its id.
func (s *Store) InsertEvent(ctx context.Context, e detect.Event, created time.Time) (int64, error) {
	var id int64
	err := s.retryBusy(ctx, func() error {
		res, err := s.insertEvent.ExecContext(ctx,
			e.Start.UnixMilli(), e.End.UnixMilli(), e.Duration().Milliseconds(),
			e.LAeq, e.LAmax, e.BaselineAtTrigger,
			e.LowBand, e.HighBand, e.LowHighRatioDB,
			string(e.Class), e.Confidence, EncodeEnvelope(e.Envelope), boolInt(e.Forced),
			created.UnixMilli(),
			nullFloat(e.HasContext, e.JumpDB), nullFloat(e.HasContext, e.RiseDB))
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: inserting event: %w", err)
	}
	return id, nil
}

// Media is one clip file that belongs to an event.
type Media struct {
	EventID  int64
	Kind     string
	Path     string
	Bytes    int64
	Duration time.Duration
	SHA256   string
	// Started is the time of the clip's first sample. It is the zero time for
	// a row written before the collector recorded it, which means "not
	// recorded" and never the epoch.
	Started time.Time
	// Clipped counts samples that hit the 16-bit limit. Anything above zero
	// means the clip is distorted at those moments.
	Clipped   int
	Truncated bool
	// CameraAudio is true when a video clip really carries the camera's own
	// audio track: the writer read the file back and found one, which is not
	// the same as camera_audio having been on when the clip was cut. It is
	// always false for an audio clip, which is the measuring microphone's and
	// is filtered (SPEC.md section 15 decision 22).
	CameraAudio bool
	// Purged is set when the file was deliberately deleted, and nil when it
	// was not. It is how a caller tells a purged clip from one that has
	// vanished without a second query: a vanished clip is a fault and gets a
	// health record, a purged one is something the owner did on purpose.
	// Every other field of this row keeps the value the collector wrote.
	Purged *Purge
}

// InsertMedia stores a media row. Each event has at most one row per kind.
func (s *Store) InsertMedia(ctx context.Context, m Media) error {
	err := s.retryBusy(ctx, func() error {
		_, err := s.w.ExecContext(ctx, `INSERT INTO event_media
			(event_id, kind, path, bytes, duration_ms, sha256, started_ms, clipped, truncated, camera_audio)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.EventID, m.Kind, m.Path, m.Bytes, m.Duration.Milliseconds(), m.SHA256,
			msOrZero(m.Started), m.Clipped, boolInt(m.Truncated), boolInt(m.CameraAudio))
		return err
	})
	if err != nil {
		return fmt.Errorf("store: inserting %s media for event %d: %w", m.Kind, m.EventID, err)
	}
	return nil
}

// Review is a person's decision about an event.
type Review struct {
	EventID  int64
	Status   string
	Note     string
	Reviewer string
	At       time.Time
}

// SetReview stores or replaces the review of an event. It writes only to
// event_review.
func (s *Store) SetReview(ctx context.Context, r Review) error {
	err := s.retryBusy(ctx, func() error {
		_, err := s.w.ExecContext(ctx, `INSERT INTO event_review (event_id, status, note, reviewer, reviewed_ms)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (event_id) DO UPDATE SET status = excluded.status, note = excluded.note,
				reviewer = excluded.reviewer, reviewed_ms = excluded.reviewed_ms`,
			r.EventID, r.Status, r.Note, r.Reviewer, r.At.UnixMilli())
		return err
	})
	if err != nil {
		return fmt.Errorf("store: saving review for event %d: %w", r.EventID, err)
	}
	return nil
}

// AddHealth stores a health record, for example a capture gap.
func (s *Store) AddHealth(ctx context.Context, at time.Time, kind, detail string, d time.Duration) error {
	err := s.retryBusy(ctx, func() error {
		_, err := s.w.ExecContext(ctx, `INSERT INTO system_health (ts_ms, kind, detail, duration_ms) VALUES (?, ?, ?, ?)`,
			at.UnixMilli(), kind, detail, d.Milliseconds())
		return err
	})
	if err != nil {
		return fmt.Errorf("store: adding %s health record: %w", kind, err)
	}
	return nil
}

// IntegrityCheck runs PRAGMA integrity_check and returns an error that lists
// every problem SQLite reports.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	rows, err := s.r.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("store: integrity check: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("store: integrity check: %w", err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: integrity check: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("store: integrity check failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Snapshot writes a consistent copy of the database to path with VACUUM INTO
// and returns the SHA-256 of the copy. It fails if path exists.
func (s *Store) Snapshot(ctx context.Context, path string) (string, error) {
	if _, err := s.r.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("store: snapshot to %s: %w", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("store: snapshot: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("store: syncing snapshot: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("store: hashing snapshot: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EncodeEnvelope packs envelope samples as float32 little-endian.
func EncodeEnvelope(env []float64) []byte {
	b := make([]byte, 4*len(env))
	for i, v := range env {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(float32(v)))
	}
	return b
}

// DecodeEnvelope unpacks a blob written by EncodeEnvelope.
func DecodeEnvelope(b []byte) ([]float64, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("store: envelope blob has %d bytes, not a multiple of 4", len(b))
	}
	env := make([]float64, len(b)/4)
	for i := range env {
		env[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])))
	}
	return env, nil
}

// msOrZero writes the zero time as a plain 0, the column's "not recorded".
// UnixMilli of the zero time is a huge negative number, which would read as a
// real instant in the year 1.
func msOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// nullFloat is v when ok, and NULL otherwise. A measurement the detector
// could not make must not reach the database as a 0, which would read as a
// level that did not move.
func nullFloat(ok bool, v float64) any {
	if !ok {
		return nil
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
