package main

// stompwatch reset clears the recorded data and leaves the instrument itself
// alone. It exists because the other way to do it is a hand-written rm, which
// nobody checks, and one slip of which removes the configuration or the
// calibration file.
//
// It is deliberately not on the dashboard. Wiping the record is not something
// to do from a phone.
//
// Every refusal here is the point of the command, not a detail of it:
//
//   - It refuses while the collector is running, because the collector is
//     writing the database and cutting clips as the files go.
//   - It refuses if any event carries a review, because a review is a
//     person's decision and its presence means this is not test data.
//   - It does nothing at all without -yes. The preview is the default, so a
//     mistyped command is harmless.
//   - It refuses a config file it cannot read, and never guesses a path.
//
// It removes the contents of each configured directory and never the
// directory, so the owner, the group, and the mode survive and the service
// user can still write there.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/settings"
	"github.com/minayousseif/stompwatch/internal/store"
)

// resetDir is one directory whose contents reset removes, measured.
type resetDir struct {
	label string // what the owner calls it
	key   string // the config key it came from
	dir   string
	files int
	bytes int64
}

// resetFile is one file reset removes by name, measured. bytes is -1 for a
// file that is not there.
type resetFile struct {
	label string
	path  string
	bytes int64
}

// resetPlan is everything reset would remove, with the size of each part. It
// is measured before anything is removed, because the report the owner reads
// to decide is the same report the command acts on.
type resetPlan struct {
	cfgPath string
	dbPath  string
	files   []resetFile
	dirs    []resetDir
	kept    []resetFile
}

// planReset measures what a reset would remove and what it would keep.
//
// The database, its write-ahead log, and its shared-memory file are named one
// by one. Everything else is a directory to empty.
func planReset(s config.Config, cfgPath string) resetPlan {
	p := resetPlan{cfgPath: cfgPath, dbPath: s.DBPath}
	for _, f := range []resetFile{
		{"the database", s.DBPath, 0},
		{"its -wal file", s.DBPath + "-wal", 0},
		{"its -shm file", s.DBPath + "-shm", 0},
	} {
		f.bytes = -1
		if fi, err := os.Stat(f.path); err == nil {
			f.bytes = fi.Size()
		}
		p.files = append(p.files, f)
	}
	for _, d := range []resetDir{
		{label: "the audio clips", key: "clip_dir", dir: s.ClipDir},
		{label: "the video clips", key: "video_dir", dir: s.VideoDir},
		{label: "the snapshots", key: "db_path", dir: snapshotDir(s.DBPath)},
		{label: "the logs", key: "log_dir", dir: s.LogDir},
	} {
		if d.dir == "" {
			continue
		}
		d.files, d.bytes = measureDir(d.dir)
		p.dirs = append(p.dirs, d)
	}
	p.kept = []resetFile{
		{"the config file", cfgPath, 0},
		{"the calibration file", s.CalibrationFile, 0},
		{"the credentials file", s.CameraCredentialsFile, 0},
		{"the lock file", lockPath(s.DBPath), 0},
	}
	return p
}

// measureDir counts the files under dir and what they take up. A directory
// that is not there is no files and no bytes: reset is run on a box that has
// only just been installed too.
func measureDir(dir string) (files int, bytes int64) {
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}

// --- the command ---

func cmdReset(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("reset", flag.ContinueOnError)
	fl.SetOutput(stderr)
	cfgPath := fl.String("config", defaultConfig, "config file")
	yes := fl.Bool("yes", false, "really remove the data; without this nothing is removed")
	reviewed := fl.Bool("reviewed", false, "remove the data even though an event carries a review")
	if fl.Parse(args) != nil {
		return 2
	}

	// The config file says where the data is. A file that does not parse, or
	// that does not say, leaves nothing safe to do: reset never guesses a
	// path.
	s, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch: %v\n", err)
		return 1
	}
	for _, missing := range []struct {
		empty bool
		key   string
	}{{s.DBPath == "", "db_path"}, {s.ClipDir == "", "clip_dir"}} {
		if missing.empty {
			fmt.Fprintf(stderr, "stompwatch reset: %s is not set in %s, and reset never guesses a path.\n",
				missing.key, *cfgPath)
			return 1
		}
	}

	// Checked before the lock is taken, because the lock file is one of the
	// files a reset leaves behind.
	dir := filepath.Dir(s.DBPath)
	if uid, owner, ok := dirOwner(dir); ok {
		if note := ownerRefusal(os.Geteuid(), uid, owner, dir, *cfgPath, *reviewed); note != "" {
			fmt.Fprint(stderr, note)
			return 1
		}
	}

	// The collector holds this lock for its whole life.
	lock, err := lockData(s.DBPath)
	if err != nil {
		if errors.Is(err, errLocked) {
			fmt.Fprintf(stderr, "stompwatch reset: a stompwatch is running with this data, and the data must not be "+
				"removed while it is.\nIt holds %s.\nStop the service, reset, and start it again:\n\n"+
				"    sudo systemctl stop stompwatch\n    %s\n    sudo systemctl start stompwatch\n",
				lockPath(s.DBPath), resetCommand(resetOwner(s.DBPath), *cfgPath, *reviewed))
			return 1
		}
		fmt.Fprintf(stderr, "stompwatch reset: %v\n", err)
		return 1
	}
	defer lock.release()

	totals, err := readTotals(s.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch reset: %v\n", err)
		return 1
	}
	plan := planReset(s, *cfgPath)
	printHeader(stdout, plan, totals)

	// A review is a person's decision about a night, and removing it loses
	// something nobody can measure again.
	if totals.Reviews > 0 && !*reviewed {
		fmt.Fprintf(stderr, "stompwatch reset: %d of the %d events carry a review, so this is not test data.\n"+
			"A review is a person's decision about a night, and a reset removes it.\n"+
			"Add -reviewed to the command to remove it anyway.\n", totals.Reviews, totals.Events)
		return 1
	}

	// The paths come from a file a person edits. A directory that holds the
	// configuration must never be cleared, whatever the file says.
	if err := plan.checkKept(); err != nil {
		fmt.Fprintf(stderr, "stompwatch reset: %v\n", err)
		return 1
	}

	saved, err := readSettings(s.DBPath)
	if err != nil {
		fmt.Fprintf(stderr, "stompwatch reset: %v\n", err)
		return 1
	}

	printPlan(stdout, plan, *yes)
	printSettings(stdout, saved, *cfgPath)
	if !*yes {
		fmt.Fprintf(stdout, "\nNothing was removed. Run the same command again with -yes to remove it.\n")
		return 0
	}
	if err := plan.remove(); err != nil {
		fmt.Fprintf(stderr, "stompwatch reset: %v\n", err)
		return 1
	}

	// The audit trail is the one thing that should survive a wipe.
	by := resetUser(os.Getenv)
	if err := recordReset(s.DBPath, plan, totals, by, time.Now()); err != nil {
		fmt.Fprintf(stderr, "stompwatch reset: the data is removed, but the record of it is not written: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nRemoved. The new database holds one %s record of this reset, by %s, and nothing else.\n",
		store.HealthDataReset, by)
	return 0
}

// reviewedFlag repeats -reviewed in the command the refusal prints, so the
// owner can run the line as it stands.
func reviewedFlag(reviewed bool) string {
	if reviewed {
		return " -reviewed"
	}
	return ""
}

// resetOwner is the user a refusal tells the owner to run reset as: the one
// that owns the data directory, and nobody in particular when that cannot be
// read or is this user already.
func resetOwner(dbPath string) string {
	uid, _, ok := dirOwner(filepath.Dir(dbPath))
	if !ok || uid == os.Geteuid() {
		return ""
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return ""
	}
	return u.Username
}

// ownerRefusal says why this process must not reset the data, or is empty when
// it may.
//
// The danger is root. reset is run with sudo, and root may remove anything and
// create anything: the new database and the lock file would belong to root, in
// directories the service user owns, and the collector could open neither, so a
// reset would stop the box recording. Any other user either owns the directory
// or cannot write it at all, and the kernel's own refusal says that clearly
// enough.
func ownerRefusal(euid, dirUID int, owner, dir, cfgPath string, reviewed bool) string {
	if euid != 0 || dirUID == 0 {
		return ""
	}
	return fmt.Sprintf("stompwatch reset: this is running as root, and %s belongs to %s.\n"+
		"The database and the lock file left behind would belong to root, and the collector\n"+
		"could not write them. Run it as that user:\n\n    %s\n",
		dir, owner, resetCommand(owner, cfgPath, reviewed))
}

// resetCommand is the command line a refusal tells the owner to run. It repeats
// the flags that were given, so the line can be run as it stands.
func resetCommand(user, cfgPath string, reviewed bool) string {
	sudo := "sudo"
	if user != "" {
		sudo = "sudo -u " + user
	}
	return fmt.Sprintf("%s stompwatch reset -config %s%s -yes", sudo, cfgPath, reviewedFlag(reviewed))
}

// dirOwner is the user that owns dir, as a number and by name. ok is false when
// the directory is not there, or when this system does not report an owner.
func dirOwner(dir string) (uid int, name string, ok bool) {
	fi, err := os.Stat(dir)
	if err != nil {
		return 0, "", false
	}
	st, isUnix := fi.Sys().(*syscall.Stat_t)
	if !isUnix {
		return 0, "", false
	}
	uid = int(st.Uid)
	name = fmt.Sprintf("user %d", uid)
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		name = fmt.Sprintf("%s (user %d)", u.Username, uid)
	}
	return uid, name, true
}

// readTotals counts what the database holds.
//
// A database that is not there holds nothing, and is not created to find that
// out: a preview must change nothing at all. For the same reason the count is
// read without opening the database for writing, because a read-write open
// applies any pending migration and folds the write-ahead log into the
// database as it closes.
//
// A database left behind by a crash cannot always be read that way: its
// write-ahead log has to be recovered first, and only a writer may do that.
// Then the read-write open is the only way to count it at all, and a reset
// must still be possible on a box whose collector crashed.
func readTotals(dbPath string) (store.Totals, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return store.Totals{}, nil
	}
	ctx := context.Background()
	if t, err := store.TotalsOf(ctx, dbPath); err == nil {
		return t, nil
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return store.Totals{}, fmt.Errorf("cannot read the database: %w", err)
	}
	defer st.Close()
	return st.Totals(ctx)
}

// readSettings reads the settings saved from the dashboard, which live in the
// database and go with it. It reads them the way readTotals counts, without
// opening the database for writing. readTotals has run first, so a write-ahead
// log left by a crash is already recovered.
//
// A key that only the config file may set is left out: the collector never
// uses such a row, and a line to paste would put its value into use.
func readSettings(dbPath string) (map[string]string, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil
	}
	saved, err := store.SettingsOf(context.Background(), dbPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read the saved settings: %w", err)
	}
	settings.DropFileOnly(saved)
	return saved, nil
}

// resetUser names whoever ran the command, for the audit row. reset is run
// with sudo, so the process is root and the name worth recording is the one
// sudo kept.
func resetUser(getenv func(string) string) string {
	for _, key := range []string{"SUDO_USER", "USER", "LOGNAME"} {
		if v := getenv(key); v != "" {
			return v
		}
	}
	return "unknown"
}

// --- the report ---

// label is the width of the first column, wide enough for the longest label.
const label = "%-22s"

func printHeader(w io.Writer, p resetPlan, t store.Totals) {
	fmt.Fprintf(w, "stompwatch reset\n")
	fmt.Fprintf(w, "  "+label+" %s\n", "config file", p.cfgPath)
	fmt.Fprintf(w, "  "+label+" %s\n", "database", p.dbPath)
	fmt.Fprintf(w, "\nIn the database:\n")
	for _, c := range []struct {
		name string
		n    int
	}{
		{"events", t.Events},
		{"measured seconds", t.Seconds},
		{"audio clips", t.AudioClips},
		{"video clips", t.VideoClips},
		{"reviews", t.Reviews},
		{"health rows", t.HealthRows},
	} {
		fmt.Fprintf(w, "  "+label+" %d\n", c.name, c.n)
	}
}

func printPlan(w io.Writer, p resetPlan, act bool) {
	heading, keeping := "Would remove:", "Would keep:"
	if act {
		heading, keeping = "Removing:", "Keeping:"
	}
	fmt.Fprintf(w, "\n%s\n", heading)
	for _, f := range p.files {
		size := "not there"
		if f.bytes >= 0 {
			size = fmt.Sprintf("%d bytes", f.bytes)
		}
		fmt.Fprintf(w, "  "+label+" %s (%s)\n", f.label, f.path, size)
	}
	for _, d := range p.dirs {
		fmt.Fprintf(w, "  "+label+" %s (%s, %d bytes)\n", d.label, d.dir, files(d.files), d.bytes)
	}
	fmt.Fprintf(w, "Each directory stays as it is. Only what is inside it is removed, so its owner\n"+
		"and its mode survive and the service user can still write there.\n")
	fmt.Fprintf(w, "\n%s\n", keeping)
	for _, f := range p.kept {
		path := f.path
		if path == "" {
			path = "not configured"
		}
		fmt.Fprintf(w, "  "+label+" %s\n", f.label, path)
	}
	fmt.Fprintf(w, "  everything outside the directories listed above\n")
}

// printSettings lists the settings saved from the dashboard as lines for the
// config file, because a reset removes them with the database. One of them
// can be recording_pause, and without it the box records in hours the owner
// excluded. With none saved it prints nothing.
func printSettings(w io.Writer, saved map[string]string, cfgPath string) {
	if len(saved) == 0 {
		return
	}
	fmt.Fprintf(w, "\nThese settings were saved from the dashboard, and they go with the database,\n"+
		"so to keep them put these lines in %s in place of any line with the same key:\n\n", cfgPath)
	for _, k := range slices.Sorted(maps.Keys(saved)) {
		fmt.Fprintf(w, "    %s = %s\n", k, saved[k])
	}
}

// files says how many files in words, so the report reads as a sentence.
func files(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// --- removing ---

// checkKept refuses the whole reset when a file that must survive it sits
// inside a directory reset would clear. The paths come from a config file a
// person edits, and log_dir = /etc/stompwatch is a typo that would otherwise
// cost the configuration.
func (p resetPlan) checkKept() error {
	for _, keep := range p.kept {
		if keep.path == "" {
			continue
		}
		path, err := filepath.Abs(keep.path)
		if err != nil {
			return fmt.Errorf("%s: %w", keep.path, err)
		}
		for _, d := range p.dirs {
			if _, err := resolveInside(d.dir, path); err == nil {
				return fmt.Errorf("%s is %s, which holds %s %s.\n"+
					"reset clears that directory, and it must never remove that file. Fix the config file.",
					d.key, d.dir, keep.label, path)
			}
		}
	}
	return nil
}

// remove clears everything in the plan, and stops at the first error rather
// than carrying on: a reset that half worked needs to be looked at.
func (p resetPlan) remove() error {
	for _, f := range p.files {
		if err := removeInside(filepath.Dir(p.dbPath), f.path); err != nil {
			return err
		}
	}
	for _, d := range p.dirs {
		if err := clearDir(d.dir); err != nil {
			return err
		}
	}
	return nil
}

// clearDir removes everything inside dir and leaves dir itself, so its owner,
// its group, and its mode survive. A directory that is not there is nothing
// to clear.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", dir, err)
	}
	for _, e := range entries {
		if err := removeInside(dir, filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// removeInside removes path, which must be inside root.
func removeInside(root, path string) error {
	target, err := resolveInside(root, path)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("cannot remove %s: %w", target, err)
	}
	return nil
}

// resolveInside resolves path against root and refuses one that leads outside
// it, or that is root itself.
//
// It is the containment check internal/web/audio.go uses before it serves a
// clip, applied to removal instead. The paths reset works from are read out
// of a config file, and a path from a file must never be able to reach the
// rest of the disk.
func resolveInside(root, path string) (string, error) {
	if root == "" {
		return "", errors.New("no directory to remove from")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(abs, target)
	}
	target = filepath.Clean(target)
	rel, err := filepath.Rel(abs, target)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", fmt.Errorf("%s is the directory itself, not something inside it", abs)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s, so reset will not remove it", target, abs)
	}
	return target, nil
}

// --- the record of the reset ---

// recordReset opens the new database and writes the one row that says the
// data was reset: when, by which user, and what went.
func recordReset(dbPath string, p resetPlan, t store.Totals, by string, at time.Time) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("cannot open a new database: %w", err)
	}
	defer st.Close()
	return st.AddHealth(context.Background(), at, store.HealthDataReset, resetDetail(p, t, by), 0)
}

// resetDetail is the one line the record holds. It says what was removed in
// the same numbers the report printed.
func resetDetail(p resetPlan, t store.Totals, by string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "the recorded data was reset by %s: removed the database, which held %s, %s, %s, %s, %s and %s",
		by, count(t.Events, "event"), count(t.Seconds, "measured second"),
		count(t.AudioClips, "audio clip"), count(t.VideoClips, "video clip"),
		count(t.Reviews, "review"), count(t.HealthRows, "health row"))
	var bytes int64
	dirs := make([]string, 0, len(p.dirs))
	for _, d := range p.dirs {
		bytes += d.bytes
		dirs = append(dirs, d.dir)
	}
	for _, f := range p.files {
		if f.bytes > 0 {
			bytes += f.bytes
		}
	}
	fmt.Fprintf(&b, ", and %d bytes of files from %s", bytes, strings.Join(dirs, ", "))
	return b.String()
}

// count puts a number in front of a word and makes the word plural when it
// has to be.
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
