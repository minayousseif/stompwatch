package web

import (
	"errors"
	"net/http"
	"os"

	"github.com/minayousseif/stompwatch/internal/store"
)

// handleEventVideo serves the stored video clip byte for byte. Range
// requests work, because a browser fetches a video in pieces and seeks by
// asking for another one. The hash is not recomputed here: it would be
// recomputed for every piece. The media entry carries it for a check by
// hand.
func (s *Server) handleEventVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the event number must be a whole number above zero.")
		return
	}
	if p := parseQuery(r); !p.ok(w) {
		return
	}
	m, err := s.cfg.Store.MediaFile(r.Context(), id, store.KindVideo)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "there is no video clip for that event.")
		return
	}
	if err != nil {
		s.serverError(w, "reading the video of an event", err)
		return
	}
	if m.Purged != nil {
		s.clipWasPurged(w, m)
		return
	}
	path, err := resolveMedia(s.cfg.VideoDir, m.Path)
	if err != nil {
		s.serverError(w, "finding the video of an event", err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.clipIsMissing(w, m)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.serverError(w, "reading a video clip", err)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
