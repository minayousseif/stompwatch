package web

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/minayousseif/stompwatch/internal/store"
)

// The timeline image is printed or pasted into an email, so it is drawn on a
// light background rather than in the dashboard's dark theme, and every
// label is big enough to read on paper.

var (
	pageColor   = color.RGBA{244, 244, 244, 255} // the paper the plot sits on
	dayColor    = color.RGBA{255, 255, 255, 255} // the plot area by day
	quietColor  = color.RGBA{221, 227, 238, 255} // the plot area at night
	gridColor   = color.RGBA{200, 200, 200, 255}
	frameColor  = color.RGBA{120, 120, 120, 255}
	traceColor  = color.RGBA{26, 78, 138, 255}
	markerColor = color.RGBA{198, 40, 40, 255}
	inkColor    = color.RGBA{34, 34, 34, 255}
)

const (
	// The margins leave room for the level labels on the left and for the
	// times and the caption below.
	plotLeft   = 56
	plotRight  = 16
	plotTop    = 30
	plotBottom = 48
	// textScale makes each 5 by 7 glyph 10 by 14, which stays readable on a
	// printed page.
	textScale = 2
	// markerHeight is how tall an event tick is, in pixels.
	markerHeight = 10
	// emptyMessage stands where the trace would be when the range holds no
	// measurement. A flat line at zero would be a claim nobody made.
	emptyMessage = "no measurement in this range"
	// axisTitle names the unit of the left hand scale.
	axisTitle = "dB"
)

// xAxisSteps are the spacings the time labels may use, smallest first.
var xAxisSteps = []time.Duration{
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute,
	30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
	24 * time.Hour, 48 * time.Hour, 72 * time.Hour, 96 * time.Hour, 7 * 24 * time.Hour,
}

func (s *Server) handleExportPNG(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "from", "to", "status", "width", "height")
	from, to := p.requireMS("from"), p.requireMS("to")
	statuses := p.many("status", exportStates...)
	width := p.intIn("width", 1200, 480, 2400)
	height := p.intIn("height", 400, 240, 1200)
	if !p.ok(w) {
		return
	}
	if to < from {
		fail(w, http.StatusBadRequest, "to is before from. Give the range the other way round.")
		return
	}
	span := rangeSpan(from, to)
	if !s.rangeFits(w, span) {
		return
	}
	if len(statuses) == 0 {
		statuses = []string{store.StatusVerified}
	}

	ctx := r.Context()
	points, bucketMS, err := s.timelinePoints(ctx, from, to, span > secondRange)
	if err != nil {
		s.serverError(w, "reading the timeline for the image", err)
		return
	}
	events, _, err := s.cfg.Store.ListEvents(ctx, store.EventFilter{
		FromMS: from, ToMS: to, Statuses: statuses, Limit: maxTimelineEvents,
	})
	if err != nil {
		s.serverError(w, "reading the events for the image", err)
		return
	}
	c := s.cfg.Settings.Current()
	quiet := c.Quiet.Spans(time.UnixMilli(from).In(s.loc), time.UnixMilli(to).In(s.loc))

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	plot := plotting{
		from: from, to: to, bucketMS: bucketMS, points: points,
		statuses: statuses, loc: s.loc,
		// The picture is of a range, not of one event, so the caption
		// carries the uncertainty in force now. An event cites its own.
		uncertaintyDB: c.SensitivityUncertaintyDB,
	}
	for _, e := range events {
		plot.events = append(plot.events, e.StartedMS)
	}
	for _, sp := range quiet {
		plot.quiet = append(plot.quiet, [2]int64{sp.From.UnixMilli(), sp.To.UnixMilli()})
	}
	plot.draw(img)

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="timeline-%s-to-%s.png"`,
		s.day(from), s.day(to)))
	if err := png.Encode(w, img); err != nil {
		s.log.Error("the timeline image could not be written", "err", err)
	}
}

// plotting is everything one image is drawn from.
type plotting struct {
	from, to int64
	bucketMS int64
	points   []pointJSON
	events   []int64    // the start of each matching event
	quiet    [][2]int64 // quiet spans, already clipped to the range
	statuses []string   // which reviews the markers stand for
	loc      *time.Location
	// uncertaintyDB is the plus or minus on every level drawn, in dB. The
	// picture leaves the dashboard, so it has to carry it
	// (SPEC.md section 15 decision 23).
	uncertaintyDB float64
}

func (p plotting) draw(img *image.RGBA) {
	b := img.Bounds()
	area := image.Rect(plotLeft, plotTop, b.Dx()-plotRight, b.Dy()-plotBottom)
	fill(img, b, pageColor)
	fill(img, area, dayColor)

	// The night goes down first, so the trace and the markers sit on top of
	// it rather than under it.
	for _, sp := range p.quiet {
		x0, x1 := p.x(area, sp[0]), p.x(area, sp[1])
		if x1 > x0 {
			fill(img, image.Rect(x0, area.Min.Y, x1, area.Max.Y), quietColor)
		}
	}

	lo, hi := p.levelRange()
	step := gridStep(hi - lo)
	for v := math.Ceil(lo/step) * step; v <= hi+0.001; v += step {
		y := p.y(area, lo, hi, v)
		fill(img, image.Rect(area.Min.X, y, area.Max.X, y+1), gridColor)
		label := levelLabel(v)
		drawText(img, area.Min.X-6-textWidth(label, textScale), y-glyphHeight*textScale/2,
			label, inkColor, textScale)
	}
	drawText(img, 4, 4, axisTitle, inkColor, textScale)

	// The frame goes on before the trace and the markers, so an event at the
	// very edge of the range is not cut in half by the border.
	frame(img, area, frameColor)
	p.drawTrace(img, area, lo, hi)
	p.drawMarkers(img, area)
	p.drawTimes(img, area)

	if len(p.points) == 0 {
		msg := emptyMessage
		drawText(img, area.Min.X+(area.Dx()-textWidth(msg, textScale))/2,
			area.Min.Y+area.Dy()/2-glyphHeight*textScale/2, msg, inkColor, textScale)
	}

	// The caption drops to the small size rather than running off the edge
	// of a narrow image.
	text := caption(time.UnixMilli(p.from).In(p.loc), time.UnixMilli(p.to).In(p.loc),
		len(p.events), p.statuses, p.loc, p.uncertaintyDB)
	scale := textScale
	if textWidth(text, scale) > b.Dx()-8 {
		scale = 1
	}
	drawText(img, max(4, (b.Dx()-textWidth(text, scale))/2),
		b.Dy()-glyphHeight*scale-6, text, inkColor, scale)
}

// drawTrace joins the points. Two points that are not neighboring buckets
// are left unjoined: the gap is where the measurement stopped, and a line
// drawn across it would be a claim nobody made.
func (p plotting) drawTrace(img *image.RGBA, area image.Rectangle, lo, hi float64) {
	for i, pt := range p.points {
		x, y := p.x(area, pt.T), p.y(area, lo, hi, pt.LAeq)
		if i > 0 && pt.T-p.points[i-1].T <= p.bucketMS {
			prev := p.points[i-1]
			line(img, p.x(area, prev.T), p.y(area, lo, hi, prev.LAeq), x, y, area, traceColor)
			continue
		}
		dot(img, x, y, area, traceColor)
	}
}

func (p plotting) drawMarkers(img *image.RGBA, area image.Rectangle) {
	for _, at := range p.events {
		x := p.x(area, at)
		if x < area.Min.X || x >= area.Max.X {
			continue
		}
		fill(img, image.Rect(x-1, area.Max.Y-markerHeight, x+2, area.Max.Y), markerColor)
	}
}

// drawTimes labels the time axis at a spacing that leaves the labels apart.
func (p plotting) drawTimes(img *image.RGBA, area image.Rectangle) {
	step := p.timeStep(area.Dx())
	from := time.UnixMilli(p.from).In(p.loc)
	for at := truncateLocal(from, step, p.loc); !at.After(time.UnixMilli(p.to)); at = at.Add(step) {
		x := p.x(area, at.UnixMilli())
		if x < area.Min.X || x >= area.Max.X {
			continue
		}
		fill(img, image.Rect(x, area.Max.Y, x+1, area.Max.Y+4), frameColor)
		label := timeLabel(at, step)
		left := x - textWidth(label, textScale)/2
		left = min(max(left, 2), img.Bounds().Dx()-textWidth(label, textScale)-2)
		drawText(img, left, area.Max.Y+7, label, inkColor, textScale)
	}
}

// timeStep picks the smallest spacing that keeps the labels from touching.
func (p plotting) timeStep(width int) time.Duration {
	span := time.Duration(p.to-p.from) * time.Millisecond
	// A label is at most six characters wide, and each one needs a gap.
	most := max(width/(textWidth("00-00", textScale)+24), 2)
	for _, step := range xAxisSteps {
		if int(span/step) <= most {
			return step
		}
	}
	return xAxisSteps[len(xAxisSteps)-1]
}

// levelRange is the span of dB the plot covers, rounded out to whole tens so
// the grid lines land on round numbers.
func (p plotting) levelRange() (float64, float64) {
	if len(p.points) == 0 {
		return 20, 80
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, pt := range p.points {
		lo, hi = math.Min(lo, pt.LAeq), math.Max(hi, pt.LAeq)
	}
	lo, hi = math.Floor((lo-3)/10)*10, math.Ceil((hi+3)/10)*10
	if hi-lo < 20 {
		hi = lo + 20
	}
	return lo, hi
}

// x is the column an instant falls in.
func (p plotting) x(area image.Rectangle, at int64) int {
	if p.to == p.from {
		return area.Min.X
	}
	f := float64(at-p.from) / float64(p.to-p.from)
	return area.Min.X + int(math.Round(f*float64(area.Dx()-1)))
}

// y is the row a level falls on. Louder is higher up the image.
func (p plotting) y(area image.Rectangle, lo, hi, v float64) int {
	f := (v - lo) / (hi - lo)
	y := area.Max.Y - 1 - int(math.Round(f*float64(area.Dy()-1)))
	return min(max(y, area.Min.Y), area.Max.Y-1)
}

// gridStep picks a spacing that puts three to six lines on the scale.
func gridStep(span float64) float64 {
	for _, step := range []float64{5, 10, 20, 25, 50, 100} {
		if span/step <= 6 {
			return step
		}
	}
	return 100
}

// levelLabel writes a level without a decimal point: the grid lines are on
// round numbers and a trailing ".0" is noise.
func levelLabel(v float64) string {
	if v == 0 {
		return "0" // a grid line at zero must not come out as "-0"
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

// timeLabel writes the time of a grid line. A spacing of a day or more gets
// the date instead of the clock.
func timeLabel(at time.Time, step time.Duration) string {
	if step >= 24*time.Hour {
		return at.Format("01-02")
	}
	return at.Format("15:04")
}

// caption says what the picture is of, because the picture leaves the
// dashboard and has to stand on its own.
// uncertaintyDB is the plus or minus on every level in the picture, and is
// the figure in force now: the picture is of a range and not of one event.
func caption(from, to time.Time, events int, statuses []string, loc *time.Location, uncertaintyDB float64) string {
	word := "events"
	if events == 1 {
		word = "event"
	}
	list := ""
	for i, st := range statuses {
		if i > 0 {
			list += ", "
		}
		list += st
	}
	from, to = from.In(loc), to.In(loc)
	end := to.Format("2006-01-02 15:04")
	if from.Format("2006-01-02") == to.Format("2006-01-02") {
		end = to.Format("15:04") // one day needs its date said once
	}
	// The picture leaves the dashboard, so it says how well its levels are
	// known rather than presenting them as exact
	// (SPEC.md section 15 decision 23).
	return fmt.Sprintf("%s to %s, %d %s (%s), levels +/- %s dB",
		from.Format("2006-01-02 15:04"), end, events, word, list,
		strconv.FormatFloat(uncertaintyDB, 'f', 1, 64))
}

// truncateLocal rounds an instant down to a whole step of local time, so the
// labels land on the hour rather than on whatever the range started at.
func truncateLocal(at time.Time, step time.Duration, loc *time.Location) time.Time {
	at = at.In(loc)
	y, mo, d := at.Date()
	midnight := time.Date(y, mo, d, 0, 0, 0, 0, loc)
	if step >= 24*time.Hour {
		return midnight
	}
	return midnight.Add(at.Sub(midnight) / step * step)
}

// --- drawing ---

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r.Intersect(img.Bounds()), image.NewUniform(c), image.Point{}, draw.Src)
}

// frame outlines the plot so the edge of the measured range is visible.
func frame(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1), c)
	fill(img, image.Rect(r.Min.X, r.Max.Y-1, r.Max.X, r.Max.Y), c)
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y), c)
	fill(img, image.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y), c)
}

// dot draws one point of the trace, two pixels across so a lone bucket is
// still visible.
func dot(img *image.RGBA, x, y int, area image.Rectangle, c color.RGBA) {
	fill(img, image.Rect(x, y, x+2, y+2).Intersect(area), c)
}

// line joins two points of the trace with a straight run of dots.
func line(img *image.RGBA, x0, y0, x1, y1 int, area image.Rectangle, c color.RGBA) {
	steps := max(abs(x1-x0), abs(y1-y0))
	if steps == 0 {
		dot(img, x0, y0, area, c)
		return
	}
	for i := 0; i <= steps; i++ {
		f := float64(i) / float64(steps)
		x := x0 + int(math.Round(f*float64(x1-x0)))
		y := y0 + int(math.Round(f*float64(y1-y0)))
		dot(img, x, y, area, c)
	}
}

// drawText puts a string on the image at the scale the whole picture uses.
func drawText(img *image.RGBA, x, y int, s string, c color.RGBA, scale int) {
	for _, r := range s {
		for dy, row := range glyph(r) {
			for dx := 0; dx < glyphWidth && dx < len(row); dx++ {
				if row[dx] != '#' {
					continue
				}
				fill(img, image.Rect(x+dx*scale, y+dy*scale,
					x+(dx+1)*scale, y+(dy+1)*scale), c)
			}
		}
		x += (glyphWidth + glyphGap) * scale
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
