package web

import (
	"net/http"
	"time"
)

// loudestJSON names the worst event in a range, so the interface can link
// straight to it.
type loudestJSON struct {
	ID        int64   `json:"id"`
	LAmax     float64 `json:"lamax"`
	StartedMS int64   `json:"started_ms"`
}

type summaryJSON struct {
	From            int64        `json:"from"`
	To              int64        `json:"to"`
	Events          int          `json:"events"`
	Reviewed        int          `json:"reviewed"`
	Verified        int          `json:"verified"`
	Loudest         *loudestJSON `json:"loudest"`
	LAeq            *float64     `json:"laeq"`
	Baseline        *float64     `json:"baseline"`
	QuietHourEvents int          `json:"quiet_hour_events"`
	// Muted counts the events a mute window covers. They are left out of
	// every other count here, so this is where they stay visible.
	Muted int `json:"muted"`
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "from", "to")
	from, to := p.requireMS("from"), p.requireMS("to")
	if !p.ok(w) {
		return
	}
	if to < from {
		fail(w, http.StatusBadRequest, "to is before from. Give the range the other way round.")
		return
	}
	// The summary counts the events in quiet hours, which is a quiet-hours
	// question.
	if !quietRangeFits(w, from, to) {
		return
	}

	c := s.cfg.Settings.Current()
	sum, err := s.cfg.Store.Summary(r.Context(), from, to, c.Quiet, s.loc)
	if err != nil {
		s.serverError(w, "summarizing the range", err)
		return
	}

	out := summaryJSON{
		From: from, To: to,
		Events: sum.Events, Reviewed: sum.Reviewed, Verified: sum.Verified,
		QuietHourEvents: sum.QuietHourEvents, Muted: sum.Muted,
	}
	if sum.Loudest != nil {
		out.Loudest = &loudestJSON{ID: sum.Loudest.ID, LAmax: sum.Loudest.LAmax, StartedMS: sum.Loudest.StartedMS}
	}
	// No seconds measured means no level. A zero would draw as silence.
	if sum.HaveLevels {
		laeq, baseline := sum.LAeq, sum.Baseline
		out.LAeq, out.Baseline = &laeq, &baseline
	}
	writeJSON(w, http.StatusOK, out)
}

// serverError logs the cause and tells the caller what to do. The cause
// itself never reaches the browser: it can name a file on disk.
func (s *Server) serverError(w http.ResponseWriter, doing string, err error) {
	s.log.Error("a dashboard request failed", "doing", doing, "err", err)
	fail(w, http.StatusInternalServerError,
		"the measurements could not be read just now. Try again in a moment.")
}

// dayStart is midnight at the start of the local day that holds t.
func (s *Server) dayStart(t time.Time) time.Time {
	t = t.In(s.loc)
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, s.loc)
}
