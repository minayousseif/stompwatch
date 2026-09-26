package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/minayousseif/stompwatch/internal/detect"
)

func reviewURL(id int64) string {
	return "/api/events/" + strconv.FormatInt(id, 10) + "/review"
}

// raw sends a body exactly as written, so a test can send a field the Go
// struct does not have. It says the body is JSON, as the dashboard does.
func (e *env) raw(method, target, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	asOwner(req)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(w, req)
	return w
}

func TestReviewRecordsTheStatusAndTheNote(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)

	w := e.do(http.MethodPut, reviewURL(id),
		map[string]string{"status": "verified", "note": "kids again"},
		"Tailscale-User-Login", "me@example.com", "Tailscale-User-Name", "Me")
	if w.Code != 200 {
		t.Fatalf("PUT review = %d, want 200: %s", w.Code, w.Body.String())
	}
	m := decode(t, w)
	if got := str(t, m, "status"); got != "verified" {
		t.Errorf("status = %q, want \"verified\"", got)
	}
	if got := str(t, m, "note"); got != "kids again" {
		t.Errorf("note = %q, want \"kids again\"", got)
	}
	if got := str(t, m, "reviewer"); got != "me@example.com" {
		t.Errorf("reviewer = %q, want \"me@example.com\"", got)
	}
	if got := num(t, m, "reviewed_ms"); int64(got) != ms(0) {
		t.Errorf("reviewed_ms = %v, want %d", got, ms(0))
	}

	// The database holds what the response said.
	ev := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
	rev := object(t, ev, "review")
	if got := str(t, rev, "status"); got != "verified" {
		t.Errorf("the stored status is %q, want \"verified\"", got)
	}
	if got := str(t, rev, "reviewer"); got != "me@example.com" {
		t.Errorf("the stored reviewer is %q, want \"me@example.com\"", got)
	}
}

// verified and rejected are opposite answers. A swap would turn a night of
// real noise into a night of dismissed noise.
func TestEachStatusIsStoredAsItself(t *testing.T) {
	e := newEnv(t)
	for _, status := range []string{"verified", "rejected", "unsure"} {
		id := e.addEvent(time.Second, time.Second, 60, detect.Running)
		w := e.do(http.MethodPut, reviewURL(id), map[string]string{"status": status})
		if w.Code != 200 {
			t.Fatalf("PUT %s = %d: %s", status, w.Code, w.Body.String())
		}
		ev := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
		if got := str(t, object(t, ev, "review"), "status"); got != status {
			t.Errorf("stored %q as %q", status, got)
		}
		// The filter must agree with the stored row.
		m := e.getJSON("/api/events?status=" + status)
		got := ids(t, m)
		if len(got) != 1 || got[0] != id {
			t.Errorf("status=%s returned %v, want [%d]", status, got, id)
		}
	}
}

// The reviewer is who the network says is calling. The body must not be able
// to name someone else, or the audit trail is worth nothing.
func TestTheReviewerComesFromTheIdentityNotTheBody(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)

	w := e.raw(http.MethodPut, reviewURL(id),
		`{"status":"verified","note":"","reviewer":"someone.else@example.com"}`,
		"Tailscale-User-Login", "me@example.com")
	if w.Code != 400 {
		t.Fatalf("a body carrying a reviewer gave %d, want 400: %s", w.Code, w.Body.String())
	}

	// Without a reviewer in the body, the reviewer is the header's login.
	w = e.do(http.MethodPut, reviewURL(id), map[string]string{"status": "verified"},
		"Tailscale-User-Login", "me@example.com")
	if w.Code != 200 {
		t.Fatalf("PUT review = %d: %s", w.Code, w.Body.String())
	}
	if got := str(t, decode(t, w), "reviewer"); got != "me@example.com" {
		t.Errorf("reviewer = %q, want \"me@example.com\"", got)
	}
}

func TestReviewRejectsAStatusThatIsNotOneOfTheThree(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	for _, body := range []string{
		`{"status":"maybe"}`,
		`{"status":""}`,
		`{}`,
		`{"status":"verified"`,
		`not json at all`,
	} {
		w := e.raw(http.MethodPut, reviewURL(id), body)
		if w.Code != 400 {
			t.Errorf("PUT %s = %d, want 400: %s", body, w.Code, w.Body.String())
		}
	}
	// Nothing was written.
	ev := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
	if v := ev["review"]; v != nil {
		t.Errorf("a rejected review still stored %v", v)
	}
}

func TestReviewOfAnEventThatDoesNotExistIs404(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodPut, reviewURL(9999), map[string]string{"status": "verified"})
	wantError(t, w, 404, "")
	w = e.do(http.MethodDelete, reviewURL(9999), nil)
	wantError(t, w, 404, "")
}

func TestDeletingAReviewMakesTheEventUnreviewedAgain(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	e.addReview(id, "verified", "kids again", "me@example.com")

	w := e.do(http.MethodDelete, reviewURL(id), nil)
	if w.Code != 204 {
		t.Fatalf("DELETE review = %d, want 204: %s", w.Code, w.Body.String())
	}
	if w.Body.Len() != 0 {
		t.Errorf("a 204 carried a body: %s", w.Body.String())
	}
	ev := e.getJSON("/api/events/" + strconv.FormatInt(id, 10))
	if v := ev["review"]; v != nil {
		t.Errorf("review = %v after a delete, want null", v)
	}
	if got := ids(t, e.getJSON("/api/events?status=none")); len(got) != 1 || got[0] != id {
		t.Errorf("status=none returned %v, want [%d]", got, id)
	}
}

func TestANoteThatIsTooLongIsRefused(t *testing.T) {
	e := newEnv(t)
	id := e.addEvent(time.Second, time.Second, 60, detect.Running)
	long := make([]byte, 2001)
	for i := range long {
		long[i] = 'a'
	}
	w := e.do(http.MethodPut, reviewURL(id),
		map[string]string{"status": "verified", "note": string(long)})
	wantError(t, w, 400, "")
}
