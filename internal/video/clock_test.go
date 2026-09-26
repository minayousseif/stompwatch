package video

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeNTP answers every request with a clock that is skew ahead of the
// host's. It returns the address to ask.
func fakeNTP(t *testing.T, skew time.Duration) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 128)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 48 {
				continue
			}
			now := time.Now().Add(skew)
			reply := make([]byte, 48)
			reply[0] = 0x24                // LI 0, version 4, mode 4 (server)
			reply[1] = 2                   // stratum
			copy(reply[24:32], buf[40:48]) // originate = the client's transmit
			putNTPTime(reply[32:40], now)  // receive
			putNTPTime(reply[40:48], now)  // transmit
			conn.WriteTo(reply, addr)
		}
	}()
	return conn.LocalAddr().String()
}

func putNTPTime(b []byte, t time.Time) {
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) << 32 / 1_000_000_000
	binary.BigEndian.PutUint32(b[0:4], uint32(secs))
	binary.BigEndian.PutUint32(b[4:8], uint32(frac))
}

// The host half of the clock check: an SNTP exchange gives the offset of
// the host clock from the server's.
func TestNTPOffsetMeasuresTheHostClock(t *testing.T) {
	for _, skew := range []time.Duration{0, 5 * time.Second, -3 * time.Second} {
		addr := fakeNTP(t, skew)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := NTPOffset(ctx, addr)
		cancel()
		if err != nil {
			t.Fatalf("skew %v: %v", skew, err)
		}
		// The offset is the host's error, so a server ahead reads negative.
		if d := got + skew; d < -200*time.Millisecond || d > 200*time.Millisecond {
			t.Errorf("skew %v: offset %v", skew, got)
		}
	}
}

func TestNTPOffsetFailsPlainlyWhenNothingAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// A port nothing listens on. UDP does not refuse, so this is a timeout.
	_, err := NTPOffset(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("no error from a server that never answers")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error %q does not name the server", err)
	}
}

// fakeRTSP answers OPTIONS with a Date header when date is not empty. It
// records whether the request carried any credentials.
func fakeRTSP(t *testing.T, date string) (host string, port int, sawAuth *bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	seen := new(bool)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil || line == "\r\n" {
						break
					}
					if strings.HasPrefix(strings.ToLower(line), "authorization:") {
						*seen = true
					}
				}
				reply := "RTSP/1.0 200 OK\r\nCSeq: 1\r\nPublic: OPTIONS, DESCRIBE, SETUP, PLAY\r\n"
				if date != "" {
					reply += "Date: " + date + "\r\n"
				}
				c.Write([]byte(reply + "\r\n"))
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, seen
}

// The camera half: the Date header in an RTSP reply is the camera's own
// clock. No login is sent to read it, so the password stays out of a
// check that runs every hour.
func TestCameraClockReadsTheDateHeader(t *testing.T) {
	camTime := time.Now().Add(7 * time.Second).UTC()
	host, port, sawAuth := fakeRTSP(t, camTime.Format(time.RFC1123))
	s := Stream{Host: host, Port: port, Creds: Credentials{User: "admin", Pass: testPass}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	offset, err := CameraOffset(ctx, s)
	if err != nil {
		t.Fatalf("CameraOffset: %v", err)
	}
	// RFC 1123 has whole seconds, so the reading is within a second.
	if offset < 6*time.Second || offset > 8*time.Second {
		t.Errorf("offset %v, want about 7 s", offset)
	}
	if *sawAuth {
		t.Error("the clock check sent the login")
	}
}

// Reolink cameras, and others built on live555, write the Date header as
// "Wed, Sep 23 2026 23:53:20 GMT": month before day, and no RFC 1123 order.
// The string is the one a real camera sent.
func TestParseDateReadsTheLive555Form(t *testing.T) {
	got, err := parseDate("Wed, Sep 23 2026 23:53:20 GMT")
	if err != nil {
		t.Fatalf("parseDate: %v", err)
	}
	if want := time.Date(2026, time.September, 23, 23, 53, 20, 0, time.UTC); !got.Equal(want) {
		t.Errorf("parseDate = %v, want %v", got, want)
	}
}

func TestCameraClockReadsTheLive555DateHeader(t *testing.T) {
	camTime := time.Now().Add(-5 * time.Second).UTC()
	host, port, _ := fakeRTSP(t, camTime.Format("Mon, Jan 02 2006 15:04:05 GMT"))
	s := Stream{Host: host, Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	offset, err := CameraOffset(ctx, s)
	if err != nil {
		t.Fatalf("CameraOffset: %v", err)
	}
	if offset < -6*time.Second || offset > -4*time.Second {
		t.Errorf("offset %v, want about -5 s", offset)
	}
}

func TestParseDateStillRefusesNonsense(t *testing.T) {
	for _, date := range []string{"", "yesterday", "Wed, Sep 32 2026 23:53:20 GMT", "2026-09-23"} {
		if _, err := parseDate(date); err == nil {
			t.Errorf("parseDate(%q) gave no error", date)
		}
	}
}

func TestCameraClockSaysWhenTheCameraDoesNotTellIt(t *testing.T) {
	host, port, _ := fakeRTSP(t, "")
	s := Stream{Host: host, Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := CameraOffset(ctx, s)
	if !errors.Is(err, ErrNoCameraClock) {
		t.Fatalf("CameraOffset gave %v, want ErrNoCameraClock", err)
	}
	if strings.Contains(err.Error(), testPass) {
		t.Error("the error holds the password")
	}
}

// SPEC.md section 7: warn above 2 s of drift, for either clock, against the same
// NTP source.
func TestClockReportFlagsDriftAboveTwoSeconds(t *testing.T) {
	if MaxClockDrift != 2*time.Second {
		t.Fatalf("MaxClockDrift = %v", MaxClockDrift)
	}
	tests := []struct {
		name   string
		rep    ClockReport
		wantN  int
		wantIn string
	}{
		{"both fine", ClockReport{HostOffset: 300 * time.Millisecond, CameraOffset: -500 * time.Millisecond}, 0, ""},
		{"host drifted", ClockReport{HostOffset: -2500 * time.Millisecond}, 1, "host clock"},
		{"camera drifted from ntp", ClockReport{HostOffset: 1500 * time.Millisecond, CameraOffset: 1500 * time.Millisecond}, 1, "camera clock"},
		{"camera unreadable", ClockReport{CameraErr: ErrNoCameraClock}, 1, "not readable"},
		{"ntp unreachable", ClockReport{HostErr: errors.New("timeout")}, 1, "NTP"},
	}
	for _, tc := range tests {
		got := tc.rep.Problems()
		if len(got) != tc.wantN {
			t.Errorf("%s: problems = %v, want %d", tc.name, got, tc.wantN)
			continue
		}
		if tc.wantN > 0 && !strings.Contains(got[0], tc.wantIn) {
			t.Errorf("%s: problem %q does not say %q", tc.name, got[0], tc.wantIn)
		}
	}
	// The camera's drift from NTP is its offset from the host plus the
	// host's offset from NTP.
	rep := ClockReport{HostOffset: 1500 * time.Millisecond, CameraOffset: 1500 * time.Millisecond}
	if got := rep.CameraFromNTP(); got != 3*time.Second {
		t.Errorf("CameraFromNTP = %v, want 3 s", got)
	}
}
