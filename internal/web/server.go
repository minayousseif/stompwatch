package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/settings"
	"github.com/minayousseif/stompwatch/internal/store"
	"github.com/minayousseif/stompwatch/internal/tailnet"
	"github.com/minayousseif/stompwatch/internal/video"
)

// Config is everything the server reads. Every field is required unless
// the comment says otherwise.
type Config struct {
	Store    *store.Store
	Settings *settings.Live
	Status   *health.Status
	Live     *LiveFeed
	Stats    func() Stats // pipeline counters
	Capture  Capture      // what the microphone is doing
	Started  time.Time
	LogDir   string
	LogName  string
	ClipDir  string
	// VideoDir is the root the video clips are served from. Empty when
	// there is no camera; then a video row, which should not exist, is a
	// server error and never a file read.
	VideoDir string
	// Camera reports the camera's state. nil means video is off.
	Camera func() CameraStatus
	// CameraLogin says whether a camera login was read and where it came
	// from. The password inside the result goes to ffmpeg's argument list
	// and to Stream.Redact, and nowhere else: no response ever carries it
	// (SPEC.md section 3). nil means the command looked for no login.
	CameraLogin func() video.Login
	// FFmpegVersion is the version line ffmpeg printed at start, or empty
	// when ffmpeg is not installed. It is read once: running a program on
	// every system request is not what a system page is for.
	FFmpegVersion string
	// FFmpeg is the program the camera test runs. nil means ffmpeg from
	// PATH. FFmpegEnv is extra environment for it; tests use it.
	FFmpeg    video.Command
	FFmpegEnv []string
	Log       *slog.Logger     // nil means slog.Default()
	Now       func() time.Time // nil means time.Now
	// Tailscale reads what Tailscale says about this node. The dashboard
	// reports it and never configures it: setting up `tailscale serve` is
	// the owner's job (SPEC.md section 9.0). It is read at most once a
	// minute and cached, because a system page must not shell out on every
	// request. nil means the block says nothing is installed.
	Tailscale func(context.Context) tailnet.Status
	// RecordHealth stores a system_health row. The server uses it when a
	// media file named by the database has vanished. nil does nothing.
	RecordHealth func(at time.Time, kind, detail string, d time.Duration)
}

// Stats are the pipeline's counters, copied so this package does not depend
// on the pipeline. The command wires the two together.
type Stats struct {
	Chunks, DroppedSamples, BinsDropped, EventsDropped, WriteFailures, LoopRestarts int64
}

// Capture is what the microphone is doing, for the system page.
type Capture struct {
	Device, Gain, Calibration string
	Channel                   int
	SensitivityDBFS           float64
}

// CameraStatus is what the camera is doing, for the system page. It is a
// copy of the ring's status, so the command decides what the dashboard is
// told rather than the ring.
type CameraStatus struct {
	Enabled bool
	// SubPath is the RTSP path the ring is using now. With camera_rtsp_path
	// empty the ring works through the defaults in turn, so this is the
	// only way to say which one is in force.
	SubPath        string
	Connected      bool
	ConnectedSince time.Time // zero when not connected
	Uptime         time.Duration
	Disconnects    int64
	LastSegment    time.Time // zero before the first one
	RingSegments   int
	RingBytes      int64
	// Audio is the audio track the camera sends, and AudioKnown says
	// whether the camera was ever asked. The collector asks once at start,
	// and only while camera_audio is on, so with the setting off nothing is
	// known and the page must not claim the camera is silent
	// (SPEC.md section 15 decision 22).
	Audio      video.AudioInfo
	AudioKnown bool
}

const (
	// requestTimeout bounds every request but the live stream, so a slow
	// query cannot pile up connections.
	requestTimeout = 30 * time.Second
	// staleAfter is the silence after which the live meter says the
	// measurement has stopped.
	staleAfter = 5 * time.Second
	// keepalive keeps an idle SSE connection open through anything in the
	// path that times out a quiet socket.
	keepalive = 15 * time.Second
	// shutdownGrace is how long Run waits for open requests to finish.
	shutdownGrace = 5 * time.Second
)

// Server serves the dashboard.
type Server struct {
	cfg Config
	log *slog.Logger
	now func() time.Time
	loc *time.Location

	// Intervals, as fields so a test does not have to wait real seconds.
	timeout       time.Duration
	staleAfter    time.Duration
	keepalive     time.Duration
	probeDeadline time.Duration

	// probing is true while a camera test runs. One at a time: each one
	// opens connections to the camera and starts ffmpeg.
	probing atomic.Bool

	// reported remembers which events have already had a missing clip
	// recorded, so one broken file does not fill the health log.
	mu       sync.Mutex
	reported map[int64]bool

	// clips holds resampled clips, so playing one twice resamples it once.
	clips *clipCache

	// The last Tailscale reading and when it was taken. Reading it means
	// running a program, so it stands for a minute.
	tsMu     sync.Mutex
	tsCached tailscaleJSON
	tsRead   time.Time
}

// New checks the config and returns a server.
func New(c Config) (*Server, error) {
	switch {
	case c.Store == nil:
		return nil, errors.New("web: no store")
	case c.Settings == nil:
		return nil, errors.New("web: no settings")
	case c.Status == nil:
		return nil, errors.New("web: no health status")
	case c.Live == nil:
		return nil, errors.New("web: no live feed")
	case c.Stats == nil:
		return nil, errors.New("web: no pipeline stats")
	case c.Started.IsZero():
		return nil, errors.New("web: no start time")
	case c.LogDir == "":
		return nil, errors.New("web: no log directory")
	case c.LogName == "":
		return nil, errors.New("web: no log file name")
	case c.ClipDir == "":
		return nil, errors.New("web: no clip directory")
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.RecordHealth == nil {
		c.RecordHealth = func(time.Time, string, string, time.Duration) {}
	}
	return &Server{
		cfg: c, log: c.Log, now: c.Now, loc: time.Local,
		timeout: requestTimeout, staleAfter: staleAfter, keepalive: keepalive,
		probeDeadline: probeDeadline,
		reported:      make(map[int64]bool),
		clips:         newClipCache(),
	}, nil
}

// route is one endpoint. The path is registered twice: once per method, and
// once on its own, so a wrong method answers in JSON like everything else.
type route struct {
	method, path string
	h            http.HandlerFunc
}

func (s *Server) routes() []route {
	return []route{
		{"GET", "/api/summary", s.handleSummary},
		{"GET", "/api/timeline", s.handleTimeline},
		{"GET", "/api/events", s.handleEvents},
		{"GET", "/api/events/{id}", s.handleEvent},
		{"PUT", "/api/events/{id}/review", s.handlePutReview},
		{"DELETE", "/api/events/{id}/review", s.handleDeleteReview},
		{"GET", "/api/events/{id}/audio", s.handleEventAudio},
		{"GET", "/api/events/{id}/audio/original", s.handleEventAudioOriginal},
		{"GET", "/api/events/{id}/video", s.handleEventVideo},
		{"GET", "/api/events/{id}/waveform", s.handleEventWaveform},
		{"POST", "/api/media/purge", s.handlePurgeMedia},
		{"GET", "/api/media/usage", s.handleMediaUsage},
		{"GET", "/api/media/purgeable", s.handlePurgeable},
		{"GET", "/api/live", s.handleLive},
		{"GET", "/api/health", s.handleHealth},
		{"GET", "/api/system", s.handleSystem},
		{"POST", "/api/camera/probe", s.handleProbeCamera},
		{"GET", "/api/logs", s.handleLogs},
		{"GET", "/api/settings", s.handleGetSettings},
		{"PUT", "/api/settings", s.handlePutSettings},
		{"GET", "/api/mute-windows", s.handleMuteWindows},
		{"POST", "/api/mute-windows", s.handleAddMuteWindow},
		{"DELETE", "/api/mute-windows/{id}", s.handleDeleteMuteWindow},
		{"GET", "/api/export/events.csv", s.handleExportCSV},
		{"GET", "/api/export/timeline.png", s.handleExportPNG},
	}
}

// Handler returns the routes. Use it in tests with httptest.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	methods := make(map[string][]string)
	for _, rt := range s.routes() {
		mux.Handle(rt.method+" "+rt.path, rt.h)
		methods[rt.path] = append(methods[rt.path], rt.method)
	}
	// A request to a known path with the wrong method lands here, because a
	// pattern without a method is less specific than one with it.
	for path, allowed := range methods {
		allow := strings.Join(allowed, ", ")
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			fail(w, http.StatusMethodNotAllowed,
				fmt.Sprintf("this address answers %s, not %s.", allow, r.Method))
		})
	}
	noSuchEndpoint := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fail(w, http.StatusNotFound, "there is no such endpoint. Check the address.")
	})
	mux.Handle("/api/", noSuchEndpoint)
	mux.Handle("/api", noSuchEndpoint)
	mux.Handle("/", s.static())

	return s.recoverPanic(s.frameHeaders(s.apiHeaders(s.sameOrigin(s.identity(s.deadline(mux))))))
}

// Run listens on http_addr and serves until ctx is done. It returns nil
// when http_addr is empty, which means the dashboard is switched off.
func (s *Server) Run(ctx context.Context) error {
	addr := s.cfg.Settings.Current().HTTPAddr
	if addr == "" {
		s.log.Info("the dashboard is switched off; http_addr is empty")
		return nil
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web: listening on %s: %w", addr, err)
	}
	s.log.Info("the dashboard is listening", "addr", ln.Addr().String())

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("web: %w", err)
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(stop); err != nil {
		srv.Close()
	}
	<-done
	return nil
}

// --- middleware ---

// identityKey is the context key the login and name are stored under.
type identityKey struct{}

type identity struct{ login, name string }

// Identity returns the login and display name of the caller.
func Identity(r *http.Request) (login, name string) {
	id, ok := r.Context().Value(identityKey{}).(identity)
	if !ok {
		// A handler must never read an empty reviewer by accident.
		return "unknown", ""
	}
	return id.login, id.name
}

// funnelHeader is the header `tailscale serve` sets on a request that came
// in through Funnel, from the public internet. It sets no login header on
// such a request, and it deletes any funnelHeader the caller sent, so the
// header cannot be forged past it. Source: tailscale/tailscale,
// ipn/ipnlocal/serve.go, addTailscaleIdentityHeaders, line 1095 at commit
// 8af8f861c032: r.Out.Header.Set("Tailscale-Funnel-Request", "?1").
const funnelHeader = "Tailscale-Funnel-Request"

// identity reads who the network says is calling. Nothing here authenticates
// anyone: see the package comment.
//
// A request that came through Funnel is refused outright, the page included:
// the dashboard is never meant to be on the public internet. In tailscale
// and trusted_header mode an /api request with no login is refused too.
// Only a request that did not come through `tailscale serve` or the proxy
// has none, and serving it as "unknown" gave it full rights. The page
// itself still loads, so the owner sees the message the API sends.
func (s *Server) identity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(funnelHeader) != "" {
			s.log.Warn("a request through Tailscale Funnel was refused", "path", r.URL.Path)
			fail(w, http.StatusForbidden,
				"the dashboard does not answer requests from the public internet through Tailscale Funnel. "+
					"Turn Funnel off and open the dashboard on the tailnet.")
			return
		}
		c := s.cfg.Settings.Current()
		id := identity{login: "unknown"}
		if c.AuthMode == "none" {
			id = identity{login: "dev", name: "dev"}
		} else {
			if v := strings.TrimSpace(r.Header.Get(c.AuthHeader)); v != "" {
				id.login = v
			} else if isAPI(r.URL.Path) {
				fail(w, http.StatusUnauthorized, noLoginMessage(c.AuthMode, c.AuthHeader))
				return
			}
			if c.AuthNameHeader != "" {
				id.name = strings.TrimSpace(r.Header.Get(c.AuthNameHeader))
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

// noLoginMessage says why a request with no login was refused and how to
// get in.
func noLoginMessage(mode, header string) string {
	through := "Open the dashboard through tailscale serve, which sets it"
	if mode == "trusted_header" {
		through = "Open the dashboard through the reverse proxy, which sets it"
	}
	return "this request has no " + header + " header, so nobody is known to be calling. " +
		through + ". For an SSH tunnel, set auth_mode = none and keep http_addr on loopback."
}

// apiHeaders keeps every API answer out of every cache. The live stream sets
// its own value, which replaces this one.
func (s *Server) apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPI(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// frameHeaders stops another site from showing the dashboard in a frame,
// where a click meant for its page could land on a delete button, and stops
// the browser from guessing a type the server did not send. It is not a full
// Content-Security-Policy: that could break the dashboard for no gain here.
func (s *Server) frameHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin refuses a POST, PUT or DELETE that a page on another site
// sent. Any page the owner opens can send one, and `tailscale serve` then
// adds the owner's identity to it. The browser says where a request came
// from in Sec-Fetch-Site, or, before 2023, in Origin. A request with
// neither header is curl or a script, which no other site can drive, so it
// goes through.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.log.Warn("a request from another web site was refused", "method", r.Method,
			"path", r.URL.Path, "origin", r.Header.Get("Origin"))
		fail(w, http.StatusForbidden,
			"this request came from another web site, so the dashboard refused it. "+
				"Open the dashboard at its own address and try again.")
	}))
	return cop.Handler(next)
}

// deadline bounds every request but the live stream, which is meant to stay
// open for as long as the browser is watching.
func (s *Server) deadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/live" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverPanic keeps one broken handler from taking the collector down. The
// cause goes to the log, never to the browser.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			cause := recover()
			if cause == nil {
				return
			}
			s.log.Error("the request handler panicked", "method", r.Method, "path", r.URL.Path,
				"cause", fmt.Sprint(cause))
			fail(w, http.StatusInternalServerError,
				"something went wrong in the dashboard. The measurement is unaffected; "+
					"the detail is in the log.")
		}()
		next.ServeHTTP(w, r)
	})
}

func isAPI(path string) bool { return path == "/api" || strings.HasPrefix(path, "/api/") }
