package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// Level of a line that is not valid JSON. It is never dropped.
const LevelRaw = "RAW"

// blockSize is how much of a file is read at a time, from the end backwards.
const blockSize = 64 << 10

// defaultBudget is the scan's byte budget when the filter names none.
const defaultBudget = 16 << 20

// Line is one parsed log record.
type Line struct {
	TSMS  int64
	Level string // DEBUG, INFO, WARN, ERROR, or RAW
	Msg   string
	Attrs map[string]any
}

// Filter selects lines for the dashboard.
type Filter struct {
	FromMS, ToMS int64    // 0 means unbounded
	Levels       []string // empty means every level
	Query        string   // case-insensitive substring of the whole line
	Limit        int      // at least 1
	Offset       int      // skip this many matching lines; below 1 means none
	MaxBytes     int64    // stop after reading this many bytes; 0 means 16 MiB
}

// Read returns the newest matching lines first, and whether the scan hit its
// byte budget before it reached the start of the range.
//
// It reads the current file, then <name>.1, and so on, and stops at the first
// number that is missing. Within a file it reads blocks from the end, so the
// newest lines of a large log cost a few blocks and not the whole file.
//
// Offset passes over that many of the newest matching lines before the page
// starts, so the view can page back without narrowing its range. It counts
// matching lines, not lines read.
//
// A Limit below 1 counts as 1, and an Offset below 1 is no offset at all. A
// rotation part way through a scan can repeat or miss a line: the log view is
// a view, and the evidence is the database.
func Read(dir, name string, f Filter) ([]Line, bool, error) {
	s := &scan{
		f:      f,
		query:  strings.ToLower(f.Query),
		levels: make(map[string]bool, len(f.Levels)),
		budget: f.MaxBytes,
		limit:  max(f.Limit, 1),
		skip:   max(f.Offset, 0),
	}
	if s.budget <= 0 {
		s.budget = defaultBudget
	}
	for _, l := range f.Levels {
		s.levels[strings.ToUpper(strings.TrimSpace(l))] = true
	}

	for n := 0; ; n++ {
		fh, err := os.Open(path(dir, name, n))
		if errors.Is(err, fs.ErrNotExist) {
			// There is no older file, so the scan saw the whole log.
			return s.out, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("logs: %w", err)
		}
		err = s.file(fh)
		fh.Close()
		if err != nil {
			return nil, false, err
		}
		if s.done {
			return s.out, false, nil
		}
		if s.budget <= 0 {
			return s.out, true, nil
		}
	}
}

// scan is one Read in progress.
type scan struct {
	f      Filter
	query  string
	levels map[string]bool
	limit  int
	budget int64 // bytes left to read
	skip   int   // matches still to be passed over before the page starts
	out    []Line
	done   bool // the limit is full, or the scan passed the start of the range
}

// file walks one log file backwards, newest line first.
func (s *scan) file(fh *os.File) error {
	fi, err := fh.Stat()
	if err != nil {
		return fmt.Errorf("logs: %w", err)
	}

	// buf holds the bytes of the file that come after pos and have not been
	// given back yet. Every line in it ends with a newline except the last,
	// which ends at the end of buf. A file that ends with a newline gives an
	// empty last line, which line ignores.
	pos := fi.Size()
	var buf []byte
	for {
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			s.line(buf[i+1:])
			buf = buf[:i]
			if s.done {
				return nil
			}
			continue
		}
		if pos == 0 {
			s.line(buf) // the first line of the file
			return nil
		}
		if s.budget <= 0 {
			return nil
		}
		n := min(int64(blockSize), pos)
		grown := make([]byte, n+int64(len(buf)))
		if _, err := fh.ReadAt(grown[:n], pos-n); err != nil {
			return fmt.Errorf("logs: %w", err)
		}
		copy(grown[n:], buf)
		buf, pos = grown, pos-n
		s.budget -= n
	}
}

// line parses one raw line and keeps it if it matches the filter.
func (s *scan) line(raw []byte) {
	if len(raw) == 0 {
		return
	}
	l := parse(raw)
	// The files are written in time order, so a line older than the start of
	// the range means every line left is older still.
	if s.f.FromMS != 0 && l.TSMS != 0 && l.TSMS < s.f.FromMS {
		s.done = true
		return
	}
	if !s.keep(raw, l) {
		return
	}
	// The first Offset matches belong to the pages already read.
	if s.skip > 0 {
		s.skip--
		return
	}
	s.out = append(s.out, l)
	if len(s.out) >= s.limit {
		s.done = true
	}
}

// keep reports whether the filter selects this line. A line with no readable
// time passes the time range: dropping it would hide a failure, which is the
// line most worth seeing.
func (s *scan) keep(raw []byte, l Line) bool {
	if l.TSMS != 0 {
		if s.f.ToMS != 0 && l.TSMS > s.f.ToMS {
			return false
		}
	}
	if len(s.levels) > 0 && !s.levels[strings.ToUpper(l.Level)] {
		return false
	}
	if s.query != "" && !strings.Contains(strings.ToLower(string(raw)), s.query) {
		return false
	}
	return true
}

// parse reads one slog JSON record. A line that is not a JSON object comes
// back whole, as RAW.
func parse(raw []byte) Line {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return Line{Level: LevelRaw, Msg: string(raw)}
	}
	var l Line
	for k, v := range m {
		switch k {
		case "time":
			// slog writes RFC 3339 with nanoseconds. A time this cannot read
			// leaves TSMS at 0 and the line is still returned.
			if s, ok := v.(string); ok {
				if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
					l.TSMS = t.UnixMilli()
				}
			}
		case "level":
			l.Level, _ = v.(string)
		case "msg":
			l.Msg, _ = v.(string)
		default:
			if l.Attrs == nil {
				l.Attrs = make(map[string]any, len(m))
			}
			l.Attrs[k] = v
		}
	}
	return l
}
