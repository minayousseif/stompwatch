package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// HeartbeatConfig sets up a Heartbeat.
type HeartbeatConfig struct {
	// URL is the ping URL, for example a healthchecks.io check. Empty
	// disables the heartbeat.
	URL string
	// Interval between pings. 0 means 5 minutes.
	Interval time.Duration
	// Client sends the pings. nil means a client with a 10 s timeout.
	Client *http.Client
	// Check returns nil while collection is healthy. It is required when
	// URL is set.
	Check func(now time.Time) error
	// Tick replaces the interval ticker, for tests.
	Tick <-chan time.Time
	Now  func() time.Time

	OnPing  func()
	OnSkip  func(error)
	OnError func(error)
}

// Heartbeat is the dead-man's switch (SPEC.md section 6.8.1). It pings a URL on
// every interval while collection is healthy and stays silent while it is
// not, so the monitoring service raises the alarm when pings stop. Fail sends
// an immediate failure. A failed ping is reported and never stops anything.
type Heartbeat struct {
	cfg  HeartbeatConfig
	ping *url.URL
	fail *url.URL
}

// NewHeartbeat checks the config. With an empty URL the heartbeat is
// disabled: Run returns at once and Fail does nothing.
func NewHeartbeat(cfg HeartbeatConfig) (*Heartbeat, error) {
	h := &Heartbeat{cfg: cfg}
	if cfg.URL == "" {
		return h, nil
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("health: heartbeat URL is not an http or https URL")
	}
	if cfg.Check == nil {
		return nil, errors.New("health: heartbeat needs a Check function")
	}
	if h.cfg.Interval == 0 {
		h.cfg.Interval = 5 * time.Minute
	}
	if h.cfg.Client == nil {
		h.cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if h.cfg.Now == nil {
		h.cfg.Now = time.Now
	}
	if h.cfg.OnPing == nil {
		h.cfg.OnPing = func() {}
	}
	if h.cfg.OnSkip == nil {
		h.cfg.OnSkip = func(error) {}
	}
	if h.cfg.OnError == nil {
		h.cfg.OnError = func(error) {}
	}

	fail := *u
	fail.Path = "/" + strings.TrimPrefix(path.Join(u.Path, "fail"), "/")
	fail.RawPath = ""
	h.ping, h.fail = u, &fail
	return h, nil
}

// Run pings on every tick until ctx is done.
func (h *Heartbeat) Run(ctx context.Context) {
	if h.ping == nil {
		return
	}
	tick := h.cfg.Tick
	if tick == nil {
		t := time.NewTicker(h.cfg.Interval)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			if err := h.cfg.Check(h.cfg.Now()); err != nil {
				h.cfg.OnSkip(err)
				continue
			}
			if err := h.send(ctx, http.MethodGet, h.ping, ""); err != nil {
				h.cfg.OnError(err)
				continue
			}
			h.cfg.OnPing()
		}
	}
}

// Fail reports a fatal condition at once, with reason as the body.
func (h *Heartbeat) Fail(ctx context.Context, reason string) error {
	if h.fail == nil {
		return nil
	}
	return h.send(ctx, http.MethodPost, h.fail, reason)
}

// send makes one request. Errors name only the host, because the path of a
// ping URL is a secret.
func (h *Heartbeat) send(ctx context.Context, method string, u *url.URL, body string) error {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("health: heartbeat to %s: %w", u.Host, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	resp, err := h.cfg.Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("health: heartbeat to %s: %w", u.Host, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("health: heartbeat to %s returned %s", u.Host, resp.Status)
	}
	return nil
}
