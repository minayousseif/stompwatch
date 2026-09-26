package audio

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// SPEC.md section 3.1: capture only from a raw hw: device.
func TestValidateDevice(t *testing.T) {
	for _, dev := range []string{"hw:EM01,0", "hw:1,0", "hw:CARD=EM01,DEV=0"} {
		if err := ValidateDevice(dev); err != nil {
			t.Errorf("ValidateDevice(%q) = %v, want nil", dev, err)
		}
	}
	for _, dev := range []string{"", "default", "plug:hw:EM01,0", "plughw:EM01,0", "sysdefault:CARD=EM01",
		"dsnoop:EM01", "pulse", "pipewire", "HW:EM01,0", " hw:EM01,0"} {
		if err := ValidateDevice(dev); err == nil {
			t.Errorf("ValidateDevice(%q) = nil, want an error", dev)
		}
	}
}

// The output format of "amixer -c N sget <control>" from alsa-utils.
const (
	amixerMono = `Simple mixer control 'Mic',0
  Capabilities: cvolume cvolume-joined cswitch cswitch-joined
  Capture channels: Mono
  Limits: Capture 0 - 127
  Mono: Capture 100 [79%] [23.00dB] [on]
`
	amixerStereo = `Simple mixer control 'Mic',0
  Capabilities: cvolume cswitch
  Capture channels: Front Left - Front Right
  Limits: Capture 0 - 127
  Front Left: Capture 100 [79%] [23.00dB] [on]
  Front Right: Capture 100 [79%] [23.00dB] [on]
`
	amixerStereoMismatch = `Simple mixer control 'Mic',0
  Front Left: Capture 100 [79%] [23.00dB] [on]
  Front Right: Capture 90 [71%] [18.00dB] [on]
`
	amixerOff = `Simple mixer control 'Mic',0
  Mono: Capture 100 [79%] [23.00dB] [off]
`
	amixerNoSwitch = `Simple mixer control 'Mic',0
  Mono: Capture 50 [39%]
`
	amixerPlaybackOnly = `Simple mixer control 'PCM',0
  Capabilities: pvolume
  Mono: Playback 80 [100%] [0.00dB]
`
)

func TestCheckGain(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		expected string
		wantRaw  int
		wantErr  string
	}{
		{"mono matches", amixerMono, "100", 100, ""},
		{"stereo matches", amixerStereo, "100", 100, ""},
		{"no switch counts as on", amixerNoSwitch, "50", 50, ""},
		{"value differs", amixerMono, "90", 0, "90"},
		{"channels differ", amixerStereoMismatch, "100", 0, "differ"},
		{"capture switched off", amixerOff, "100", 0, "off"},
		{"no control expected", amixerPlaybackOnly, "none", 0, ""},
		{"control missing", amixerPlaybackOnly, "100", 0, "no capture control"},
		{"control present but none expected", amixerMono, "none", 0, "none"},
		{"expected not configured", amixerMono, "", 0, "expected_capture_gain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, err := CheckGain(tc.out, tc.expected)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckGain error = %v, want nil", err)
				}
				if g.Raw != tc.wantRaw {
					t.Errorf("raw = %d, want %d", g.Raw, tc.wantRaw)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CheckGain error = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// The log prints this, so a mic with no capture control must not look like a
// mic whose capture is switched off.
func TestCaptureGainString(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		expected string
		want     string
	}{
		{"no capture control", amixerPlaybackOnly, "none", "no capture control"},
		{"with gain in dB", amixerMono, "100", "100 (23.00dB)"},
		{"without gain in dB", amixerNoSwitch, "50", "50"},
		{"switched off", amixerOff, "100", "100 (23.00dB) off"},
	}
	for _, tc := range tests {
		g, _ := CheckGain(tc.out, tc.expected)
		if got := g.String(); got != tc.want {
			t.Errorf("%s: String() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func feedStuck(d *StuckDetector, seconds float64, chunk int, gen func(i int) float64) bool {
	n := int(seconds * 48000)
	buf := make([]float64, chunk)
	stuck := false
	for i := 0; i < n; i += chunk {
		m := min(chunk, n-i)
		for k := 0; k < m; k++ {
			buf[k] = gen(i + k)
		}
		stuck = d.Observe(buf[:m])
	}
	return stuck
}

// SPEC.md section 6.1: a stream that stopped looks like a quiet night unless it is
// caught. All-zero or unchanging audio for more than 5 s is a failure.
func TestStuckDetectorFlagsDigitalSilenceAfterFiveSeconds(t *testing.T) {
	d := NewStuckDetector(48000, 5*time.Second)
	if feedStuck(d, 4.9, 1000, func(int) float64 { return 0 }) {
		t.Fatal("stuck after 4.9 s of zeros, want not yet")
	}
	if !feedStuck(d, 0.2, 1000, func(int) float64 { return 0 }) {
		t.Fatal("not stuck after 5.1 s of zeros")
	}
}

func TestStuckDetectorFlagsConstantValue(t *testing.T) {
	d := NewStuckDetector(48000, 5*time.Second)
	if !feedStuck(d, 6, 1000, func(int) float64 { return 0.1234 }) {
		t.Fatal("not stuck after 6 s of a constant value")
	}
}

// A driver can return the same buffer again and again. The samples change
// inside the buffer, so only a repeat check catches it.
func TestStuckDetectorFlagsRepeatedBuffer(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	period := make([]float64, 480)
	for i := range period {
		period[i] = rng.Float64()*2 - 1
	}
	d := NewStuckDetector(48000, 5*time.Second)
	if !feedStuck(d, 6, 1000, func(i int) float64 { return period[i%480] }) {
		t.Fatal("not stuck after 6 s of a repeating 10 ms buffer")
	}
}

func TestStuckDetectorAcceptsNoiseAndRecovers(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	noise := func(int) float64 { return rng.Float64()*2e-4 - 1e-4 }
	d := NewStuckDetector(48000, 5*time.Second)
	if feedStuck(d, 20, 1000, noise) {
		t.Fatal("quiet noise reported as stuck")
	}
	feedStuck(d, 6, 1000, func(int) float64 { return 0 })
	if feedStuck(d, 0.5, 1000, noise) {
		t.Fatal("still stuck after the audio came back")
	}
}
