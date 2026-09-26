package meter

import (
	"math"
	"strings"
	"testing"
)

func TestParseREWReadsAllSeparatorsAndComments(t *testing.T) {
	in := `* Calibration file
# second comment

20 -1.5 0.0
800	0.5
1000,1.0,12.5
1250 , 1.5
2000 9.0 -3
`
	got, err := ParseREW(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseREW: %v", err)
	}
	want := []CalPoint{
		{FreqHz: 20, MagDB: -1.5, PhaseDeg: 0},
		{FreqHz: 800, MagDB: 0.5},
		{FreqHz: 1000, MagDB: 1.0, PhaseDeg: 12.5},
		{FreqHz: 1250, MagDB: 1.5},
		{FreqHz: 2000, MagDB: 9.0, PhaseDeg: -3},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d points, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The Eversolo EM-01 file starts with two header lines. Header lines before
// the first data row are skipped.
func TestParseREWSkipsHeaderLinesBeforeTheData(t *testing.T) {
	in := "X    Y\nHz    dB\n20.7725947521866 -0.8883241865250\n1000 0.5\n"
	got, err := ParseREW(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseREW: %v", err)
	}
	want := []CalPoint{{FreqHz: 20.7725947521866, MagDB: -0.888324186525}, {FreqHz: 1000, MagDB: 0.5}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("points = %+v, want %+v", got, want)
	}
}

func TestParseREWNeedsDataRows(t *testing.T) {
	if _, err := ParseREW(strings.NewReader("X    Y\nHz    dB\n")); err == nil {
		t.Fatal("a file with only header lines returned no error")
	}
}

// An unknown line may mean the file is in a format we do not understand.
// Parsing must stop with the line number, not skip the line.
func TestParseREWRejectsUnknownLineWithLineNumber(t *testing.T) {
	for name, in := range map[string]string{
		"text":       "* c\n100 1\nSens Factor =-1.2dB\n",
		"one column": "* c\n100 1\n200\n",
	} {
		_, err := ParseREW(strings.NewReader(in))
		if err == nil || !strings.Contains(err.Error(), "line 3") {
			t.Errorf("%s: error = %v, want an error that names line 3", name, err)
		}
	}
}

// The file gives the mic's deviation. The correction removes it: a mic that
// reads +1.0 dB around 1 kHz is scaled by 10^(-1/20) = 0.891251.
func TestScalarCalibrationRemovesMeanDeviation800To1250Hz(t *testing.T) {
	points := []CalPoint{
		{FreqHz: 700, MagDB: -20},
		{FreqHz: 800, MagDB: 0.5},
		{FreqHz: 1000, MagDB: 1.0},
		{FreqHz: 1250, MagDB: 1.5},
		{FreqHz: 1300, MagDB: 20},
	}
	cal, err := NewScalarCalibration(points)
	if err != nil {
		t.Fatalf("NewScalarCalibration: %v", err)
	}
	if got := cal.Correct(1.0); math.Abs(got-0.891251) > 1e-6 {
		t.Fatalf("Correct(1.0) = %.6f, want 0.891251", got)
	}
	if got := cal.Correct(-2.0); math.Abs(got+1.782502) > 1e-6 {
		t.Fatalf("Correct(-2.0) = %.6f, want -1.782502", got)
	}
}

func TestScalarCalibrationNeedsPointsIn800To1250Hz(t *testing.T) {
	_, err := NewScalarCalibration([]CalPoint{{FreqHz: 100, MagDB: 1}, {FreqHz: 5000, MagDB: 1}})
	if err == nil {
		t.Fatal("NewScalarCalibration with no points in 800-1250 Hz returned no error")
	}
}
