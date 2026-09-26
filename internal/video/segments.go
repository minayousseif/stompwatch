package video

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SegmentPattern is the strftime pattern ffmpeg names segments with. The
// child runs with TZ=UTC, so the name is the segment's UTC start to the
// second, and the ring can be read back from the names alone.
const SegmentPattern = "%Y%m%dT%H%M%SZ.ts"

const segmentLayout = "20060102T150405Z.ts"

// SegmentName is the name a segment starting at t gets.
func SegmentName(t time.Time) string { return t.UTC().Format(segmentLayout) }

// ParseSegmentName reads the start out of a segment name. It is false for
// anything that is not a finished segment name.
func ParseSegmentName(name string) (time.Time, bool) {
	if len(name) != len(segmentLayout) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(segmentLayout, name, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Segment is one file of the ring.
type Segment struct {
	Path  string
	Start time.Time
	Size  int64
}

// ListSegments returns the segments in dir, oldest first. A directory that
// does not exist yet has no segments and is not an error.
func ListSegments(dir string) ([]Segment, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("video: reading the ring: %w", err)
	}
	var out []Segment
	for _, e := range entries {
		start, ok := ParseSegmentName(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed between the listing and now
		}
		out = append(out, Segment{Path: filepath.Join(dir, e.Name()), Start: start, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// holeFactor says how much longer than nominal a segment may run before
// the space after it counts as a hole. A cut lands on a keyframe, so a
// segment runs a little long; a disconnect leaves far more than that.
const holeFactor = 2

// end is when segment i stops holding video: the start of the next one, or
// the nominal length after its own start when there is no next one or the
// next one is on the far side of a hole.
func end(segs []Segment, i int, nominal time.Duration) time.Time {
	if i+1 < len(segs) {
		if next := segs[i+1].Start; next.Sub(segs[i].Start) <= holeFactor*nominal {
			return next
		}
	}
	return segs[i].Start.Add(nominal)
}

// Select returns the segments that hold any of the window from from to to,
// and whether they cover all of it. The clip is padded outward: the segment
// that holds from is in whole, and so is the one that holds to (SPEC.md section 7).
// segs must be sorted by start, as ListSegments returns them.
func Select(segs []Segment, from, to time.Time, nominal time.Duration) (sel []Segment, covered bool) {
	first := -1
	for i, s := range segs {
		if s.Start.Before(to) && end(segs, i, nominal).After(from) {
			if first < 0 {
				first = i
			}
			sel = append(sel, s)
		}
	}
	if len(sel) == 0 {
		return nil, false
	}
	last := first + len(sel) - 1
	if sel[0].Start.After(from) || end(segs, last, nominal).Before(to) {
		return sel, false
	}
	for i := first; i < last; i++ {
		if !end(segs, i, nominal).Equal(segs[i+1].Start) {
			return sel, false
		}
	}
	return sel, true
}

// Prune removes the segments that start more than keep before the newest
// one, oldest first, and returns how many went. The newest segment always
// stays, so a ring that was down for a day still holds its last minutes.
func Prune(dir string, keep time.Duration) (int, error) {
	segs, err := ListSegments(dir)
	if err != nil || len(segs) == 0 {
		return 0, err
	}
	cutoff := segs[len(segs)-1].Start.Add(-keep)
	removed := 0
	var problems []string
	for _, s := range segs[:len(segs)-1] {
		if !s.Start.Before(cutoff) {
			break
		}
		if err := os.Remove(s.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			problems = append(problems, err.Error())
			continue
		}
		removed++
	}
	if len(problems) > 0 {
		return removed, fmt.Errorf("video: pruning the ring: %s", strings.Join(problems, "; "))
	}
	return removed, nil
}
