package logs

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// The log may hold the detail of a failure, so the group can read it but
// nobody else can. This matches how the rest of the program treats /data.
const (
	dirMode  fs.FileMode = 0o750
	fileMode fs.FileMode = 0o640
)

// Config sets up a Writer.
type Config struct {
	Dir      string // the directory; it is created if missing
	Name     string // base file name, for example "stompwatch.log"
	MaxBytes int64  // rotate when the file would pass this size
	Keep     int    // files kept in total, including the current one
}

// Writer is an io.Writer that writes whole lines to a file and rotates it
// when it grows past a limit. Its Write is safe for concurrent use.
type Writer struct {
	c Config

	mu   sync.Mutex
	f    *os.File
	size int64
}

// NewWriter creates the directory if it is missing and opens the current log
// file for appending.
func NewWriter(c Config) (*Writer, error) {
	switch {
	case c.Name == "":
		return nil, errors.New("logs: the log file needs a name")
	case c.Name != filepath.Base(c.Name) || c.Name == "." || c.Name == "..":
		return nil, fmt.Errorf("logs: %q must be a file name, not a path", c.Name)
	case c.MaxBytes < 1:
		return nil, fmt.Errorf("logs: max bytes is %d; the file needs a size to rotate at", c.MaxBytes)
	case c.Keep < 1:
		return nil, fmt.Errorf("logs: keep is %d; the current file always counts as one", c.Keep)
	}
	if err := os.MkdirAll(c.Dir, dirMode); err != nil {
		return nil, fmt.Errorf("logs: %w", err)
	}
	w := &Writer{c: c}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// path names the current file for n = 0 and an older one for n above 0.
func (w *Writer) path(n int) string { return path(w.c.Dir, w.c.Name, n) }

func path(dir, name string, n int) string {
	if n == 0 {
		return filepath.Join(dir, name)
	}
	return filepath.Join(dir, fmt.Sprintf("%s.%d", name, n))
}

// open opens the current file for appending and reads the size it already
// has, so a restart continues the file instead of losing it.
func (w *Writer) open() error {
	f, err := os.OpenFile(w.path(0), os.O_CREATE|os.O_WRONLY|os.O_APPEND, fileMode)
	if err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	// Set the mode as well as ask for it: the umask takes bits off a new
	// file, and a file from an older version may have the wrong mode.
	if err := f.Chmod(fileMode); err != nil {
		f.Close()
		return fmt.Errorf("logs: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logs: %w", err)
	}
	w.f, w.size = f, fi.Size()
	return nil
}

// Write appends the records in p to the current file. p may hold several
// newline-terminated records; each one lands whole in one file. A record
// with no newline gets one, so the next record cannot run into it.
func (w *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, errors.New("logs: the log file is closed")
	}
	done := 0
	for done < len(p) {
		rest := p[done:]
		rec := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			rec = rest[:i+1]
		} else {
			rec = append(append(make([]byte, 0, len(rest)+1), rest...), '\n')
		}
		if err := w.record(rec); err != nil {
			return done, err
		}
		done += min(len(rec), len(rest))
	}
	return done, nil
}

// record writes one whole record, rotating first if it would take the file
// past MaxBytes. A record longer than MaxBytes goes into a file of its own
// rather than being cut in half.
func (w *Writer) record(rec []byte) error {
	if w.size > 0 && w.size+int64(len(rec)) > w.c.MaxBytes {
		if err := w.rotate(); err != nil {
			return err
		}
	}
	n, err := w.f.Write(rec)
	w.size += int64(n)
	if err != nil {
		return fmt.Errorf("logs: writing the log: %w", err)
	}
	return nil
}

// rotate closes the current file, moves every kept file one number up, and
// starts a new current file. The oldest file falls off the end and is
// deleted. With Keep = 1 the current file is itself the oldest.
func (w *Writer) rotate() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	w.f, w.size = nil, 0
	if err := remove(w.path(w.c.Keep - 1)); err != nil {
		return err
	}
	for n := w.c.Keep - 2; n >= 0; n-- {
		if err := os.Rename(w.path(n), w.path(n+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("logs: rotating the log: %w", err)
		}
	}
	return w.open()
}

func remove(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("logs: rotating the log: %w", err)
	}
	return nil
}

// Close closes the current file. Writing after Close is an error.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	f := w.f
	w.f = nil
	if err := f.Close(); err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	return nil
}
