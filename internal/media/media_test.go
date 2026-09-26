package media

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/store"
)

// root makes a clip directory with a file outside it, and returns both.
func root(t *testing.T) (dir, outside string) {
	t.Helper()
	base := t.TempDir()
	dir = filepath.Join(base, "clips")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(base, "secret.wav")
	if err := os.WriteFile(outside, []byte("not for deletion"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, outside
}

// A stored path is a full path under its root, whether it was stored as
// relative or as absolute. One that climbs out with .. or was never under
// the root is refused with a named error, so a caller can tell a refusal
// from a broken disk.
func TestResolveKeepsAPathUnderItsRoot(t *testing.T) {
	dir, outside := root(t)
	inside := filepath.Join(dir, "2026", "09", "11", "1.wav")
	// Resolve returns the path with its symbolic links resolved, so the
	// answer is built the same way here with the standard library rather
	// than with the function under test. On a Mac the temporary directory
	// is reached through /var, which is a link to /private/var.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realDir, "2026", "09", "11", "1.wav")
	for _, stored := range []string{filepath.Join("2026", "09", "11", "1.wav"), inside} {
		got, err := Resolve(dir, stored)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", stored, got, err, want)
		}
	}
	for _, stored := range []string{
		"..", filepath.Join("..", "secret.wav"), outside,
		filepath.Join("2026", "..", "..", "secret.wav"),
	} {
		got, err := Resolve(dir, stored)
		if !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Resolve(%q) = %q, %v; want ErrOutsideRoot", stored, got, err)
		}
	}
	if _, err := Resolve("", "1.wav"); err == nil || errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Resolve with no root = %v; want an error that is not ErrOutsideRoot", err)
	}
}

// Remove deletes the one file and says how big it was, so the purge
// record can carry the bytes reclaimed.
func TestRemoveDeletesTheFileAndReportsItsSize(t *testing.T) {
	dir, _ := root(t)
	path := filepath.Join(dir, "7.wav")
	if err := os.WriteFile(path, []byte("nineteen bytes long"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Remove(dir, "7.wav")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !got.Existed || got.Bytes != 19 {
		t.Errorf("Remove = %+v, want Existed true and 19 bytes", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the file is still on disk: %v", err)
	}
}

// A file that is already gone is not an error: what is recorded is the
// decision, not the free space. It reports no bytes, so the record does
// not claim to have reclaimed anything.
func TestRemoveOfAMissingFileIsNotAnError(t *testing.T) {
	dir, _ := root(t)
	got, err := Remove(dir, "gone.wav")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got.Existed || got.Bytes != 0 {
		t.Errorf("Remove = %+v, want Existed false and 0 bytes", got)
	}
}

// A path outside the root is refused, never deleted. This is the check
// that stands between a path bug and the owner's evidence.
func TestRemoveRefusesAPathOutsideTheRoot(t *testing.T) {
	dir, outside := root(t)
	for _, stored := range []string{filepath.Join("..", "secret.wav"), outside} {
		got, err := Remove(dir, stored)
		if !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Remove(%q) = %+v, %v; want ErrOutsideRoot", stored, got, err)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("Remove(%q) deleted the file outside the root: %v", stored, err)
		}
	}
}

// The health detail is one sentence: who, how many files, which events,
// how many bytes. Past twenty events it counts the rest.
func TestPurgeDetailIsOneSentence(t *testing.T) {
	at := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	one := []store.Purge{{EventID: 5, Kind: "audio", Bytes: 1000, At: at, By: "alex"}}
	if got, want := PurgeDetail("alex", one, 1000),
		"alex deleted 1 recording from event 5, reclaiming 1000 bytes"; got != want {
		t.Errorf("PurgeDetail = %q, want %q", got, want)
	}

	var many []store.Purge
	for id := int64(1); id <= 25; id++ {
		many = append(many,
			store.Purge{EventID: id, Kind: "audio", Bytes: 10, At: at, By: "retention"},
			store.Purge{EventID: id, Kind: "video", Bytes: 20, At: at, By: "retention"})
	}
	got := PurgeDetail("retention at 90 days", many, 750)
	want := "retention at 90 days deleted 50 recordings from events " +
		"1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, and 5 more, " +
		"reclaiming 750 bytes"
	if got != want {
		t.Errorf("PurgeDetail = %q, want %q", got, want)
	}
	if strings.Contains(got, "21") {
		t.Errorf("the detail names event 21, past the twenty it should stop at")
	}
}

// A directory symbolic link inside the root is still a way out of it.
// filepath.Rel compares text, so a path through a link reads as inside the
// root while the file it names is anywhere on the disk.
func TestResolveRefusesAPathThroughASymlinkOutOfTheRoot(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "clips")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(outside, "evidence.wav")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "away")); err != nil {
		t.Skipf("this filesystem will not make a symlink: %v", err)
	}

	if _, err := Resolve(root, filepath.Join(root, "away", "evidence.wav")); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Resolve through a symlink = %v, want ErrOutsideRoot", err)
	}
	got, err := Remove(root, filepath.Join(root, "away", "evidence.wav"))
	if !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Remove through a symlink = %v, %v, want ErrOutsideRoot", got, err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the file outside the root was deleted: %v", err)
	}
}

// A root reached through a symbolic link is the normal shape of /data on a
// box where the disk is mounted elsewhere. A file really inside it must
// still resolve, or retention would refuse every clip on such a box.
func TestResolveAcceptsARootThatIsItselfASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(real, "2026", "09", "13"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "clips")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this filesystem will not make a symlink: %v", err)
	}
	clip := filepath.Join(real, "2026", "09", "13", "7.wav")
	if err := os.WriteFile(clip, []byte("a clip"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The collector stores the path it was given, which goes through the
	// link, and a restored database may hold the real path instead.
	for _, stored := range []string{filepath.Join(link, "2026/09/13/7.wav"), clip} {
		if _, err := Resolve(link, stored); err != nil {
			t.Errorf("Resolve(%q) = %v, want it accepted", stored, err)
		}
	}
}

// An empty path resolves to the root itself. Deleting the root would take
// the directory every clip lives in.
func TestRemoveRefusesTheRootItself(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "clips")
	if err := os.MkdirAll(filepath.Join(root, "2026"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{"", ".", root, root + "/"} {
		if _, err := Remove(root, stored); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Remove(root, %q) = %v, want ErrOutsideRoot", stored, err)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the root was deleted: %v", err)
	}
}

// A media row that names a directory must not take the directory. A day
// directory holds every clip cut that night.
func TestRemoveRefusesADirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "clips")
	day := filepath.Join(root, "2026", "09", "13")
	if err := os.MkdirAll(day, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(root, day); err == nil {
		t.Error("Remove of a directory returned no error")
	}
	if _, err := os.Stat(day); err != nil {
		t.Errorf("the day directory was deleted: %v", err)
	}
}

// A sibling directory whose name starts with the root's name is not
// inside the root. A check on the text alone would let video reach audio
// clips on a box where the two roots are named that way.
func TestResolveRefusesASiblingWithTheSameNameStart(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "audio")
	sibling := filepath.Join(dir, "audio-old")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(sibling, "1.wav")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Remove(root, victim); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Remove from the sibling directory = %v, want ErrOutsideRoot", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the file in the sibling directory was deleted: %v", err)
	}
}
