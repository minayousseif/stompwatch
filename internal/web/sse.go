package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/minayousseif/stompwatch/internal/meter"
)

// liveJSON is one line of the live meter. The levels are null until the
// first second has been measured: a zero would draw as silence.
type liveJSON struct {
	TMS      int64    `json:"t"`
	LAeq     *float64 `json:"laeq"`
	LAmax    *float64 `json:"lamax"`
	Baseline *float64 `json:"baseline"`
	Stale    bool     `json:"stale"`
}

// handleLive streams the newest measured second to one browser.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r)
	if !p.ok(w) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "the live meter needs a connection that can be streamed.")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	bins, stop := s.cfg.Live.Subscribe()
	defer stop()

	stale := time.NewTicker(s.staleAfter)
	defer stale.Stop()
	alive := time.NewTicker(s.keepalive)
	defer alive.Stop()

	var last *liveJSON
	said := false // a stale line has gone out since the last bin

	send := func(v liveJSON) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case b, open := <-bins:
			if !open {
				return
			}
			v := fromBin(b)
			last, said = &v, false
			// The silence is counted from this bin, not from a fixed grid.
			stale.Reset(s.staleAfter)
			if !send(v) {
				return
			}
		case <-stale.C:
			// Nothing has arrived for a while. Say so once, so the interface
			// can show that measurement has stopped rather than freezing on
			// an old number.
			if said {
				continue
			}
			said = true
			v := liveJSON{TMS: s.now().UnixMilli(), Stale: true}
			if last != nil {
				v = *last
				v.Stale = true
			}
			if !send(v) {
				return
			}
		case <-alive.C:
			if _, err := w.Write([]byte(":keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func fromBin(b meter.Bin) liveJSON {
	laeq, lamax, baseline := b.LAeq, b.LAmax, b.Baseline
	return liveJSON{TMS: b.Start.UnixMilli(), LAeq: &laeq, LAmax: &lamax, Baseline: &baseline}
}
