package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// WALSizeWarning is the -wal file size above which the collector logs a
// warning (SPEC.md section 6.9.1). A WAL that keeps growing means a reader is
// holding an old snapshot and checkpoints cannot finish.
const WALSizeWarning = 64 << 20

// WALSize returns the size of the -wal file in bytes. A missing file is 0.
func (s *Store) WALSize() (int64, error) {
	fi, err := os.Stat(s.path + "-wal")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: %w", err)
	}
	return fi.Size(), nil
}

// Checkpoint copies the WAL into the database and truncates the -wal file.
// Run it when the collector is idle and after the nightly integrity job. It
// returns an error if an active reader stopped it from finishing.
func (s *Store) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	err := s.w.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		return fmt.Errorf("store: checkpoint: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("store: checkpoint did not finish: %d of %d WAL frames copied while a reader was active",
			checkpointed, logFrames)
	}
	return nil
}
