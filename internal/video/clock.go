package video

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxClockDrift is how far either clock may sit from NTP before it is a
// problem (SPEC.md section 7). Timestamp integrity is the foundation of the
// record: a clip stamped two seconds off is a clip of the wrong moment.
const MaxClockDrift = 2 * time.Second

// ErrNoCameraClock means the camera answered but did not say the time.
var ErrNoCameraClock = errors.New("video: the camera does not report its clock in RTSP replies")

// ntpEpochOffset is the seconds between the NTP epoch of 1900 and the Unix
// epoch of 1970.
const ntpEpochOffset = 2208988800

// clockTimeout bounds one exchange with either the NTP server or the camera.
const clockTimeout = 5 * time.Second

// NTPOffset asks server, as host:port or a host name on port 123, and
// returns the host's clock minus the server's: the host's error, positive
// when the host runs ahead. It is one SNTP exchange: 48 bytes out, 48
// back, with the four timestamps of RFC 4330.
func NTPOffset(ctx context.Context, server string) (time.Duration, error) {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "123")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return 0, fmt.Errorf("video: NTP %s: %w", server, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(clockTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	conn.SetDeadline(deadline)

	req := make([]byte, 48)
	req[0] = 0x23 // LI 0, version 4, mode 3 (client)
	t1 := time.Now()
	putNTP(req[40:48], t1)
	if _, err := conn.Write(req); err != nil {
		return 0, fmt.Errorf("video: NTP %s: %w", server, err)
	}
	reply := make([]byte, 48)
	n, err := conn.Read(reply)
	t4 := time.Now()
	if err != nil {
		return 0, fmt.Errorf("video: NTP %s: %w", server, err)
	}
	if n < 48 || reply[0]&0x07 != 4 {
		return 0, fmt.Errorf("video: NTP %s: the reply is not an NTP server reply", server)
	}
	if binary.BigEndian.Uint64(reply[24:32]) != binary.BigEndian.Uint64(req[40:48]) {
		return 0, fmt.Errorf("video: NTP %s: the reply is not to this request", server)
	}
	t2 := readNTP(reply[32:40])
	t3 := readNTP(reply[40:48])
	return -(t2.Sub(t1) + t3.Sub(t4)) / 2, nil
}

func putNTP(b []byte, t time.Time) {
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) << 32 / 1_000_000_000
	binary.BigEndian.PutUint32(b[0:4], uint32(secs))
	binary.BigEndian.PutUint32(b[4:8], uint32(frac))
}

func readNTP(b []byte) time.Time {
	secs := int64(binary.BigEndian.Uint32(b[0:4])) - ntpEpochOffset
	frac := int64(binary.BigEndian.Uint32(b[4:8])) * 1_000_000_000 >> 32
	return time.Unix(secs, frac)
}

// CameraOffset reads the camera's clock and returns it minus the host's.
// It sends an RTSP OPTIONS request, which needs no login, and reads the
// Date header of the reply. Not every camera sends one; then the error is
// ErrNoCameraClock and the owner checks the camera's clock by hand.
//
// The reading is whole seconds, because that is what the header carries,
// and it arrives half a round trip late. Both are far inside the 2 s that
// matter.
func CameraOffset(ctx context.Context, s Stream) (time.Duration, error) {
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("video: camera clock: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(clockTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	conn.SetDeadline(deadline)

	sent := time.Now()
	req := fmt.Sprintf("OPTIONS rtsp://%s/ RTSP/1.0\r\nCSeq: 1\r\nUser-Agent: stompwatch\r\n\r\n", addr)
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, fmt.Errorf("video: camera clock: %w", err)
	}
	r := bufio.NewReader(conn)
	status, err := r.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("video: camera clock: reading the reply: %w", err)
	}
	if !strings.HasPrefix(status, "RTSP/") {
		return 0, fmt.Errorf("video: camera clock: %s did not answer with RTSP", addr)
	}
	received := time.Now()
	var date string
	for {
		line, err := r.ReadString('\n')
		if err != nil || line == "\r\n" || line == "\n" {
			break
		}
		if key, val, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(key), "Date") {
			date = strings.TrimSpace(val)
		}
	}
	if date == "" {
		return 0, ErrNoCameraClock
	}
	camera, err := parseDate(date)
	if err != nil {
		return 0, fmt.Errorf("video: camera clock: cannot read the Date header %q: %w", date, err)
	}
	// The camera stamped the reply somewhere between the send and the
	// receive; the middle is the best estimate.
	host := sent.Add(received.Sub(sent) / 2)
	return camera.Sub(host), nil
}

// live555Date is how live555, the RTSP server in Reolink and many other
// cameras, writes the Date header: "Wed, Sep 23 2026 23:53:20 GMT", with the
// month before the day and the year after it.
const live555Date = "Mon, Jan 2 2006 15:04:05 MST"

// parseDate reads the Date header. HTTP wants GMT; a camera may write UTC
// or a numeric zone, and both mean the same instant.
func parseDate(date string) (time.Time, error) {
	if t, err := http.ParseTime(date); err == nil {
		return t, nil
	}
	for _, layout := range []string{time.RFC1123, time.RFC1123Z, live555Date} {
		if t, err := time.Parse(layout, date); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("not a date in RFC 1123 or live555 form")
}

// ClockReport is one check of both clocks against the same NTP server.
type ClockReport struct {
	At           time.Time
	HostOffset   time.Duration // host minus NTP
	HostErr      error
	CameraOffset time.Duration // camera minus host
	CameraErr    error
}

// CheckClocks measures the host against server and the camera against the
// host. Both halves run even when one fails, so the report says as much as
// can be known.
func CheckClocks(ctx context.Context, server string, s Stream) ClockReport {
	rep := ClockReport{At: time.Now()}
	rep.HostOffset, rep.HostErr = NTPOffset(ctx, server)
	rep.CameraOffset, rep.CameraErr = CameraOffset(ctx, s)
	return rep
}

// CameraFromNTP is how far the camera's clock sits from NTP: its offset
// from the host plus the host's own error.
func (r ClockReport) CameraFromNTP() time.Duration {
	return r.CameraOffset + r.HostOffset
}

// Problems lists what is wrong, in words for the log and the health log.
// Nothing is listed when both clocks are inside MaxClockDrift. When the
// host itself is off, or NTP did not answer, the camera is measured
// against the host, which is the clock the record is stamped with.
func (r ClockReport) Problems() []string {
	var out []string
	hostKnown := r.HostErr == nil && abs(r.HostOffset) <= MaxClockDrift
	switch {
	case r.HostErr != nil:
		out = append(out, fmt.Sprintf("the host clock could not be checked: NTP did not answer: %v", r.HostErr))
	case !hostKnown:
		out = append(out, fmt.Sprintf("the host clock is %s from NTP; the limit is %s", r.HostOffset.Round(time.Millisecond), MaxClockDrift))
	}
	switch {
	case r.CameraErr != nil:
		out = append(out, fmt.Sprintf("the camera clock is not readable: %v; check it by hand against the host", r.CameraErr))
	case hostKnown && abs(r.CameraFromNTP()) > MaxClockDrift:
		out = append(out, fmt.Sprintf("the camera clock is %s from NTP; the limit is %s", r.CameraFromNTP().Round(time.Millisecond), MaxClockDrift))
	case !hostKnown && abs(r.CameraOffset) > MaxClockDrift:
		out = append(out, fmt.Sprintf("the camera clock is %s from the host; the limit is %s", r.CameraOffset.Round(time.Millisecond), MaxClockDrift))
	}
	return out
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
