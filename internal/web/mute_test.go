package web

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestMuteWindowsAreAddedListedAndRemoved(t *testing.T) {
	e := newEnv(t)
	if got := list(t, e.getJSON("/api/mute-windows"), "windows"); len(got) != 0 {
		t.Errorf("a new database has %d mute windows, want none", len(got))
	}

	w := e.do(http.MethodPost, "/api/mute-windows", map[string]any{
		"start_ms": ms(0), "end_ms": ms(2 * time.Hour), "reason": "party",
	})
	if w.Code != 201 {
		t.Fatalf("POST = %d, want 201: %s", w.Code, w.Body.String())
	}
	created := decode(t, w)
	id := int64(num(t, created, "id"))
	if id <= 0 {
		t.Fatalf("the new window has id %d", id)
	}
	if got := str(t, created, "reason"); got != "party" {
		t.Errorf("reason = %q, want \"party\"", got)
	}
	if got := int64(num(t, created, "start_ms")); got != ms(0) {
		t.Errorf("start_ms = %d, want %d", got, ms(0))
	}
	if got := int64(num(t, created, "end_ms")); got != ms(2*time.Hour) {
		t.Errorf("end_ms = %d, want %d", got, ms(2*time.Hour))
	}

	windows := list(t, e.getJSON("/api/mute-windows"), "windows")
	if len(windows) != 1 || int64(num(t, windows[0], "id")) != id {
		t.Fatalf("the list holds %v, want the one window %d", windows, id)
	}

	del := e.do(http.MethodDelete, "/api/mute-windows/"+strconv.FormatInt(id, 10), nil)
	if del.Code != 204 {
		t.Fatalf("DELETE = %d, want 204: %s", del.Code, del.Body.String())
	}
	if got := list(t, e.getJSON("/api/mute-windows"), "windows"); len(got) != 0 {
		t.Errorf("the window is still there: %v", got)
	}
}

func TestAMuteWindowMustEndAfterItStarts(t *testing.T) {
	e := newEnv(t)
	for _, body := range []map[string]any{
		{"start_ms": ms(time.Hour), "end_ms": ms(0), "reason": ""},
		{"start_ms": ms(0), "end_ms": ms(0), "reason": ""},
	} {
		w := e.do(http.MethodPost, "/api/mute-windows", body)
		wantError(t, w, 400, "")
	}
	if got := list(t, e.getJSON("/api/mute-windows"), "windows"); len(got) != 0 {
		t.Errorf("a refused window was stored: %v", got)
	}
}

func TestDeletingAMuteWindowThatIsNotThereIs404(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.do(http.MethodDelete, "/api/mute-windows/9999", nil), 404, "")
	wantError(t, e.do(http.MethodDelete, "/api/mute-windows/abc", nil), 400, "")
}

func TestMuteWindowRejectsABodyItDoesNotKnow(t *testing.T) {
	e := newEnv(t)
	w := e.raw(http.MethodPost, "/api/mute-windows", `{"from":0,"to":1}`)
	wantError(t, w, 400, "")
}
