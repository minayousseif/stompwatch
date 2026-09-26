package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// purgeJSON is a purge request body for one event, written out by hand so
// the test does not lean on the code under test to build it.
func purgeJSON(id int64) string {
	return `{"event_ids":[` + itoa(id) + `],"confirm":true}`
}

// send sends a body with exactly the headers given and no others, so a test
// states what the browser (or curl) put on the request.
func (e *env) send(method, target, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, req)
	return w
}

// onDisk fails the test when a file is missing, or present, against want.
func onDisk(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if got := err == nil; got != want {
		t.Errorf("%s on disk = %v, want %v (%v)", filepath.Base(path), got, want, err)
	}
}

// A page on another site can POST to the dashboard, and `tailscale serve`
// then adds the owner's identity to it. Such a request must delete nothing.
// The body and its Content-Type are right, so only the origin can refuse it.
func TestCrossSitePostIsRefusedAndDeletesNothing(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, videoPath := e.clipWithVideo(videoDir, 0)

	w := e.send(http.MethodPost, purgeURL, purgeJSON(id),
		"Content-Type", "application/json",
		"Sec-Fetch-Site", "cross-site",
		"Origin", "https://evil.example",
		"Tailscale-User-Login", "alex@example.com")
	wantError(t, w, http.StatusForbidden, "another web site")
	onDisk(t, audioPath, true)
	onDisk(t, videoPath, true)
}

// A browser from before 2023 sends no Sec-Fetch-Site. The Origin header
// still names the other site.
func TestCrossSitePostFromAnOldBrowserIsRefused(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, _ := e.clipWithVideo(videoDir, 0)

	w := e.send(http.MethodPost, purgeURL, purgeJSON(id),
		"Content-Type", "application/json",
		"Origin", "https://evil.example",
		"Tailscale-User-Login", "alex@example.com")
	wantError(t, w, http.StatusForbidden, "another web site")
	onDisk(t, audioPath, true)
}

// The dashboard's own page must still work. httptest sends Host
// example.com, which the Origin matches.
func TestSameOriginPostStillPurges(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, videoPath := e.clipWithVideo(videoDir, 0)

	w := e.send(http.MethodPost, purgeURL, purgeJSON(id),
		"Content-Type", "application/json",
		"Sec-Fetch-Site", "same-origin",
		"Origin", "http://example.com",
		"Tailscale-User-Login", "alex@example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin POST purge = %d, want 200: %s", w.Code, w.Body.String())
	}
	onDisk(t, audioPath, false)
	onDisk(t, videoPath, false)
}

// curl and scripts send neither header. They are not a browser, so no other
// site can drive them, and they must keep working.
func TestPostWithNoBrowserHeadersStillPurges(t *testing.T) {
	e, videoDir := videoEnv(t, nil)
	id, audioPath, _ := e.clipWithVideo(videoDir, 0)

	w := e.send(http.MethodPost, purgeURL, purgeJSON(id),
		"Content-Type", "application/json; charset=utf-8",
		"Tailscale-User-Login", "alex@example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("curl-like POST purge = %d, want 200: %s", w.Code, w.Body.String())
	}
	onDisk(t, audioPath, false)
}

// A form or a no-cors fetch can send text/plain without asking first. The
// body is valid JSON, and the request comes from nowhere in particular, so
// only the Content-Type can refuse it.
func TestPostThatIsNotJSONIs415(t *testing.T) {
	for _, ct := range []string{"text/plain", "text/plain;charset=UTF-8",
		"application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		name := ct
		if name == "" {
			name = "no Content-Type"
		}
		t.Run(name, func(t *testing.T) {
			e, videoDir := videoEnv(t, nil)
			id, audioPath, _ := e.clipWithVideo(videoDir, 0)

			headers := []string{"Tailscale-User-Login", "alex@example.com"}
			if ct != "" {
				headers = append(headers, "Content-Type", ct)
			}
			w := e.send(http.MethodPost, purgeURL, purgeJSON(id), headers...)
			wantError(t, w, http.StatusUnsupportedMediaType, "application/json")
			onDisk(t, audioPath, true)
		})
	}
}

// The settings body is read by its own decoder, so it needs the same check.
func TestSettingsPutThatIsNotJSONIs415(t *testing.T) {
	e := newEnv(t)
	before := e.live.Current().ThresholdDB
	w := e.send(http.MethodPut, "/api/settings", `{"threshold_db":"19"}`,
		"Content-Type", "text/plain",
		"Tailscale-User-Login", "alex@example.com")
	wantError(t, w, http.StatusUnsupportedMediaType, "application/json")
	if got := e.live.Current().ThresholdDB; got != before || got == 19 {
		t.Errorf("threshold_db = %v after a refused request, want it unchanged", got)
	}
}

// No other site may show the dashboard in a frame, where a click on its
// page could land on a delete button. Every answer says so, the page, the
// API, and a refusal alike.
func TestEveryResponseForbidsFraming(t *testing.T) {
	e := newEnv(t)
	cases := map[string]*httptest.ResponseRecorder{
		"the page":     e.get("/"),
		"an API reply": e.get("/api/settings"),
		"a 404":        e.get("/api/nonsense"),
		"a refusal": e.send(http.MethodPost, purgeURL, `{}`,
			"Content-Type", "application/json", "Sec-Fetch-Site", "cross-site"),
	}
	want := map[string]string{
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
		"X-Content-Type-Options":  "nosniff",
	}
	for name, w := range cases {
		for header, value := range want {
			if got := w.Header().Get(header); got != value {
				t.Errorf("%s (status %d): %s = %q, want %q", name, w.Code, header, got, value)
			}
		}
	}
}
