package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/minayousseif/stompwatch/internal/video"
)

// probeDeadline bounds one camera test. It is shorter than the probe's own
// total patience, which is one timeout per path plus the clock reading, so
// the request answers with a sentence rather than being cut off by the
// 30-second deadline every request has.
const probeDeadline = 25 * time.Second

// attemptJSON is one stream the test tried. Detail is ffmpeg's own words
// for a failure and the negotiated stream for a success, and it has been
// through Stream.Redact either way.
type attemptJSON struct {
	Path   string `json:"path"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// audioTrackJSON says whether the camera's stream carries an audio track,
// with the codec and sample rate when it does. Known is false when nothing
// has asked the camera yet, and then Present is false because nothing was
// measured, not because the camera is silent. Many cameras send no audio,
// and then camera_audio does nothing (SPEC.md section 15 decision 22).
type audioTrackJSON struct {
	Known   bool   `json:"known"`
	Present bool   `json:"present"`
	Codec   string `json:"codec"`
	RateHz  int    `json:"rate_hz"`
}

// audioTrack describes the audio of a stream that answered. It is the one
// place the video package's reading becomes a response, so the system page
// and the camera test can never disagree about what a camera sends.
func audioTrack(a video.AudioInfo) audioTrackJSON {
	return audioTrackJSON{Known: true, Present: a.Present, Codec: a.Codec, RateHz: a.RateHz}
}

type probeJSON struct {
	Tried []attemptJSON `json:"tried"`
	// SubPath and MainPath are the paths that answered, or empty when none
	// did. The owner applies them through the settings form; this endpoint
	// changes nothing.
	SubPath  string `json:"sub_path"`
	MainPath string `json:"main_path"`
	// ClockDriftMS is the camera clock minus the host clock. It is zero
	// when ClockReadable is false, rather than a number nobody measured.
	ClockDriftMS  int64 `json:"clock_drift_ms"`
	ClockReadable bool  `json:"clock_readable"`
	// Audio is the audio track of the sub-stream that answered. It is not
	// known when none answered.
	Audio audioTrackJSON `json:"audio"`
}

// handleProbeCamera tries each RTSP path on the camera and reports what
// works. It is the dashboard's form of stompwatch probe-camera and runs the
// same code (SPEC.md section 7).
//
// It changes no setting. A test that quietly wrote what it found would take
// the decision away from the owner, and a discovered path is exactly the
// thing they should look at before saving.
func (s *Server) handleProbeCamera(w http.ResponseWriter, r *http.Request) {
	if p := parseQuery(r); !p.ok(w) {
		return
	}
	c := s.cfg.Settings.Current()
	if c.CameraHost == "" {
		fail(w, http.StatusBadRequest,
			"there is no camera address to test. Set camera_host in the config file, restart the collector, and try again.")
		return
	}
	if s.cfg.CameraLogin == nil {
		fail(w, http.StatusBadRequest,
			"this collector was started without looking for a camera login, so it cannot open a stream.")
		return
	}
	login := s.cfg.CameraLogin()
	if !login.Loaded() {
		fail(w, http.StatusBadRequest,
			"no camera login is loaded, so every stream would answer 401. Put CAMERA_USER and CAMERA_PASS "+
				"in the credentials file or the environment, restart the collector, and try again.")
		return
	}
	if err := video.CheckFFmpeg(s.cfg.FFmpeg); err != nil {
		fail(w, http.StatusBadRequest,
			"ffmpeg is not installed, so nothing can open the stream. Install it with: apt install ffmpeg")
		return
	}

	// One test at a time. Each one makes the box open connections to the
	// camera and start ffmpeg, and two sets of those help nobody.
	if !s.probing.CompareAndSwap(false, true) {
		fail(w, http.StatusConflict,
			"a camera test is already running. Wait for it to finish, then try again.")
		return
	}
	defer s.probing.Store(false)

	ctx, cancel := context.WithTimeout(r.Context(), s.probeDeadline)
	defer cancel()

	base := video.Stream{Host: c.CameraHost, Port: c.CameraPort, Creds: login.Creds}
	rep := video.ProbeCamera(ctx, s.cfg.FFmpeg, s.cfg.FFmpegEnv, base,
		video.CandidatePaths(c.CameraRTSPPath), c.CameraRTSPPathMain)

	if errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Context().Err() == nil {
		fail(w, http.StatusGatewayTimeout,
			"the camera did not answer in time, so the test was given up. Check that the camera is on "+
				"and that the address and port are right, then try again.")
		return
	}

	out := probeJSON{Tried: []attemptJSON{}, SubPath: subPathOf(rep), MainPath: mainPathOf(rep)}
	if rep.Sub.Working != nil {
		out.Audio = audioTrack(rep.Sub.Working.Info.Audio)
	}
	for _, a := range append(append([]video.Attempt{}, rep.Sub.Tried...), rep.Main.Tried...) {
		out.Tried = append(out.Tried, attempt(a))
	}
	if rep.ClockErr == nil {
		out.ClockReadable = true
		out.ClockDriftMS = rep.ClockOffset.Milliseconds()
	}
	login_, _ := Identity(r)
	s.log.Info("the camera was tested from the dashboard", "by", login_,
		"host", c.CameraHost, "sub_path", out.SubPath, "audio_track", out.Audio.Present)
	writeJSON(w, http.StatusOK, out)
}

// attempt describes one stream the test tried. Every string goes through
// Redact, because ffmpeg repeats the URL it was given, password and all.
func attempt(a video.Attempt) attemptJSON {
	out := attemptJSON{Path: a.Stream.Path, OK: a.Err == nil}
	if a.Err != nil {
		out.Detail = a.Stream.Redact(a.Err.Error())
		return out
	}
	out.Detail = a.Stream.Redact(a.Info.String())
	return out
}

func subPathOf(rep video.CameraReport) string {
	if rep.Sub.Working == nil {
		return ""
	}
	return rep.Sub.Working.Stream.Path
}

func mainPathOf(rep video.CameraReport) string {
	if rep.Main.Working == nil {
		return ""
	}
	return rep.Main.Working.Stream.Path
}
