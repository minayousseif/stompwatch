package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/minayousseif/stompwatch/internal/store"
)

// csvColumns is the header row, and the order every row follows.
//
// level_uncertainty_db and sensitivity_source come from that event's own
// capture-settings row, so one file may carry rows measured under different
// calibrations. A CSV has no comment syntax, so a column is the honest way
// to carry them (SPEC.md section 15 decision 23).
var csvColumns = []string{
	"id", "started_local", "ended_local", "duration_s", "laeq_db", "lamax_db", "baseline_db",
	"class", "confidence", "level_uncertainty_db", "sensitivity_source",
	"review_status", "review_note", "reviewer", "reviewed_local",
	"audio_sha256",
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "from", "to", "status")
	from, to := p.requireMS("from"), p.requireMS("to")
	statuses := p.many("status", exportStates...)
	if !p.ok(w) {
		return
	}
	if to < from {
		fail(w, http.StatusBadRequest, "to is before from. Give the range the other way round.")
		return
	}
	if len(statuses) == 0 {
		// The export is evidence, so it holds what a person has checked
		// unless the owner asks for more.
		statuses = []string{store.StatusVerified}
	}

	ctx := r.Context()
	// A muted event is left out. The export is evidence, and a complaint
	// should not rest on an evening the owner already knows about.
	unmuted := false
	rows, _, err := s.cfg.Store.ListEvents(ctx, store.EventFilter{
		FromMS: from, ToMS: to, Statuses: statuses, Muted: &unmuted, Sort: "started_ms",
	})
	if err != nil {
		s.serverError(w, "listing events for the export", err)
		return
	}

	// The whole history at once. It holds one row per change of the
	// instrument, so it is small, and a query per event would be many.
	history, err := s.cfg.Store.CaptureSettingsHistory(ctx)
	if err != nil {
		s.serverError(w, "reading the capture settings for the export", err)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="events-%s-to-%s.csv"`,
		s.day(from), s.day(to)))
	cw := csv.NewWriter(w)
	cw.Write(csvColumns)
	for _, e := range rows {
		sum := ""
		if e.HasAudio {
			if m, err := s.cfg.Store.MediaFile(ctx, e.ID, store.KindAudio); err == nil {
				sum = m.SHA256
			} else if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("a clip hash could not be read for the export", "event", e.ID, "err", err)
			}
		}
		cw.Write(safeRow(s.eventRow(e, sum, history)))
	}
	cw.Flush()
}

// eventRow lays one event out in the order of csvColumns.
func (s *Server) eventRow(e store.EventRow, sum string, history store.CaptureHistory) []string {
	status, note, reviewer, reviewed := "", "", "", ""
	if e.Review != nil {
		status, note, reviewer = e.Review.Status, e.Review.Note, e.Review.Reviewer
		reviewed = e.Review.At.In(s.loc).Format(time.RFC3339)
	}
	// Both cells stay empty for an event older than the history. An empty
	// cell says the settings were not recorded; a number there would be one
	// nobody measured.
	uncertainty, source := "", ""
	if cs, ok := history.At(e.StartedMS); ok {
		uncertainty, source = oneDecimal(cs.UncertaintyDB), cs.Source
	}
	return []string{
		strconv.FormatInt(e.ID, 10),
		s.localTime(e.StartedMS),
		s.localTime(e.EndedMS),
		strconv.FormatFloat(float64(e.DurationMS)/1000, 'f', 1, 64),
		oneDecimal(e.LAeq), oneDecimal(e.LAmax), oneDecimal(e.BaselineAtTrigger),
		e.Class,
		strconv.FormatFloat(e.Confidence, 'f', 2, 64),
		uncertainty, source,
		status, note, reviewer, reviewed, sum,
	}
}

// safeRow stops a spreadsheet reading a field as a formula. The review note
// is written by a person and is the field that matters, but the rule is the
// same for every column so there is nothing to remember.
func safeRow(row []string) []string {
	for i, f := range row {
		if strings.IndexByte("=+-@", firstByte(f)) >= 0 {
			row[i] = "'" + f
		}
	}
	return row
}

func firstByte(s string) byte {
	if s == "" {
		return 0
	}
	return s[0]
}

// localTime writes an instant the way a person reads it, with the offset, so
// a reader in another zone can still tell when it happened.
func (s *Server) localTime(msec int64) string {
	return time.UnixMilli(msec).In(s.loc).Format(time.RFC3339)
}

// day names the local date of an instant, for a file name.
func (s *Server) day(msec int64) string {
	return time.UnixMilli(msec).In(s.loc).Format("2006-01-02")
}

func oneDecimal(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
