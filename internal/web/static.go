package web

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	webui "github.com/minayousseif/stompwatch/web"
)

// assetPrefix is where the build puts files whose name holds a hash of their
// content. Those may be cached forever; nothing else may.
const assetPrefix = "/assets/"

// static serves the built interface. A path that is not a file and is not
// under /api gets index.html, so a client-side route survives a refresh.
func (s *Server) static() http.Handler {
	dist, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		// The files are embedded at build time, so this cannot happen in a
		// built binary.
		s.log.Error("the dashboard files are not embedded", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fail(w, http.StatusInternalServerError, "the dashboard files are missing from this build.")
		})
	}
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		if _, err := fs.Stat(dist, name); err == nil {
			if strings.HasPrefix(r.URL.Path, assetPrefix) {
				// The name holds a hash of the content, so a change is a new
				// name and this copy is good forever.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
			return
		}

		// A missing asset must not become the page: the browser would run
		// HTML as JavaScript and the failure would make no sense.
		if strings.HasPrefix(r.URL.Path, assetPrefix) {
			http.NotFound(w, r)
			return
		}
		s.serveIndex(w, r, dist)
	})
}

// serveIndex sends the interface itself. It is never cached, so a new build
// takes effect on the next refresh.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		s.log.Error("index.html is not in the build", "err", err)
		fail(w, http.StatusInternalServerError, "the dashboard files are missing from this build.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(b)
	}
}
