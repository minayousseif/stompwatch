package web

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/minayousseif/stompwatch/internal/config"
	"github.com/minayousseif/stompwatch/internal/settings"
	"github.com/minayousseif/stompwatch/internal/store"
)

type settingJSON struct {
	Key string `json:"key"`
	// Value is the text the config file would carry. ValueNum is the same
	// value counted in the unit Min and Max use, so a form can range-check
	// it without parsing Go duration text. It is null for a clock time,
	// which is not one number.
	Value      string   `json:"value"`
	ValueNum   *float64 `json:"value_num"`
	Default    string   `json:"default"`
	DefaultNum *float64 `json:"default_num"`
	Source     string   `json:"source"`
	Type       string   `json:"type"`
	Min        float64  `json:"min"`
	Max        float64  `json:"max"`
	Label      string   `json:"label"`
	Unit       string   `json:"unit"`
	Live       bool     `json:"live"`
	// Note is one sentence the interface must show with the field, or empty.
	// It comes from settings.Editable, so the warning on camera_audio cannot
	// be lost by an interface that only draws inputs.
	Note string `json:"note"`
}

type settingsJSON struct {
	Settings []settingJSON `json:"settings"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	p := parseQuery(r)
	if !p.ok(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.settingsBody())
}

// settingsBody lists the editable settings, in display order.
func (s *Server) settingsBody() settingsJSON {
	current, file := s.cfg.Settings.Current(), s.cfg.Settings.File()
	overrides := s.cfg.Settings.Overrides()

	out := settingsJSON{Settings: make([]settingJSON, 0, len(settings.Editable))}
	for _, f := range settings.Editable {
		source := "file"
		if _, ok := overrides[f.Key]; ok {
			source = "db"
		}
		out.Settings = append(out.Settings, settingJSON{
			Key:      f.Key,
			Value:    settingValue(f.Key, current),
			ValueNum: settingNumber(f, current),
			// The default is what the config file says, which is what comes
			// back when the database row is removed.
			Default:    settingValue(f.Key, file),
			DefaultNum: settingNumber(f, file),
			Source:     source,
			Type:       f.Type, Min: f.Min, Max: f.Max,
			Label: f.Label, Unit: f.Unit, Live: f.Live, Note: f.Note,
		})
	}
	return out
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if !isJSON(w, r) {
		return
	}
	var changes map[string]*string
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	if err := dec.Decode(&changes); err != nil {
		fail(w, http.StatusBadRequest,
			"the request body must be an object of settings. Each value is text, or null to go back to the file default.")
		return
	}

	// Apply validates the whole merged result first. Nothing reaches the
	// disk until it returns.
	before := s.cfg.Settings.Current()
	if err := s.cfg.Settings.Apply(changes); err != nil {
		fail(w, http.StatusBadRequest, plainError(err))
		return
	}
	// The change is in force from here, saved or not, so it is recorded
	// before the save is tried.
	s.recordSettingsChange(r, changes, before, s.cfg.Settings.Current())
	if err := s.cfg.Store.PutSettings(r.Context(), changes, s.now()); err != nil {
		s.log.Error("saving settings failed", "err", err)
		fail(w, http.StatusInternalServerError,
			"the change is in force now, but it could not be saved. It will be lost if the collector restarts.")
		return
	}
	writeJSON(w, http.StatusOK, s.settingsBody())
}

// recordSettingsChange logs each setting whose value changed, with the old
// and the new value, and writes one system_health row that names the login
// and every change. A key sent with the value it already had changed
// nothing and is left out; a request that changed nothing writes no row.
func (s *Server) recordSettingsChange(r *http.Request, changes map[string]*string, before, after config.Config) {
	login, _ := Identity(r)
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(changes)) {
		old, now := settingValue(k, before), settingValue(k, after)
		if old == now {
			continue
		}
		s.log.Info("setting changed", "by", login, "key", k, "old", old, "new", now)
		parts = append(parts, k+": "+old+" -> "+now)
	}
	if len(parts) == 0 {
		return
	}
	s.cfg.RecordHealth(s.now(), store.HealthSettingsChanged,
		login+" changed "+strings.Join(parts, "; "), 0)
}

// plainError drops the package prefix from a validation message, which is
// already written for a person to read.
func plainError(err error) string {
	msg := err.Error()
	for _, prefix := range []string{"settings: ", "config: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// settingValue reads one editable setting out of a parsed Config, as the
// text the config file would carry. It is the other half of config.Set, and
// TestSettingValuesSurviveARoundTrip holds the two together.
func settingValue(key string, c config.Config) string {
	switch key {
	case "threshold_db":
		return number(c.ThresholdDB)
	case "min_duration_ms":
		return units(c.MinDuration, time.Millisecond)
	case "hangover_ms":
		return units(c.Hangover, time.Millisecond)
	case "cooldown_ms":
		return units(c.Cooldown, time.Millisecond)
	case "max_event_s":
		return units(c.MaxEvent, time.Second)
	case "min_baseline_s":
		return units(c.MinBaseline, time.Second)
	case "pre_roll_s":
		return units(c.PreRoll, time.Second)
	case "post_roll_s":
		return units(c.PostRoll, time.Second)
	case "baseline_window":
		return c.BaselineWindow.String()
	case "baseline_percentile":
		return number(c.BaselinePercentile)
	case "quiet_start":
		return clock(c.Quiet.Start)
	case "quiet_end":
		return clock(c.Quiet.End)
	case "recording_pause":
		// Empty is a value here too: it means no pause.
		return c.RecordingPause.String()
	case "batch_interval":
		return c.BatchInterval.String()
	case "disk_warn_free_mb":
		return strconv.FormatInt(c.DiskWarnFreeMB, 10)
	case "retention_days":
		return strconv.Itoa(c.RetentionDays)
	case "camera_rtsp_path":
		// An empty path is a value, not a missing one: it means "try the
		// default paths in order" (SPEC.md section 7).
		return c.CameraRTSPPath
	case "camera_rtsp_path_main":
		return c.CameraRTSPPathMain
	case "video_ring_minutes":
		return strconv.Itoa(c.VideoRingMinutes)
	case "video_segment_seconds":
		return strconv.Itoa(c.VideoSegmentSeconds)
	case "camera_audio":
		return boolText(c.CameraAudio)
	}
	return ""
}

// settingNumber reads a setting as the number Min and Max are counted in.
// A duration key such as baseline_window is written as "10m0s" but ranged
// in seconds, so the two must not be read from the same text.
func settingNumber(f settings.Field, c config.Config) *float64 {
	text := settingValue(f.Key, c)
	switch f.Type {
	case "duration":
		d, err := time.ParseDuration(text)
		if err != nil {
			return nil
		}
		v := d.Seconds()
		return &v
	case "float", "int":
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil
		}
		return &v
	}
	// Everything else has no single number to range-check: a clock time is
	// an hour and a minute, and "text" and "bool" are not quantities at
	// all. All three send null.
	return nil
}

func number(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// boolText writes a yes-or-no the way the config file does. Nothing else is
// accepted there, so nothing else may be written here.
func boolText(v bool) string { return strconv.FormatBool(v) }

// units writes a duration as a whole number of unit, for keys such as
// hangover_ms.
func units(d, unit time.Duration) string { return strconv.FormatInt(int64(d/unit), 10) }

// clock writes an offset from midnight as HH:MM.
func clock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
}
