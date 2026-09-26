package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
	"github.com/minayousseif/stompwatch/internal/meter"
)

// The colors are written out here as numbers rather than read from the
// package, so the test says what the image must look like instead of
// agreeing with whatever it does look like.
var (
	quietRGB  = color.RGBA{221, 227, 238, 255}
	dayRGB    = color.RGBA{255, 255, 255, 255}
	traceRGB  = color.RGBA{26, 78, 138, 255}
	markerRGB = color.RGBA{198, 40, 40, 255}
	inkRGB    = color.RGBA{34, 34, 34, 255}
)

func pngURL(from, to time.Duration, extra string) string {
	return "/api/export/timeline.png?from=" + strconv.FormatInt(ms(from), 10) +
		"&to=" + strconv.FormatInt(ms(to), 10) + extra
}

// decodePNG reads the body with the standard library's decoder.
func decodePNG(t *testing.T, w *httptest.ResponseRecorder) image.Image {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("the body is not a PNG: %v", err)
	}
	return img
}

func sameColor(img image.Image, x, y int, c color.RGBA) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return uint8(r>>8) == c.R && uint8(g>>8) == c.G && uint8(b>>8) == c.B
}

// columnHas reports whether any pixel of a column has a color.
func columnHas(img image.Image, x int, c color.RGBA) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if sameColor(img, x, y, c) {
			return true
		}
	}
	return false
}

// countColor counts the pixels of one color in the whole image.
func countColor(img image.Image, c color.RGBA) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if sameColor(img, x, y, c) {
				n++
			}
		}
	}
	return n
}

// minuteBins fills a range with one measured second a minute.
func minuteBins(e *env, minutes int, level func(i int) float64) {
	e.t.Helper()
	bins := make([]meter.Bin, minutes)
	for i := range bins {
		bins[i] = bin(i*60, level(i), level(i)+5, 30)
	}
	e.addBins(bins...)
}

func TestTimelinePNGHasTheSizeAskedFor(t *testing.T) {
	e := newEnv(t)
	img := decodePNG(t, e.get(pngURL(0, 12*time.Hour, "")))
	if got := img.Bounds().Size(); got.X != 1200 || got.Y != 400 {
		t.Errorf("size = %v, want the default 1200 by 400", got)
	}
	img = decodePNG(t, e.get(pngURL(0, 12*time.Hour, "&width=800&height=300")))
	if got := img.Bounds().Size(); got.X != 800 || got.Y != 300 {
		t.Errorf("size = %v, want 800 by 300", got)
	}
}

func TestTimelinePNGRejectsASizeOutOfRange(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.get(pngURL(0, time.Hour, "&width=479")), 400, "width")
	wantError(t, e.get(pngURL(0, time.Hour, "&width=2401")), 400, "width")
	wantError(t, e.get(pngURL(0, time.Hour, "&height=239")), 400, "height")
	wantError(t, e.get(pngURL(0, time.Hour, "&height=1201")), 400, "height")
	wantError(t, e.get(pngURL(0, time.Hour, "&status=maybe")), 400, "status")
	wantError(t, e.get(pngURL(0, 32*24*time.Hour, "")), 400, "31 days")
	wantError(t, e.get("/api/export/timeline.png"), 400, "from")
}

// The owner has to be able to see at a glance whether a loud moment was at a
// time that matters, so the night is shaded behind everything else.
func TestTimelinePNGShadesQuietHoursBehindTheTrace(t *testing.T) {
	e := newEnv(t)
	// Midnight to noon. The default quiet hours end at 07:00, so the first
	// seven hours of the twelve are quiet: seven twelfths of the way across.
	img := decodePNG(t, e.get(pngURL(-3*time.Hour, 9*time.Hour, "")))
	w := img.Bounds().Dx()

	if !columnHas(img, w/5, quietRGB) {
		t.Errorf("the column a fifth of the way across has no quiet shading")
	}
	if columnHas(img, w*85/100, quietRGB) {
		t.Errorf("the column 85%% of the way across is shaded, but it is the morning")
	}
	// The last shaded column marks the end of the night.
	last := -1
	for x := 0; x < w; x++ {
		if columnHas(img, x, quietRGB) {
			last = x
		}
	}
	if last < 0 {
		t.Fatalf("nothing on the image is shaded as a quiet hour")
	}
	if f := float64(last) / float64(w); f < 0.55 || f > 0.64 {
		t.Errorf("the shading ends %.0f%% of the way across, want about 60%%", f*100)
	}
	// The day is a different color from the night, not a shade of it.
	if !columnHas(img, w*85/100, dayRGB) {
		t.Errorf("the morning part of the plot is not the day color")
	}
}

// Louder must draw higher. A chart that put the quiet half above the loud
// half would be worse than no chart.
func TestTimelinePNGDrawsLouderHigher(t *testing.T) {
	e := newEnv(t)
	// Twelve hours, one measured second a minute: 40 dB, then 60 dB.
	minuteBins(e, 720, func(i int) float64 {
		if i < 360 {
			return 40
		}
		return 60
	})
	img := decodePNG(t, e.get(pngURL(0, 12*time.Hour, "")))
	w := img.Bounds().Dx()

	quietY, ok := traceY(img, w/4)
	if !ok {
		t.Fatalf("there is no trace a quarter of the way across")
	}
	loudY, ok := traceY(img, w*3/4)
	if !ok {
		t.Fatalf("there is no trace three quarters of the way across")
	}
	if loudY >= quietY {
		t.Errorf("the 60 dB half is drawn at row %d and the 40 dB half at row %d; "+
			"louder must be higher up the image", loudY, quietY)
	}
}

// traceY returns the middle row of the trace in a column.
func traceY(img image.Image, x int) (int, bool) {
	b := img.Bounds()
	first, last := -1, -1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if sameColor(img, x, y, traceRGB) {
			if first < 0 {
				first = y
			}
			last = y
		}
	}
	if first < 0 {
		return 0, false
	}
	return (first + last) / 2, true
}

// A range with no measurement in it must say so, not draw a flat line at
// zero across a night when the microphone was unplugged.
func TestTimelinePNGSaysWhenThereIsNothingToShow(t *testing.T) {
	e := newEnv(t)
	img := decodePNG(t, e.get(pngURL(0, 12*time.Hour, "")))
	if got := img.Bounds().Size(); got.X != 1200 || got.Y != 400 {
		t.Errorf("size = %v, want 1200 by 400", got)
	}
	if n := countColor(img, traceRGB); n != 0 {
		t.Errorf("%d pixels of trace are drawn over a range with no measurement", n)
	}
	if n := countColor(img, inkRGB); n < 200 {
		t.Errorf("the image carries %d pixels of text, too few to say anything", n)
	}
}

// The markers are the point of the picture: this is when it happened. Three
// events at the start, the middle and the end of the range must come out
// evenly spaced, wherever the plot's own edges fall.
func TestTimelinePNGMarksMatchingEvents(t *testing.T) {
	e := newEnv(t)
	for _, at := range []time.Duration{0, 6 * time.Hour, 12 * time.Hour} {
		id := e.addEvent(at, time.Minute, 70, detect.Running)
		e.addReview(id, "verified", "", "me@example.com")
	}

	img := decodePNG(t, e.get(pngURL(0, 12*time.Hour, "")))
	w := img.Bounds().Dx()
	// Each tick is a few pixels wide, so gather the runs and take the middle
	// of each one.
	var marks []int
	run := -1
	for x := 0; x <= w; x++ {
		hit := x < w && columnHas(img, x, markerRGB)
		switch {
		case hit && run < 0:
			run = x
		case !hit && run >= 0:
			marks = append(marks, (run+x-1)/2)
			run = -1
		}
	}
	if len(marks) != 3 {
		t.Fatalf("the image carries %d markers at %v, want 3", len(marks), marks)
	}
	if gap := (marks[2] - marks[1]) - (marks[1] - marks[0]); gap < -2 || gap > 2 {
		t.Errorf("the markers are at %v; three events six hours apart must be evenly spaced", marks)
	}
	if marks[0] > w/10 {
		t.Errorf("the first marker is at %d, too far in for an event at the start of the range", marks[0])
	}
	if marks[2] < w*9/10 {
		t.Errorf("the last marker is at %d, too far in for an event at the end of the range", marks[2])
	}

	// An event the filter rules out is not on the picture at all.
	img = decodePNG(t, e.get(pngURL(0, 12*time.Hour, "&status=rejected")))
	if n := countColor(img, markerRGB); n != 0 {
		t.Errorf("%d marker pixels are drawn for a status nobody asked for", n)
	}
}

// A label drawn with a rune the font does not hold reads as a row of boxes.
func TestEveryLabelTheImageWritesHasGlyphs(t *testing.T) {
	texts := []string{emptyMessage, axisTitle}
	for _, status := range append([]string{}, filterStates...) {
		texts = append(texts, caption(t0, t0.Add(12*time.Hour), 1, []string{status}, time.UTC, 2))
	}
	texts = append(texts, caption(t0, t0.Add(72*time.Hour), 0, filterStates, time.UTC, 0.5))
	for _, step := range xAxisSteps {
		texts = append(texts, timeLabel(t0.Add(9*time.Hour+7*time.Minute), step))
	}
	for _, v := range []float64{-10, 0, 33.5, 120} {
		texts = append(texts, levelLabel(v))
	}
	for _, s := range texts {
		for _, r := range s {
			if !hasGlyph(r) {
				t.Errorf("the font has no %q, which %q needs", r, s)
			}
		}
	}
}
