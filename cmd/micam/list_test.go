package main

import (
	"strings"
	"testing"
	"time"
)

func TestReconnectDelayGrowsAndPlateaus(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 15 * time.Second},
		{6, 30 * time.Second},
		{7, 30 * time.Second},
		{50, 30 * time.Second},
	}
	for _, c := range cases {
		if got := reconnectDelay(c.attempt); got != c.want {
			t.Fatalf("reconnectDelay(%d) = %s, want %s", c.attempt, got, c.want)
		}
	}
}

func TestTruncateName(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"客厅摄像头", 20, "客厅摄像头"},       // short names pass through
		{"  padded  ", 20, "padded"}, // surrounding space is trimmed
		{"0123456789", 5, "0123…"},   // one rune is spent on the ellipsis
		{"摄像头一二三四五六七八九十", 4, "摄像头…"},
	}
	for _, c := range cases {
		if got := truncateName(c.in, c.limit); got != c.want {
			t.Fatalf("truncateName(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

func TestTruncateNameCountsRunesNotBytes(t *testing.T) {
	got := truncateName("摄像头", 2)
	if len(got) == 0 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncateName cut mid-character: %q", got)
	}
	// Two Chinese characters plus the ellipsis is three runes, six bytes; a
	// byte-based cut would have produced invalid UTF-8.
	if len([]rune(got)) != 2 {
		t.Fatalf("expected two runes, got %d", len([]rune(got)))
	}
}
