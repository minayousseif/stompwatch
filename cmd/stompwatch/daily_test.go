package main

import (
	"testing"
	"time"
)

func TestNextDaily(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("time zone data not available: %v", err)
	}
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"before the hour", time.Date(2026, 9, 11, 3, 0, 0, 0, ny), time.Date(2026, 9, 11, 4, 0, 0, 0, ny)},
		{"exactly at the hour", time.Date(2026, 9, 11, 4, 0, 0, 0, ny), time.Date(2026, 9, 12, 4, 0, 0, 0, ny)},
		{"after the hour", time.Date(2026, 9, 11, 23, 30, 0, 0, ny), time.Date(2026, 9, 12, 4, 0, 0, 0, ny)},
		{"across month end", time.Date(2026, 9, 30, 5, 0, 0, 0, ny), time.Date(2026, 10, 1, 4, 0, 0, 0, ny)},
		// Clocks go forward at 02:00 on 2026-03-08; 04:00 still exists.
		{"across daylight saving start", time.Date(2026, 3, 7, 5, 0, 0, 0, ny), time.Date(2026, 3, 8, 4, 0, 0, 0, ny)},
	}
	for _, tc := range tests {
		if got := nextDaily(tc.now, 4); !got.Equal(tc.want) {
			t.Errorf("%s: nextDaily(%v) = %v, want %v", tc.name, tc.now, got, tc.want)
		}
	}
}
