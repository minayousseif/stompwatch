package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/minayousseif/stompwatch/internal/store"
)

// maxReason is as long a reason as a mute window accepts.
const maxReason = 500

type muteWindowJSON struct {
	ID      int64  `json:"id"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Reason  string `json:"reason"`
}

type muteWindowsJSON struct {
	Windows []muteWindowJSON `json:"windows"`
}

func (s *Server) handleMuteWindows(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r)
	if !p.ok(w) {
		return
	}
	rows, err := s.cfg.Store.MuteWindows(r.Context())
	if err != nil {
		s.serverError(w, "reading the mute windows", err)
		return
	}
	out := muteWindowsJSON{Windows: make([]muteWindowJSON, len(rows))}
	for i, m := range rows {
		out.Windows[i] = muteWindowJSON{ID: m.ID, StartMS: m.StartMS, EndMS: m.EndMS, Reason: m.Reason}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddMuteWindow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StartMS int64  `json:"start_ms"`
		EndMS   int64  `json:"end_ms"`
		Reason  string `json:"reason"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.EndMS <= body.StartMS {
		fail(w, http.StatusBadRequest, "the window must end after it starts.")
		return
	}
	if len([]rune(body.Reason)) > maxReason {
		fail(w, http.StatusBadRequest, "the reason is too long. Keep it under 500 characters.")
		return
	}

	id, err := s.cfg.Store.AddMuteWindow(r.Context(),
		store.MuteWindow{StartMS: body.StartMS, EndMS: body.EndMS, Reason: body.Reason})
	if err != nil {
		s.serverError(w, "adding a mute window", err)
		return
	}
	s.recordMuteWindow(r, store.HealthMuteWindowAdded, "added",
		store.MuteWindow{ID: id, StartMS: body.StartMS, EndMS: body.EndMS, Reason: body.Reason})
	writeJSON(w, http.StatusCreated,
		muteWindowJSON{ID: id, StartMS: body.StartMS, EndMS: body.EndMS, Reason: body.Reason})
}

// recordMuteWindow logs who added or removed a mute window, and writes a
// system_health row that names the login and the window.
func (s *Server) recordMuteWindow(r *http.Request, kind, did string, m store.MuteWindow) {
	login, _ := Identity(r)
	const layout = "2006-01-02 15:04:05 MST"
	window := fmt.Sprintf("window %d, %s to %s, reason %q", m.ID,
		time.UnixMilli(m.StartMS).In(s.loc).Format(layout),
		time.UnixMilli(m.EndMS).In(s.loc).Format(layout), m.Reason)
	s.log.Info("mute window "+did, "by", login, "id", m.ID, "start_ms", m.StartMS, "end_ms", m.EndMS)
	s.cfg.RecordHealth(s.now(), kind, login+" "+did+" mute "+window, 0)
}

func (s *Server) handleDeleteMuteWindow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the mute window number must be a whole number above zero.")
		return
	}
	// The window is read first, so the record can say which one went.
	windows, err := s.cfg.Store.MuteWindows(r.Context())
	if err != nil {
		s.serverError(w, "reading the mute windows", err)
		return
	}
	gone := store.MuteWindow{ID: id}
	for _, m := range windows {
		if m.ID == id {
			gone = m
		}
	}
	err = s.cfg.Store.DeleteMuteWindow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "there is no mute window with that number.")
		return
	}
	if err != nil {
		s.serverError(w, "removing a mute window", err)
		return
	}
	s.recordMuteWindow(r, store.HealthMuteWindowRemoved, "removed", gone)
	w.WriteHeader(http.StatusNoContent)
}
