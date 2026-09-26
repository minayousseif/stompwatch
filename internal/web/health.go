package web

import (
	"net/http"

	"github.com/minayousseif/stompwatch/internal/store"
)

// healthKinds is every kind a record may have. A filter value outside this
// list is a typo, and a typo that returned an empty list would read as "all
// is well".
var healthKinds = []string{
	store.HealthCaptureGap, store.HealthStuckStream, store.HealthFrameDrop,
	store.HealthClockStep, store.HealthClockDrift, store.HealthGainChange,
	store.HealthWriteError, store.HealthDiskLow, store.HealthLoopRestart,
	store.HealthClipTruncated, store.HealthClipNoAudio,
	store.HealthMediaMissing, store.HealthMediaPurge,
	store.HealthCameraDisconnect,
	store.HealthSettingsChange,
	store.HealthDataReset,
	store.HealthRecordingPause,
	store.HealthSettingsChanged,
	store.HealthMuteWindowAdded, store.HealthMuteWindowRemoved,
}

type healthRecordJSON struct {
	ID         int64  `json:"id"`
	TSMS       int64  `json:"ts_ms"`
	Kind       string `json:"kind"`
	Detail     string `json:"detail"`
	DurationMS int64  `json:"duration_ms"`
}

type healthListJSON struct {
	Total   int                `json:"total"`
	Records []healthRecordJSON `json:"records"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "from", "to", "kind", "limit", "offset")
	f := store.HealthFilter{
		FromMS: p.ms("from"),
		ToMS:   p.ms("to"),
		Kinds:  p.many("kind", healthKinds...),
		Limit:  p.intIn("limit", 100, 1, 500),
		Offset: p.intIn("offset", 0, 0, 1<<30),
	}
	if !p.ok(w) {
		return
	}
	rows, total, err := s.cfg.Store.ListHealth(r.Context(), f)
	if err != nil {
		s.serverError(w, "reading the health log", err)
		return
	}
	out := healthListJSON{Total: total, Records: make([]healthRecordJSON, len(rows))}
	for i, h := range rows {
		out.Records[i] = healthRecordJSON{ID: h.ID, TSMS: h.TSMS, Kind: h.Kind,
			Detail: h.Detail, DurationMS: h.DurationMS}
	}
	writeJSON(w, http.StatusOK, out)
}
