package video

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var s0 = time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)

// writeSegments puts empty segment files in dir, one every step from s0.
func writeSegments(t *testing.T, dir string, step time.Duration, n int) []string {
	t.Helper()
	var names []string
	for i := range n {
		name := SegmentName(s0.Add(time.Duration(i) * step))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", i+1)), 0o640); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func TestSegmentNamesCarryTheirUTCStart(t *testing.T) {
	if got := SegmentName(s0); got != "20260912T030000Z.ts" {
		t.Fatalf("SegmentName = %q", got)
	}
	// Local time must not leak into a name: the same instant in another
	// zone gives the same name.
	local := s0.In(time.FixedZone("EDT", -4*3600))
	if got := SegmentName(local); got != "20260912T030000Z.ts" {
		t.Fatalf("SegmentName in EDT = %q", got)
	}
	start, ok := ParseSegmentName("20260912T030010Z.ts")
	if !ok || !start.Equal(s0.Add(10*time.Second)) {
		t.Fatalf("ParseSegmentName = %v, %v", start, ok)
	}
	for _, bad := range []string{"20260912T030010Z.mp4", "notes.txt", ".20260912T030010Z.ts.tmp", "20260912T030010.ts"} {
		if _, ok := ParseSegmentName(bad); ok {
			t.Errorf("ParseSegmentName(%q) accepted it", bad)
		}
	}
	// The ffmpeg pattern makes the names ParseSegmentName reads.
	if SegmentPattern != "%Y%m%dT%H%M%SZ.ts" {
		t.Errorf("SegmentPattern = %q", SegmentPattern)
	}
}

func TestListSegmentsIsSortedAndSkipsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	names := writeSegments(t, dir, 10*time.Second, 3)
	for _, other := range []string{"README", ".20260912T030030Z.ts.tmp", "20260912T030030Z.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, other), nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	segs, err := ListSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 {
		t.Fatalf("got %d segments: %+v", len(segs), segs)
	}
	for i, s := range segs {
		if !s.Start.Equal(s0.Add(time.Duration(i) * 10 * time.Second)) {
			t.Errorf("segment %d starts %v", i, s.Start)
		}
		if s.Size != int64(i+1) {
			t.Errorf("segment %d size %d", i, s.Size)
		}
		if s.Path != filepath.Join(dir, names[i]) {
			t.Errorf("segment %d path %q", i, s.Path)
		}
	}
	if segs, err := ListSegments(filepath.Join(dir, "missing")); err != nil || len(segs) != 0 {
		t.Errorf("a missing directory gave %v, %v; want no segments and no error", segs, err)
	}
}

// SPEC.md section 7: clip boundaries land on segment edges, and the clip is padded
// outward. The segment holding the start of the pre-roll is in, and so is
// the one holding the end of the post-roll, whatever their edges are.
func TestSelectPadsOutwardToSegmentEdges(t *testing.T) {
	var segs []Segment
	for i := range 12 {
		segs = append(segs, Segment{Path: "s" + string(rune('a'+i)), Start: s0.Add(time.Duration(i) * 10 * time.Second)})
	}
	nominal := 10 * time.Second
	tests := []struct {
		name     string
		from, to time.Time
		want     string
		covered  bool
	}{
		{"inside edges", s0.Add(15 * time.Second), s0.Add(47 * time.Second), "sb,sc,sd,se", true},
		{"on edges", s0.Add(20 * time.Second), s0.Add(40 * time.Second), "sc,sd", true},
		{"end on an edge keeps the earlier segment only", s0.Add(25 * time.Second), s0.Add(40 * time.Second), "sc,sd", true},
		{"one second window", s0.Add(31 * time.Second), s0.Add(32 * time.Second), "sd", true},
		{"before the ring", s0.Add(-30 * time.Second), s0.Add(5 * time.Second), "sa", false},
		{"past the newest, which is still open", s0.Add(105 * time.Second), s0.Add(125 * time.Second), "sk,sl", false},
		{"nothing", s0.Add(200 * time.Second), s0.Add(230 * time.Second), "", false},
	}
	for _, tc := range tests {
		sel, covered := Select(segs, tc.from, tc.to, nominal)
		var got []string
		for _, s := range sel {
			got = append(got, s.Path)
		}
		if strings.Join(got, ",") != tc.want || covered != tc.covered {
			t.Errorf("%s: Select = %v, covered %v; want %s, %v", tc.name, got, covered, tc.want, tc.covered)
		}
	}
}

// A hole in the ring, from a disconnect, means the selected segments do not
// cover the window even though there is one on each side of the hole.
func TestSelectSeesAHoleInTheRing(t *testing.T) {
	nominal := 10 * time.Second
	segs := []Segment{
		{Path: "a", Start: s0},
		{Path: "b", Start: s0.Add(10 * time.Second)},
		// 40 seconds missing here
		{Path: "c", Start: s0.Add(60 * time.Second)},
		{Path: "d", Start: s0.Add(70 * time.Second)},
	}
	sel, covered := Select(segs, s0.Add(5*time.Second), s0.Add(75*time.Second), nominal)
	if len(sel) != 4 || covered {
		t.Fatalf("Select = %d segments, covered %v; want 4 and false", len(sel), covered)
	}
	// A segment a little longer than nominal is a keyframe cut, not a hole.
	segs[2].Start = s0.Add(24 * time.Second)
	segs[3].Start = s0.Add(34 * time.Second)
	if _, covered := Select(segs, s0.Add(5*time.Second), s0.Add(40*time.Second), nominal); !covered {
		t.Error("a 14 s segment was taken for a hole")
	}
}

// SPEC.md section 7: the ring is pruned oldest-first to video_ring_minutes.
func TestPruneDropsTheOldestSegmentsFirst(t *testing.T) {
	dir := t.TempDir()
	names := writeSegments(t, dir, time.Minute, 25) // 25 minutes of one-minute segments
	removed, err := Prune(dir, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 14 {
		t.Errorf("removed %d segments, want 14", removed)
	}
	segs, err := ListSegments(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, s := range segs {
		left = append(left, filepath.Base(s.Path))
	}
	// The newest is minute 24, so minutes 14 to 24 stay: 10 minutes back
	// from the newest, inclusive.
	if !slices.Equal(left, names[14:]) {
		t.Fatalf("left %v\nwant %v", left, names[14:])
	}
	// Pruning again removes nothing.
	if removed, err := Prune(dir, 10*time.Minute); err != nil || removed != 0 {
		t.Errorf("second prune removed %d, %v", removed, err)
	}
	// A ring with one segment keeps it, however old.
	one := t.TempDir()
	writeSegments(t, one, time.Minute, 1)
	if removed, err := Prune(one, time.Minute); err != nil || removed != 0 {
		t.Errorf("prune of one segment removed %d, %v", removed, err)
	}
}
