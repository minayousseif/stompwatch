package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const driverName = "sqlite"

// Writer settings from SPEC.md section 6.9.1. Transactions begin IMMEDIATE so a
// transaction never has to upgrade from a read lock to a write lock.
const writerParams = "_txlock=immediate" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)" +
	"&_pragma=foreign_keys(ON)" +
	"&_pragma=temp_store(MEMORY)" +
	"&_pragma=cache_size(-65536)" +
	"&_pragma=mmap_size(268435456)"

const readerParams = "mode=ro&_pragma=busy_timeout(5000)"

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store is the SQLite database. It holds two handles: a writer with exactly
// one connection, so writes never contend with each other, and a read-only
// pool for queries. WAL lets reads run while the writer writes.
type Store struct {
	path string
	w    *sql.DB
	r    *sql.DB

	insertBin   *sql.Stmt
	getMinute   *sql.Stmt
	putMinute   *sql.Stmt
	insertEvent *sql.Stmt
}

// Open opens or creates the database at path and applies any pending
// migrations. path must be on a local filesystem.
func Open(path string) (*Store, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: invalid database path %q", path)
	}
	ctx := context.Background()

	w, err := sql.Open(driverName, "file:"+path+"?"+writerParams)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	s := &Store{path: path, w: w}

	if err := s.migrate(ctx); err != nil {
		w.Close()
		return nil, err
	}

	r, err := sql.Open(driverName, "file:"+path+"?"+readerParams)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	r.SetMaxOpenConns(4)
	s.r = r
	if err := r.PingContext(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("store: opening reader: %w", err)
	}

	if err := s.prepare(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close closes both handles.
func (s *Store) Close() error {
	for _, st := range []*sql.Stmt{s.insertBin, s.getMinute, s.putMinute, s.insertEvent} {
		if st != nil {
			st.Close()
		}
	}
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	errs = append(errs, s.w.Close())
	return errors.Join(errs...)
}

// SchemaVersion returns the highest applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.r.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	return v, err
}

type migration struct {
	version int
	name    string
	sql     string
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// loadMigrations reads NNNN_name.sql files from fsys. Versions must start
// at 1 and have no gaps.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: reading migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("store: migration file %q: name must be NNNN_name.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: reading %s: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(b)})
	}
	if len(ms) == 0 {
		return nil, errors.New("store: no migrations found")
	}
	slices.SortFunc(ms, func(a, b migration) int { return a.version - b.version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration %s: want version %d", m.name, i+1)
		}
	}
	return ms, nil
}

func (s *Store) migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	ms, err := loadMigrations(sub)
	if err != nil {
		return err
	}

	if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_ms INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: creating schema_version: %w", err)
	}
	var current int
	if err := s.w.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("store: reading schema version: %w", err)
	}
	if current > len(ms) {
		return fmt.Errorf("store: database schema version %d is newer than this program (%d)", current, len(ms))
	}

	for _, m := range ms[current:] {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version, applied_ms) VALUES (?, ?)`,
				m.version, time.Now().UnixMilli())
			return err
		})
		if err != nil {
			return fmt.Errorf("store: applying migration %s: %w", m.name, err)
		}
	}
	return nil
}

func (s *Store) prepare(ctx context.Context) error {
	var err error
	prep := func(q string) *sql.Stmt {
		if err != nil {
			return nil
		}
		var st *sql.Stmt
		st, err = s.w.PrepareContext(ctx, q)
		return st
	}
	s.insertBin = prep(`INSERT OR IGNORE INTO samples_1s (ts, laeq, lamax, low_energy, high_energy, baseline)
		VALUES (?, ?, ?, ?, ?, ?)`)
	s.getMinute = prep(`SELECT laeq_min, laeq_mean, laeq_max, lamax_max, n FROM samples_1m WHERE ts = ?`)
	s.putMinute = prep(`INSERT INTO samples_1m (ts, laeq_min, laeq_mean, laeq_max, lamax_max, n)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (ts) DO UPDATE SET laeq_min = excluded.laeq_min, laeq_mean = excluded.laeq_mean,
			laeq_max = excluded.laeq_max, lamax_max = excluded.lamax_max, n = excluded.n`)
	s.insertEvent = prep(`INSERT INTO events (started_ms, ended_ms, duration_ms, laeq, lamax, baseline_at_trigger,
			low_energy, high_energy, low_high_ratio, class, confidence, envelope, forced, created_ms,
			jump_db, rise_db)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: preparing statements: %w", err)
	}
	return nil
}

// inTx runs fn in a write transaction. It retries the whole transaction if
// SQLite reports that the database is busy, and returns the last error if
// the database stays busy.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.retryBusy(ctx, func() error {
		tx, err := s.w.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	})
}

const (
	busyAttempts   = 5
	busyFirstDelay = 50 * time.Millisecond
)

// retryBusy runs fn and retries it with a doubling delay while it fails with
// SQLITE_BUSY. Each attempt already waits up to busy_timeout inside SQLite.
func (s *Store) retryBusy(ctx context.Context, fn func() error) error {
	delay := busyFirstDelay
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !isBusy(err) || attempt == busyAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func isBusy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == sqlite3.SQLITE_BUSY
}
