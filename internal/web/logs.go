package web

import (
	"net/http"

	"github.com/minayousseif/stompwatch/internal/logs"
)

// logLevels is every level a line may carry. RAW is a line that was not
// valid JSON, which the reader keeps rather than drops.
var logLevels = []string{"DEBUG", "INFO", "WARN", "ERROR", logs.LevelRaw}

type logLineJSON struct {
	TSMS  int64          `json:"ts_ms"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs"`
}

type logsJSON struct {
	Truncated bool `json:"truncated"`
	// More says an older page exists. The log has no cheap total: counting
	// every matching line means reading the whole log, which is the cost the
	// backwards scan exists to avoid. One extra line answers the only
	// question the view asks.
	More  bool          `json:"more"`
	Lines []logLineJSON `json:"lines"`
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "from", "to", "level", "q", "limit", "offset")
	limit := p.intIn("limit", 200, 1, 1000)
	f := logs.Filter{
		FromMS: p.ms("from"),
		ToMS:   p.ms("to"),
		Levels: p.many("level", logLevels...),
		Query:  p.text("q"),
		// One line past the page, to answer More. It is dropped below.
		Limit:  limit + 1,
		Offset: p.intIn("offset", 0, 0, 1<<30),
	}
	if !p.ok(w) {
		return
	}

	lines, truncated, err := logs.Read(s.cfg.LogDir, s.cfg.LogName, f)
	if err != nil {
		s.log.Error("reading the log failed", "err", err)
		fail(w, http.StatusInternalServerError,
			"the log could not be read just now. The measurement is unaffected.")
		return
	}
	more := len(lines) > limit
	if more {
		lines = lines[:limit]
	}
	out := logsJSON{Truncated: truncated, More: more, Lines: make([]logLineJSON, len(lines))}
	for i, l := range lines {
		out.Lines[i] = logLineJSON{TSMS: l.TSMS, Level: l.Level, Msg: l.Msg, Attrs: l.Attrs}
	}
	writeJSON(w, http.StatusOK, out)
}
