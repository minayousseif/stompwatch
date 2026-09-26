package media

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/minayousseif/stompwatch/internal/store"
)

// ErrOutsideRoot is the error for a stored path that leads outside the
// directory its kind of media lives in. A caller that sees it refuses the
// file. It never names the path: the owner reads errors on a phone, and a
// path is more than a message needs to say.
var ErrOutsideRoot = errors.New("media: the stored path leads outside its directory")

// Resolve turns a stored media path into a full path under root and
// refuses one that leads outside it. Only the collector writes these rows,
// so this is a second line of defense: a database restored from elsewhere,
// or a future import path, must not be able to read or delete the rest of
// the disk (SPEC.md section 9, media serving rules). Audio is resolved
// against clip_dir and video against video_dir, so neither can reach the
// other's files either.
func Resolve(rootDir, stored string) (string, error) {
	if rootDir == "" {
		return "", errors.New("media: no directory is configured for this kind of media")
	}
	root, err := filepath.Abs(rootDir)
	if err != nil {
		return "", err
	}
	path := stored
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	// Both sides are resolved through their symbolic links before they are
	// compared. filepath.Rel compares text, and text says a path through a
	// link inside the root is inside the root, whatever the link points at.
	// Resolving both is also what lets a box keep its clips on a mounted
	// disk, where the root itself is a link and the stored path may be
	// written either way round.
	root, path = followLinks(root), followLinks(filepath.Clean(path))
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	// "." is the root itself, which an empty stored path resolves to. The
	// root is the directory every clip of its kind lives in.
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return path, nil
}

// followLinks resolves the symbolic links in path. A path that is not on
// disk has no links to resolve, so the deepest part of it that does exist
// is resolved and the rest is kept as it was written. That matters because
// a clip that is already gone must still resolve: the caller records the
// purge either way.
func followLinks(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	parent, base := filepath.Dir(path), filepath.Base(path)
	if parent == path || base == "." || base == string(filepath.Separator) {
		return path
	}
	return filepath.Join(followLinks(parent), base)
}

// Removal says what Remove found.
type Removal struct {
	// Existed is true when the file was on disk and is now deleted, and
	// false when it was already gone. Either way the caller records the
	// purge: what is recorded is the decision, not the free space.
	Existed bool
	// Bytes is the size the file had, or 0 when it was already gone.
	Bytes int64
}

// Remove deletes one clip file under root and says what it found. A
// stored path outside root is refused with ErrOutsideRoot and nothing is
// deleted. A file that is already gone is not an error. Any other error is
// the operating system's, and then the file may still be there.
func Remove(rootDir, stored string) (Removal, error) {
	path, err := Resolve(rootDir, stored)
	if err != nil {
		return Removal{}, err
	}
	var out Removal
	if fi, err := os.Stat(path); err == nil {
		// A media row names one file. A row that names a directory would
		// take a whole night of clips, because an empty day directory
		// deletes without complaint.
		if fi.IsDir() {
			return Removal{}, fmt.Errorf("media: the stored path is a directory, not a clip file")
		}
		out.Bytes = fi.Size()
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Removal{}, nil
		}
		return Removal{}, err
	}
	out.Existed = true
	return out, nil
}

// namedEventsInDetail is how many event numbers the health record spells
// out before it counts the rest. A detail of 500 numbers is not a sentence
// anybody reads.
const namedEventsInDetail = 20

// PurgeDetail is the sentence the health log carries for a purge. Rule 3
// of SPEC.md section 3 says every deletion is logged, so it names who
// asked, how many files went, which events they belonged to, and how many
// bytes came back. The record is in event order, as the callers build it.
func PurgeDetail(by string, record []store.Purge, bytes int64) string {
	var events []int64
	for _, p := range record {
		if len(events) == 0 || events[len(events)-1] != p.EventID {
			events = append(events, p.EventID)
		}
	}
	names := make([]string, 0, namedEventsInDetail)
	for _, id := range events {
		if len(names) == namedEventsInDetail {
			names = append(names, fmt.Sprintf("and %d more", len(events)-namedEventsInDetail))
			break
		}
		names = append(names, strconv.FormatInt(id, 10))
	}
	files := "recordings"
	if len(record) == 1 {
		files = "recording"
	}
	from := "events"
	if len(events) == 1 {
		from = "event"
	}
	return fmt.Sprintf("%s deleted %d %s from %s %s, reclaiming %d bytes",
		by, len(record), files, from, strings.Join(names, ", "), bytes)
}
