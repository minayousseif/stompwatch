package web

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/health"
	"github.com/minayousseif/stompwatch/internal/video"
)

// audioLimit is how long the instrument may go without audio before it is
// no longer collecting. It is the heartbeat's rule, so both agree.
const audioLimit = 10 * time.Second

type diskJSON struct {
	// Name says which store this is, not where it lives. A response never
	// names a file on disk.
	Name   string `json:"name"`
	FreeMB int64  `json:"free_mb"`
}

type clipsJSON struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

type countsJSON struct {
	EventsToday  int `json:"events_today"`
	EventsTotal  int `json:"events_total"`
	Unreviewed   int `json:"unreviewed"`
	SamplesToday int `json:"samples_today"`
}

type captureJSON struct {
	Device          string  `json:"device"`
	Channel         int     `json:"channel"`
	Gain            string  `json:"gain"`
	SensitivityDBFS float64 `json:"sensitivity_dbfs"`
	Calibration     string  `json:"calibration"`
}

// levelsJSON is how well the levels are known now. Every level the
// instrument reports is dBFS - sensitivity_dbfs + 94, so UncertaintyDB is
// the plus or minus on all of them. It is the current state, which is what a
// System screen is for; an event cites its own capture block instead
// (SPEC.md section 15 decision 23).
type levelsJSON struct {
	UncertaintyDB float64 `json:"uncertainty_db"`
	Source        string  `json:"source"`
	// MeasuredOn and Reference are empty for the datasheet figure, which was
	// not measured on a day and not measured against anything.
	MeasuredOn string `json:"measured_on"`
	Reference  string `json:"reference"`
}

type pipelineJSON struct {
	Chunks         int64 `json:"chunks"`
	DroppedSamples int64 `json:"dropped_samples"`
	BinsDropped    int64 `json:"bins_dropped"`
	EventsDropped  int64 `json:"events_dropped"`
	WriteFailures  int64 `json:"write_failures"`
	LoopRestarts   int64 `json:"loop_restarts"`
}

type heartbeatJSON struct {
	Configured bool  `json:"configured"`
	IntervalMS int64 `json:"interval_ms"`
}

type authJSON struct {
	Mode  string `json:"mode"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

type ringJSON struct {
	Segments int   `json:"segments"`
	Bytes    int64 `json:"bytes"`
}

// cameraJSON says whether the camera is delivering video, for how long it
// has in all, and how often it dropped. Uptime is time with video, not
// time since start, so a gap shows as uptime short of the process uptime.
type cameraJSON struct {
	Enabled          bool             `json:"enabled"`
	Connected        bool             `json:"connected"`
	ConnectedSinceMS int64            `json:"connected_since_ms"`
	UptimeMS         int64            `json:"uptime_ms"`
	Disconnects      int64            `json:"disconnects"`
	LastSegmentMS    int64            `json:"last_segment_ms"`
	Ring             ringJSON         `json:"ring"`
	Clips            clipsJSON        `json:"clips"`
	Config           cameraConfigJSON `json:"config"`
}

// credentialsJSON says whether a camera login was read and where it came
// from. There is no password field, and there must never be one: not empty,
// not masked, not present (SPEC.md section 3). User is the login name,
// which is not the secret.
type credentialsJSON struct {
	Loaded bool   `json:"loaded"`
	Source string `json:"source"` // environment, file, or none
	User   string `json:"user"`
}

// cameraConfigJSON is how the camera is set up. It is filled whether or not
// a camera is configured, because the owner reads it while bringing one up.
type cameraConfigJSON struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	SubPath  string `json:"sub_path"`
	MainPath string `json:"main_path"`
	// SubPathSource is "configured" when camera_rtsp_path names a path and
	// "discovered" when the collector found one by trying the defaults. A
	// discovered path is tried again on every reconnect, so the difference
	// matters to the owner.
	SubPathSource  string `json:"sub_path_source"`
	RingMinutes    int    `json:"ring_minutes"`
	SegmentSeconds int    `json:"segment_seconds"`
	// Audio is camera_audio: true means the ring and the event clips carry
	// the camera's own audio track, unfiltered, so they hold speech in
	// clear. AudioTrack is what the camera actually sends, which is not
	// known until something asks it (SPEC.md section 15 decision 22).
	Audio       bool            `json:"audio"`
	AudioTrack  audioTrackJSON  `json:"audio_track"`
	FFmpeg      string          `json:"ffmpeg"`
	Credentials credentialsJSON `json:"credentials"`
}

// Where a sub-stream path came from.
const (
	pathConfigured = "configured"
	pathDiscovered = "discovered"
	// pathNone is no path at all: none is configured and the collector has
	// not found one. Saying "discovered" here would claim a discovery that
	// has not happened.
	pathNone = "none"
)

// tailscaleJSON is what Tailscale says about this box. Every field is best
// effort and none of it is a failure: a box with no Tailscale answers
// installed false, and the collector is unaffected either way
// (SPEC.md section 9.0.1).
//
// It is built field by field out of the reading. The raw
// `tailscale status --json` answer carries the node key, every peer's key
// and the tailnet's user list, and none of that may cross this line.
type tailscaleJSON struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Backend   string `json:"backend"`
	Name      string `json:"name"`
	Serving   bool   `json:"serving"`
	ServeURL  string `json:"serve_url"`
	// Funnel true means the dashboard is published to the public internet,
	// which makes the identity header auth_mode trusts worthless. It is a
	// fault, not a reading.
	Funnel bool `json:"funnel"`
	// KeyExpiryMS is 0 when key expiry is disabled, which is what
	// SPEC.md section 9.0 asks for.
	KeyExpiryMS int64  `json:"key_expiry_ms"`
	Err         string `json:"err"`
	// HTTPAddr is the dashboard's listen address: what a serve target has
	// to point at, and what the command that fixes it names. It is here
	// because it is the one fact the Tailscale part of the screen needs
	// that no other response carries.
	HTTPAddr string `json:"http_addr"`
}

type pauseJSON struct {
	Span   string `json:"span"` // HH:MM-HH:MM, or several joined by ", "; empty for no pause
	Active bool   `json:"active"`
	// Until is the end of the window that holds now, HH:MM; empty when not active.
	Until string `json:"until"`
}

// pauseUntil is the end of the pause window that holds now, or empty when
// now is outside every window.
func pauseUntil(pause config.PauseWindows, now time.Time) string {
	q, ok := pause.Window(now)
	if !ok {
		return ""
	}
	return clock(q.End)
}

type systemJSON struct {
	StartedMS    int64 `json:"started_ms"`
	NowMS        int64 `json:"now_ms"`
	LastAudioMS  int64 `json:"last_audio_ms"`
	LastCommitMS int64 `json:"last_commit_ms"`
	Collecting   bool  `json:"collecting"`
	// RecordingPause is the daily recording pause and whether now is inside
	// it. Inside it no event opens, so a stretch with no events is explained
	// here rather than taken for a broken detector. Collecting stays true:
	// the level of every second is still measured.
	RecordingPause pauseJSON     `json:"recording_pause"`
	DBBytes        int64         `json:"db_bytes"`
	WALBytes       int64         `json:"wal_bytes"`
	SchemaVersion  int           `json:"schema_version"`
	Disk           []diskJSON    `json:"disk"`
	Clips          clipsJSON     `json:"clips"`
	Counts         countsJSON    `json:"counts"`
	Capture        captureJSON   `json:"capture"`
	Levels         levelsJSON    `json:"levels"`
	Pipeline       pipelineJSON  `json:"pipeline"`
	Heartbeat      heartbeatJSON `json:"heartbeat"`
	Auth           authJSON      `json:"auth"`
	Camera         cameraJSON    `json:"camera"`
	Tailscale      tailscaleJSON `json:"tailscale"`
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r)
	if !p.ok(w) {
		return
	}
	ctx := r.Context()
	c := s.cfg.Settings.Current()
	now := s.now()
	login, name := Identity(r)

	out := systemJSON{
		StartedMS:    s.cfg.Started.UnixMilli(),
		NowMS:        now.UnixMilli(),
		LastAudioMS:  s.cfg.Status.LastAudio().UnixMilli(),
		LastCommitMS: s.cfg.Status.LastCommit().UnixMilli(),
		// The same rule as the heartbeat: audio within 10 s and bins within
		// batch_interval x heartbeat_stall_factor.
		Collecting: s.cfg.Status.Check(now,
			time.Duration(float64(c.BatchInterval)*c.HeartbeatStallFactor), audioLimit) == nil,
		// The span is written the way the settings screen writes it, so the
		// two cannot disagree about what the pause is.
		RecordingPause: pauseJSON{
			Span:   settingValue("recording_pause", c),
			Active: c.RecordingPause.Contains(now),
			Until:  pauseUntil(c.RecordingPause, now),
		},
		Capture: captureJSON{
			Device: s.cfg.Capture.Device, Channel: s.cfg.Capture.Channel,
			Gain: s.cfg.Capture.Gain, SensitivityDBFS: s.cfg.Capture.SensitivityDBFS,
			Calibration: s.cfg.Capture.Calibration,
		},
		Levels: levelsJSON{
			UncertaintyDB: c.SensitivityUncertaintyDB, Source: c.SensitivitySource,
			MeasuredOn: c.SensitivityMeasuredOn, Reference: c.SensitivityReference,
		},
		Heartbeat: heartbeatJSON{
			Configured: c.HeartbeatURL != "",
			IntervalMS: c.HeartbeatInterval.Milliseconds(),
		},
		Auth: authJSON{Mode: c.AuthMode, Login: login, Name: name},
		Disk: []diskJSON{},
	}

	st := s.cfg.Stats()
	out.Pipeline = pipelineJSON{
		Chunks: st.Chunks, DroppedSamples: st.DroppedSamples, BinsDropped: st.BinsDropped,
		EventsDropped: st.EventsDropped, WriteFailures: st.WriteFailures, LoopRestarts: st.LoopRestarts,
	}

	var err error
	if out.DBBytes, out.WALBytes, err = s.cfg.Store.DBSize(); err != nil {
		s.log.Error("reading the size of the database failed", "err", err)
	}
	if out.SchemaVersion, err = s.cfg.Store.SchemaVersion(ctx); err != nil {
		s.log.Error("reading the schema version failed", "err", err)
	}

	counts, err := s.cfg.Store.CountsSince(ctx, s.dayStart(now).UnixMilli())
	if err != nil {
		s.serverError(w, "counting what has been recorded", err)
		return
	}
	out.Counts = countsJSON{EventsToday: counts.EventsSince, EventsTotal: counts.EventsTotal,
		Unreviewed: counts.Unreviewed, SamplesToday: counts.SamplesSince}

	// The stores can sit on different filesystems, and the owner needs to
	// know which one is filling up.
	stores := []struct{ name, path string }{
		{"database", filepath.Dir(c.DBPath)},
		{"clips", s.cfg.ClipDir},
	}
	if s.cfg.Camera != nil {
		stores = append(stores, struct{ name, path string }{"video", s.cfg.VideoDir})
	}
	for _, d := range stores {
		free, err := health.FreeMB(d.path)
		if err != nil {
			s.log.Error("reading free space failed", "store", d.name, "err", err)
			continue
		}
		out.Disk = append(out.Disk, diskJSON{Name: d.name, FreeMB: free})
	}
	out.Clips = s.usage(s.cfg.ClipDir, nil)
	out.Camera = s.cameraStatus(c)
	out.Tailscale = s.tailscaleStatus(ctx)
	out.Tailscale.HTTPAddr = c.HTTPAddr

	writeJSON(w, http.StatusOK, out)
}

// ringDirName is the directory under video_dir that holds the segment ring.
// It is skipped when the event clips are counted.
const ringDirName = "ring"

// cameraStatus copies the ring's state into the response and says how the
// camera is configured. The configuration is there with video off too: the
// owner reads it while bringing a camera up.
func (s *Server) cameraStatus(c config.Config) cameraJSON {
	out := cameraJSON{Config: s.cameraConfig(c, CameraStatus{})}
	if s.cfg.Camera == nil {
		return out
	}
	st := s.cfg.Camera()
	out = cameraJSON{
		Enabled: st.Enabled, Connected: st.Connected,
		UptimeMS: st.Uptime.Milliseconds(), Disconnects: st.Disconnects,
		Ring:   ringJSON{Segments: st.RingSegments, Bytes: st.RingBytes},
		Clips:  s.usage(s.cfg.VideoDir, []string{ringDirName}),
		Config: s.cameraConfig(c, st),
	}
	if !st.ConnectedSince.IsZero() {
		out.ConnectedSinceMS = st.ConnectedSince.UnixMilli()
	}
	if !st.LastSegment.IsZero() {
		out.LastSegmentMS = st.LastSegment.UnixMilli()
	}
	return out
}

// cameraConfig says how the camera is set up, without ever saying what the
// password is. st is the ring's state, or the zero value with video off.
func (s *Server) cameraConfig(c config.Config, st CameraStatus) cameraConfigJSON {
	out := cameraConfigJSON{
		Host: c.CameraHost, Port: c.CameraPort,
		SubPath: c.CameraRTSPPath, MainPath: c.CameraRTSPPathMain,
		SubPathSource:  pathConfigured,
		RingMinutes:    c.VideoRingMinutes,
		SegmentSeconds: c.VideoSegmentSeconds,
		Audio:          c.CameraAudio,
		FFmpeg:         s.cfg.FFmpegVersion,
		Credentials:    credentialsJSON{Source: video.FromNowhere},
	}
	if st.AudioKnown {
		out.AudioTrack = audioTrack(st.Audio)
	}
	if out.SubPath == "" {
		// Nothing is configured, so the ring is working through the
		// defaults. What it is using now is the answer, and it is re-tried
		// on every reconnect.
		out.SubPathSource, out.SubPath = pathDiscovered, st.SubPath
		if out.SubPath == "" {
			out.SubPathSource = pathNone
		}
	}
	if out.MainPath == "" {
		out.MainPath = video.MainPath(out.SubPath)
	}
	if s.cfg.CameraLogin != nil {
		login := s.cfg.CameraLogin()
		// Only the source and the user cross this line. Nothing reads
		// login.Creds.Pass on the way to a response.
		out.Credentials = credentialsJSON{
			Loaded: login.Loaded(), Source: login.Source, User: login.Creds.User,
		}
		if !login.Loaded() {
			out.Credentials.Source = video.FromNowhere
			out.Credentials.User = ""
		}
	}
	return out
}

// usage counts the files under root and what they take up, skipping the
// named top-level directories. A directory it cannot read is logged and
// skipped: the system page must still answer.
func (s *Server) usage(root string, skip []string) clipsJSON {
	var out clipsJSON
	if root == "" {
		return out
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && filepath.Dir(path) == root && slices.Contains(skip, d.Name()) {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out.Count++
		out.Bytes += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		s.log.Error("measuring the clips failed", "err", err)
	}
	return out
}

// tailscaleCacheFor is how long one reading stands. Shelling out on every
// request would make the system page slow and hammer the tailscaled socket.
// The owner reloading the screen gets the cached answer until it is stale.
const tailscaleCacheFor = time.Minute

// tailscaleStatus reads what Tailscale says, at most once a minute. It
// never fails: an answer that could not be read is a block with err set.
func (s *Server) tailscaleStatus(ctx context.Context) tailscaleJSON {
	if s.cfg.Tailscale == nil {
		return tailscaleJSON{}
	}
	now := s.now()
	s.tsMu.Lock()
	defer s.tsMu.Unlock()
	if !s.tsRead.IsZero() && now.Sub(s.tsRead) < tailscaleCacheFor {
		return s.tsCached
	}
	st := s.cfg.Tailscale(ctx)
	out := tailscaleJSON{
		Installed: st.Installed, Running: st.Running, Backend: st.Backend,
		Name: st.Name, Serving: st.Serving, ServeURL: st.ServeURL,
		Funnel: st.Funnel, Err: st.Err,
	}
	if !st.KeyExpiry.IsZero() {
		out.KeyExpiryMS = st.KeyExpiry.UnixMilli()
	}
	if ctx.Err() != nil {
		// The browser went away mid-read, so this answer says nothing about
		// Tailscale. Caching it would keep that non-answer for a minute.
		return out
	}
	s.tsCached, s.tsRead = out, now
	return out
}
