package web

import (
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
)

func csvURL(from, to time.Duration, extra string) string {
	u := "/api/export/events.csv?from=" + strconv.FormatInt(ms(from), 10) +
		"&to=" + strconv.FormatInt(ms(to), 10)
	return u + extra
}

// readCSV reads the body with the standard library's own reader, so nothing
// here checks the server against the server.
func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v: %s", err, body)
	}
	return rows
}

// The columns are the agreement. A spreadsheet built on them breaks if they
// move.
func TestCSVHasTheContractColumnsInOrder(t *testing.T) {
	e := newEnv(t)
	w := e.get(csvURL(0, time.Hour, ""))
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !hasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	rows := readCSV(t, w.Body.String())
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want only the header", len(rows))
	}
	want := []string{"id", "started_local", "ended_local", "duration_s", "laeq_db", "lamax_db",
		"baseline_db", "class", "confidence", "level_uncertainty_db", "sensitivity_source",
		"review_status", "review_note", "reviewer", "reviewed_local", "audio_sha256"}
	if len(rows[0]) != len(want) {
		t.Fatalf("the header has %d columns, want %d: %v", len(rows[0]), len(want), rows[0])
	}
	for i, name := range want {
		if rows[0][i] != name {
			t.Errorf("column %d is %q, want %q", i, rows[0][i], name)
		}
	}
}

// The export is evidence. A complaint must not rest on an evening the owner
// already knows about, so a muted event is left out of the file.
func TestCSVLeavesOutMutedEvents(t *testing.T) {
	e := newEnv(t)
	kept := e.addEvent(time.Second, 2*time.Second, 60, detect.Running)
	muted := e.addEvent(10*time.Second, time.Second, 70, detect.Jumping)
	e.addReview(kept, "verified", "kids again", "me@example.com")
	e.addReview(muted, "verified", "our own party", "me@example.com")
	e.addMuteWindow(ms(10*time.Second), ms(11*time.Second), "party")

	rows := readCSV(t, e.get(csvURL(0, time.Hour, "")).Body.String())
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the header and the one unmuted event: %v", len(rows), rows)
	}
	if rows[1][0] != strconv.FormatInt(kept, 10) {
		t.Errorf("the file holds event %q, want %d", rows[1][0], kept)
	}
}

// The file is read by a person, so the times carry their offset and the
// levels are rounded to something a person can read.
func TestCSVWritesLocalTimesAndOneDecimal(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, 2500*time.Millisecond, 60.26, detect.Running)
	e.addReview(id, "verified", "kids again", "me@example.com")

	rows := readCSV(t, e.get(csvURL(0, time.Hour, "")).Body.String())
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the header and one event", len(rows))
	}
	r := rows[1]
	if r[0] != strconv.FormatInt(id, 10) {
		t.Errorf("id = %q, want %d", r[0], id)
	}
	// t0 is 03:00 UTC and the tests read the clock in UTC.
	if r[1] != "2026-09-11T03:00:01Z" {
		t.Errorf("started_local = %q, want 2026-09-11T03:00:01Z", r[1])
	}
	if r[2] != "2026-09-11T03:00:03Z" {
		t.Errorf("ended_local = %q, want 2026-09-11T03:00:03Z", r[2])
	}
	if r[3] != "2.5" {
		t.Errorf("duration_s = %q, want 2.5", r[3])
	}
	if r[4] != "55.3" {
		t.Errorf("laeq_db = %q, want 55.3", r[4])
	}
	if r[5] != "60.3" {
		t.Errorf("lamax_db = %q, want 60.3", r[5])
	}
	if r[6] != "33.0" {
		t.Errorf("baseline_db = %q, want 33.0", r[6])
	}
	if r[7] != "running" {
		t.Errorf("class = %q, want running", r[7])
	}
	if r[11] != "verified" || r[12] != "kids again" || r[13] != "me@example.com" {
		t.Errorf("the review columns are %q, %q, %q", r[11], r[12], r[13])
	}
	if r[14] != "2026-09-11T03:00:00Z" {
		t.Errorf("reviewed_local = %q, want 2026-09-11T03:00:00Z", r[14])
	}
}

// A spreadsheet treats a field that starts with =, +, - or @ as a formula.
// The note is written by a person and is the field that matters here.
func TestCSVStopsANoteBeingReadAsAFormula(t *testing.T) {
	e := newEnv(t)
	notes := []string{"=SUM(A1)", "+1", "-drilling again", "@home"}
	for i, note := range notes {
		id := e.addEvent(time.Duration(i)*time.Second, time.Second, 60, detect.Running)
		e.addReview(id, "verified", note, "me@example.com")
	}

	rows := readCSV(t, e.get(csvURL(0, time.Hour, "")).Body.String())
	if len(rows) != len(notes)+1 {
		t.Fatalf("rows = %d, want the header and %d events", len(rows), len(notes))
	}
	want := []string{"'=SUM(A1)", "'+1", "'-drilling again", "'@home"}
	for i, r := range rows[1:] {
		if r[12] != want[i] {
			t.Errorf("review_note = %q, want %q", r[12], want[i])
		}
	}
}

// An ordinary note is written as it stands. A quote in front of every note
// would be noise in the file the owner reads.
func TestCSVLeavesAnOrdinaryNoteAlone(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	e.addReview(id, "verified", "kids again, 10pm", "me@example.com")

	rows := readCSV(t, e.get(csvURL(0, time.Hour, "")).Body.String())
	if got := rows[1][12]; got != "kids again, 10pm" {
		t.Errorf("review_note = %q, want it unchanged", got)
	}
}

// The export is evidence for a complaint, so it holds only what a person has
// checked unless the owner asks for more.
func TestCSVExportsVerifiedEventsByDefault(t *testing.T) {
	e := newEnv(t)
	yes := e.addEvent(time.Second, time.Second, 60, detect.Running)
	no := e.addEvent(2*time.Second, time.Second, 61, detect.Jumping)
	e.addEvent(3*time.Second, time.Second, 62, detect.Stomping) // unreviewed
	e.addReview(yes, "verified", "", "me@example.com")
	e.addReview(no, "rejected", "", "me@example.com")

	rows := readCSV(t, e.get(csvURL(0, time.Hour, "")).Body.String())
	if len(rows) != 2 || rows[1][0] != strconv.FormatInt(yes, 10) {
		t.Fatalf("the default export has %d rows, want only the verified event", len(rows)-1)
	}

	rows = readCSV(t, e.get(csvURL(0, time.Hour, "&status=verified&status=rejected")).Body.String())
	if len(rows) != 3 {
		t.Errorf("asking for two statuses gave %d events, want 2", len(rows)-1)
	}
}

func TestCSVCarriesTheClipHash(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	e.addClip(id, 1000, []int16{0, 800})
	e.addReview(id, "verified", "", "me@example.com")

	rows := readCSV(t, e.get(csvURL(0, time.Hour, "")).Body.String())
	if got := rows[1][15]; len(got) != 64 {
		t.Errorf("audio_sha256 = %q, want the 64 hex digits of a SHA-256", got)
	}
}

func TestCSVAttachmentNamesTheDateRange(t *testing.T) {
	e := newEnv(t)
	w := e.get(csvURL(0, 25*time.Hour, ""))
	want := `attachment; filename="events-2026-09-11-to-2026-09-12.csv"`
	if got := w.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
}

func TestCSVRejectsABadRequest(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.get("/api/export/events.csv"), 400, "from")
	wantError(t, e.get(csvURL(0, time.Hour, "&status=maybe")), 400, "status")
	wantError(t, e.get(csvURL(0, time.Hour, "&sort=lamax")), 400, "sort")
	wantError(t, e.get(csvURL(time.Hour, 0, "")), 400, "")
}

// The claim the export makes is that a person looked at every event in it.
// Allowing status=none would let a caller build a file of events nobody
// reviewed and still call it a report.
func TestCSVRefusesUnreviewedEvents(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.get("/api/export/events.csv?from=0&to=1&status=none"), 400, "status")
	wantError(t, e.get("/api/export/timeline.png?from=0&to=1&status=none"), 400, "status")
}
