package web

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/minayousseif/stompwatch/internal/store"
)

// Around an event the interface draws the half minute either side, so the
// owner can see what the noise came out of.
const eventContext = 30 * time.Second

// maxNote is as long a note as the review form accepts. It is a reminder to
// the owner, not a document.
const maxNote = 2000

// maxBody is the largest request body any endpoint here reads.
const maxBody = 64 << 10

var (
	eventClasses = []string{"running", "jumping", "stomping", "impact", "steady", "airborne", "unknown"}
	reviewStates = []string{"verified", "rejected", "unsure"}
	filterStates = []string{"verified", "rejected", "unsure", "none"}
	// An export may not ask for unreviewed events. The claim it makes is
	// that a person looked at every event in it.
	exportStates  = []string{"verified", "rejected", "unsure"}
	eventSorts    = []string{"started_ms", "lamax", "duration_ms", "class"}
	sortDirection = []string{"desc", "asc"}
)

type reviewJSON struct {
	Status     string `json:"status"`
	Note       string `json:"note"`
	Reviewer   string `json:"reviewer"`
	ReviewedMS int64  `json:"reviewed_ms"`
}

type eventJSON struct {
	ID                int64   `json:"id"`
	StartedMS         int64   `json:"started_ms"`
	EndedMS           int64   `json:"ended_ms"`
	DurationMS        int64   `json:"duration_ms"`
	LAeq              float64 `json:"laeq"`
	LAmax             float64 `json:"lamax"`
	BaselineAtTrigger float64 `json:"baseline_at_trigger"`
	LowEnergy         float64 `json:"low_energy"`
	HighEnergy        float64 `json:"high_energy"`
	LowHighRatio      float64 `json:"low_high_ratio"`
	Class             string  `json:"class"`
	Confidence        float64 `json:"confidence"`
	Forced            bool    `json:"forced"`
	// JumpDB and RiseDB are null for events recorded before schema 7 and
	// for events with less than 10 s of levels before them.
	JumpDB   *float64    `json:"jump_db"`
	RiseDB   *float64    `json:"rise_db"`
	HasAudio bool        `json:"has_audio"`
	HasVideo bool        `json:"has_video"`
	Muted    bool        `json:"muted"`
	Review   *reviewJSON `json:"review"`
}

type mediaJSON struct {
	Kind       string `json:"kind"`
	Bytes      int64  `json:"bytes"`
	DurationMS int64  `json:"duration_ms"`
	SHA256     string `json:"sha256"`
	// StartMS is the time of the clip's first sample, and MissingHeadMS is
	// how much of the requested pre-roll the clip does not have. Both are
	// null for a clip stored before the collector recorded its start.
	StartMS       *int64 `json:"start_ms"`
	MissingHeadMS *int64 `json:"missing_head_ms"`
	// Clipped counts samples that hit the 16-bit limit. Anything above zero
	// means the clip is distorted at those moments, and the screen should
	// say so rather than leave it in the health log.
	Clipped int `json:"clipped"`
	// RateHz is the rate of the stored file. The clip filter cutoff is half
	// it, so this is how the interface says what a clip holds instead of
	// assuming 1 kHz. A clip keeps the rate it was recorded at, so a
	// database holds clips of more than one (SPEC.md section 15 decision 18).
	RateHz *int `json:"rate_hz"`
	// PeakDBFS is the loudest sample of the stored clip, and PlaybackGainDB
	// is how much the playback stream is raised above it. Both are null
	// when the file is gone. The stored file is never changed.
	PeakDBFS       *float64 `json:"peak_dbfs"`
	PlaybackGainDB *float64 `json:"playback_gain_db"`
	Truncated      bool     `json:"truncated"`
	// CameraAudio is true when a video clip really carries the camera's own
	// audio track, unfiltered. The writer read the file back after it wrote
	// it, so this is the clip's own answer and not the setting in force now,
	// nor the setting in force when it was cut. It is always false for an
	// audio clip, which is the measuring microphone's and is filtered
	// (SPEC.md section 15 decision 22).
	CameraAudio bool `json:"camera_audio"`
	// Missing is true when the file has vanished: the row is here, no purge
	// was recorded, and the file is not on disk. That is a fault. A purged
	// clip has Missing false, because the owner deleted it on purpose.
	Missing bool `json:"missing"`
	// PurgedMS and PurgedBy say the file was deleted deliberately, when and
	// by whom. Both are null for a clip that was never purged. Bytes,
	// DurationMS and SHA256 above keep the values the collector recorded:
	// the row is the record that the clip existed and what it was.
	PurgedMS *int64  `json:"purged_ms"`
	PurgedBy *string `json:"purged_by"`
}

type envelopeJSON struct {
	RateHz  int       `json:"rate_hz"`
	StartMS int64     `json:"start_ms"`
	Values  []float64 `json:"values"`
}

// captureSettingsJSON is the capture and calibration settings in force when
// an event was recorded. It is read from the capture-settings history, never
// from what is in force now: an event reprinted three months later must not
// claim today's calibration (SPEC.md section 15 decision 23).
type captureSettingsJSON struct {
	// FromMS is when these settings came into force.
	FromMS          int64   `json:"from_ms"`
	SensitivityDBFS float64 `json:"sensitivity_dbfs"`
	// UncertaintyDB is the plus or minus on every level of this event.
	UncertaintyDB float64 `json:"uncertainty_db"`
	Source        string  `json:"source"`
	MeasuredOn    string  `json:"measured_on"`
	Reference     string  `json:"reference"`
	// Calibration is the base name of the calibration file, never its path,
	// and empty when none was configured.
	Calibration         string  `json:"calibration"`
	CalibrationOffsetDB float64 `json:"calibration_offset_db"`
	Device              string  `json:"device"`
	Channel             int     `json:"channel"`
}

type eventDetailJSON struct {
	eventJSON
	Media    []mediaJSON   `json:"media"`
	Envelope *envelopeJSON `json:"envelope"`
	Samples  []pointJSON   `json:"samples"`
	// Capture is the settings in force when this event was recorded, or null
	// when the history does not reach back that far. Null is the honest
	// answer for every event recorded before the history existed, and the
	// interface must say so rather than fall back to today's settings.
	Capture *captureSettingsJSON `json:"capture"`
}

type eventListJSON struct {
	Total  int         `json:"total"`
	Events []eventJSON `json:"events"`
}

func toEventJSON(e store.EventRow) eventJSON {
	out := eventJSON{
		ID: e.ID, StartedMS: e.StartedMS, EndedMS: e.EndedMS, DurationMS: e.DurationMS,
		LAeq: e.LAeq, LAmax: e.LAmax, BaselineAtTrigger: e.BaselineAtTrigger,
		LowEnergy: e.LowEnergy, HighEnergy: e.HighEnergy, LowHighRatio: e.LowHighRatio,
		Class: e.Class, Confidence: e.Confidence, Forced: e.Forced,
		HasAudio: e.HasAudio, HasVideo: e.HasVideo, Muted: e.Muted,
	}
	if e.JumpDB.Valid {
		v := e.JumpDB.Float64
		out.JumpDB = &v
	}
	if e.RiseDB.Valid {
		v := e.RiseDB.Float64
		out.RiseDB = &v
	}
	if e.Review != nil {
		out.Review = toReviewJSON(*e.Review)
	}
	return out
}

func toReviewJSON(r store.Review) *reviewJSON {
	return &reviewJSON{Status: r.Status, Note: r.Note, Reviewer: r.Reviewer,
		ReviewedMS: r.At.UnixMilli()}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r, "from", "to", "class", "status", "min_lamax", "max_lamax",
		"min_duration_ms", "quiet_only", "muted", "q", "sort", "order", "limit", "offset")
	f := store.EventFilter{
		FromMS:        p.ms("from"),
		ToMS:          p.ms("to"),
		Classes:       p.many("class", eventClasses...),
		Statuses:      p.many("status", filterStates...),
		MinLAmax:      p.float("min_lamax"),
		MaxLAmax:      p.float("max_lamax"),
		MinDurationMS: p.int64AtLeast("min_duration_ms", 0, 0),
		Note:          p.text("q"),
		Sort:          p.one("sort", "started_ms", eventSorts...),
		Desc:          p.one("order", "desc", sortDirection...) == "desc",
		Limit:         p.intIn("limit", 100, 1, 500),
		Offset:        p.intIn("offset", 0, 0, math.MaxInt32),
	}
	quietOnly := p.flag("quiet_only")
	// An absent muted parameter keeps every event. Only a given one narrows
	// the list, to one side of the mark or the other.
	if p.has("muted") {
		muted := p.flag("muted")
		f.Muted = &muted
	}
	if !p.ok(w) {
		return
	}
	if f.ToMS != 0 && f.FromMS != 0 && f.ToMS < f.FromMS {
		fail(w, http.StatusBadRequest, "to is before from. Give the range the other way round.")
		return
	}
	if quietOnly {
		// With a side left open the store fills it from the events, and
		// Spans stops at its own limit.
		if f.FromMS != 0 && f.ToMS != 0 && !quietRangeFits(w, f.FromMS, f.ToMS) {
			return
		}
		q := s.cfg.Settings.Current().Quiet
		f.Quiet, f.Loc = &q, s.loc
	}

	rows, total, err := s.cfg.Store.ListEvents(r.Context(), f)
	if err != nil {
		s.serverError(w, "listing events", err)
		return
	}
	out := eventListJSON{Total: total, Events: make([]eventJSON, len(rows))}
	for i, e := range rows {
		out.Events[i] = toEventJSON(e)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the event number must be a whole number above zero.")
		return
	}
	ctx := r.Context()
	e, err := s.cfg.Store.Event(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.noSuchEvent(w)
		return
	}
	if err != nil {
		s.serverError(w, "reading an event", err)
		return
	}

	out := eventDetailJSON{eventJSON: toEventJSON(e), Media: []mediaJSON{}, Samples: []pointJSON{}}

	media, err := s.cfg.Store.EventMedia(ctx, id)
	if err != nil {
		s.serverError(w, "reading the clips of an event", err)
		return
	}
	preRoll := s.cfg.Settings.Current().PreRoll
	for _, m := range media {
		out.Media = append(out.Media, s.toMediaJSON(m, e.StartedMS, preRoll))
	}

	env, err := s.cfg.Store.EventEnvelope(ctx, id)
	if err != nil {
		s.serverError(w, "reading the envelope of an event", err)
		return
	}
	if env == nil {
		env = []float64{}
	}
	out.Envelope = &envelopeJSON{RateHz: envelopeRateHz, StartMS: e.StartedMS, Values: env}

	samples, err := s.cfg.Store.Samples(ctx,
		e.StartedMS-eventContext.Milliseconds(), e.EndedMS+eventContext.Milliseconds())
	if err != nil {
		s.serverError(w, "reading the seconds around an event", err)
		return
	}
	for _, pt := range samples {
		out.Samples = append(out.Samples, pointJSON{T: pt.TMS, LAeq: pt.LAeq, LAmax: pt.LAmax, Baseline: pt.Baseline})
	}

	// The settings this event was measured under. ErrNotFound means the
	// history does not reach back to it, which is true of every event
	// recorded before the history existed. The block stays null then: a
	// fallback to the settings in force now is exactly the claim this
	// endpoint must not make.
	cs, err := s.cfg.Store.CaptureSettingsAt(ctx, e.StartedMS)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		s.serverError(w, "reading the capture settings of an event", err)
		return
	default:
		out.Capture = toCaptureSettingsJSON(cs)
	}
	writeJSON(w, http.StatusOK, out)
}

func toCaptureSettingsJSON(c store.CaptureSettings) *captureSettingsJSON {
	return &captureSettingsJSON{
		FromMS: c.From.UnixMilli(), SensitivityDBFS: c.SensitivityDBFS,
		UncertaintyDB: c.UncertaintyDB, Source: c.Source,
		MeasuredOn: c.MeasuredOn, Reference: c.Reference,
		Calibration: c.CalibrationFile, CalibrationOffsetDB: c.CalibrationOffsetDB,
		Device: c.CaptureDevice, Channel: c.CaptureChannel,
	}
}

// envelopeRateHz is the rate the detector stores the envelope at.
const envelopeRateHz = 100

// toMediaJSON describes one clip. start_ms and missing_head_ms are null when
// the clip's start was never recorded: the interface must be able to tell
// "unknown" from a real time, and a shortfall worked out from a guessed
// start would be a number nobody measured.
func (s *Server) toMediaJSON(m store.Media, eventStartMS int64, preRoll time.Duration) mediaJSON {
	out := mediaJSON{
		Kind: m.Kind, Bytes: m.Bytes, DurationMS: m.Duration.Milliseconds(),
		SHA256: m.SHA256, Clipped: m.Clipped, Truncated: m.Truncated,
		CameraAudio: m.CameraAudio,
		Missing:     s.mediaMissing(m),
	}
	if m.Purged != nil {
		purgedMS, purgedBy := m.Purged.At.UnixMilli(), m.Purged.By
		out.PurgedMS, out.PurgedBy = &purgedMS, &purgedBy
	}
	if !out.Missing && m.Purged == nil && m.Kind == store.KindAudio {
		if rate, peak, gain, err := s.clipLevels(m); err == nil {
			out.RateHz, out.PeakDBFS, out.PlaybackGainDB = &rate, &peak, &gain
		} else {
			s.log.Warn("cannot read the level of a clip", "event", m.EventID, "err", err)
		}
	}
	if m.Started.IsZero() {
		return out
	}
	startMS := m.Started.UnixMilli()
	// The clip was asked to begin one pre-roll before the event. What it does
	// not have is the gap between that instant and its first sample. The
	// truncated flag says a clip is short; this says by how much.
	missing := startMS - (eventStartMS - preRoll.Milliseconds())
	if missing < 0 {
		missing = 0
	}
	out.StartMS, out.MissingHeadMS = &startMS, &missing
	return out
}

// clipLevels reads the stored clip and returns its rate, its peak in dBFS,
// and the gain playback applies. It reads the file rather than the database
// because all three are properties of the audio, and the audio is the thing
// that cannot be wrong. The rate matters on its own: the clip filter cutoff
// is half it, and a database holds clips of more than one rate (SPEC.md section 15
// decision 18).
func (s *Server) clipLevels(m store.Media) (rate int, peakDB, gainDB float64, err error) {
	path, err := resolveMedia(s.cfg.ClipDir, m.Path)
	if err != nil {
		return 0, 0, 0, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, err
	}
	rate, samples, err := decodeWAV(raw)
	if err != nil {
		return 0, 0, 0, err
	}
	// The peak is the stored clip's own, which is the honest number. The
	// gain is what the default playback actually applies, which is worked
	// out from the finished signal, so the two are not simply related.
	peakDB = peakDBFS(samples)
	_, gainDB = buildPlayback(samples, rate, "tilt")
	if math.IsInf(peakDB, -1) {
		// Digital silence has no peak to report. Say so rather than send
		// negative infinity, which is not a number in JSON.
		peakDB = -math.MaxFloat64
	}
	return rate, peakDB, gainDB, nil
}

// mediaMissing reports whether the file a media row names has vanished, and
// records it once per event per run. A clip that has vanished is exactly the
// kind of silent problem that should surface loudly.
//
// A purged clip is not missing. The owner deleted it on purpose and the
// purge is already recorded, so reporting it here would call their own
// decision a fault and fill the health log with alarms about it.
func (s *Server) mediaMissing(m store.Media) bool {
	if m.Purged != nil {
		return false
	}
	path, err := resolveMedia(s.mediaRoot(m.Kind), m.Path)
	if err != nil {
		// A path that may not be read is not a missing file. The handler
		// that serves it reports the refusal.
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return false
	}
	s.mu.Lock()
	first := !s.reported[m.EventID]
	if first {
		s.reported[m.EventID] = true
	}
	s.mu.Unlock()
	if first {
		s.log.Error("a clip named by the database is not on disk", "event", m.EventID, "kind", m.Kind)
		s.cfg.RecordHealth(s.now(), store.HealthMediaMissing,
			"event "+itoa64(m.EventID)+": the "+m.Kind+" file is missing", 0)
	}
	return true
}

func (s *Server) noSuchEvent(w http.ResponseWriter) {
	fail(w, http.StatusNotFound, "there is no event with that number.")
}

// --- review ---

func (s *Server) handlePutReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the event number must be a whole number above zero.")
		return
	}
	var body struct {
		Status string  `json:"status"`
		Note   *string `json:"note"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !slices.Contains(reviewStates, body.Status) {
		fail(w, http.StatusBadRequest, "status must be verified, rejected, or unsure.")
		return
	}
	note := ""
	if body.Note != nil {
		note = *body.Note
	}
	if len([]rune(note)) > maxNote {
		fail(w, http.StatusBadRequest, "the note is too long. Keep it under 2000 characters.")
		return
	}

	ctx := r.Context()
	if _, err := s.cfg.Store.Event(ctx, id); errors.Is(err, store.ErrNotFound) {
		s.noSuchEvent(w)
		return
	} else if err != nil {
		s.serverError(w, "reading an event before saving a review", err)
		return
	}

	// The reviewer is who the network says is calling, never what the body
	// claims. The audit trail is only worth having if it cannot be set by
	// the thing being audited.
	login, _ := Identity(r)
	rec := store.Review{EventID: id, Status: body.Status, Note: note, Reviewer: login, At: s.now()}
	if err := s.cfg.Store.SetReview(ctx, rec); err != nil {
		s.serverError(w, "saving a review", err)
		return
	}
	writeJSON(w, http.StatusOK, toReviewJSON(rec))
}

func (s *Server) handleDeleteReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		fail(w, http.StatusBadRequest, "the event number must be a whole number above zero.")
		return
	}
	ctx := r.Context()
	if _, err := s.cfg.Store.Event(ctx, id); errors.Is(err, store.ErrNotFound) {
		s.noSuchEvent(w)
		return
	} else if err != nil {
		s.serverError(w, "reading an event before removing a review", err)
		return
	}
	if err := s.cfg.Store.DeleteReview(ctx, id); err != nil {
		s.serverError(w, "removing a review", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readJSON decodes a request body and reports a bad one to the caller.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !isJSON(w, r) {
		return false
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "the request body is not the JSON this address expects.")
		return false
	}
	return true
}

// isJSON refuses a body that does not say it is JSON. A form, or a fetch in
// no-cors mode, may send text/plain to another site without asking first;
// application/json it may not. The check is the second guard behind
// sameOrigin.
func isJSON(w http.ResponseWriter, r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		fail(w, http.StatusUnsupportedMediaType,
			"the request body must be JSON, sent with Content-Type: application/json.")
		return false
	}
	return true
}

// itoa64 writes an event number into a health record's detail.
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }
