package config

import (
	"os"
	"reflect"
	"testing"
)

// The example config in deploy/ must stay valid, and must use the defaults.
func TestExampleConfigParsesToDefaults(t *testing.T) {
	c, err := Load("../../deploy/stompwatch.conf.example")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	want.ExpectedCaptureGain = "none"
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("example config differs from the defaults:\n got %+v\nwant %+v", c, want)
	}
	if _, err := os.Stat("../../deploy/stompwatch.service"); err != nil {
		t.Errorf("systemd unit missing: %v", err)
	}
}
