//go:build linux

package stress

import (
	"strings"
	"testing"
	"time"
)

func TestParseLinuxProcessCPUTime(t *testing.T) {
	fields := []string{
		"R",
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "10",
		"200",
		"50",
	}
	stat := []byte("123 (worker) with spaces) " + strings.Join(fields, " "))

	got, err := parseLinuxProcessCPUTime(stat, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2500*time.Millisecond {
		t.Fatalf("process CPU time = %v want 2.5s", got)
	}
}

func TestParseLinuxProcessCPUTimeRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name   string
		stat   []byte
		clocks float64
	}{
		{name: "missing process name", stat: []byte("123 R 1 2"), clocks: 100},
		{name: "too few fields", stat: []byte("123 (worker) R 1 2"), clocks: 100},
		{
			name:   "invalid user ticks",
			stat:   []byte("123 (worker) R 1 2 3 4 5 6 7 8 9 10 nope 50"),
			clocks: 100,
		},
		{
			name:   "invalid clock rate",
			stat:   []byte("123 (worker) R 1 2 3 4 5 6 7 8 9 10 200 50"),
			clocks: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseLinuxProcessCPUTime(tt.stat, tt.clocks); err == nil {
				t.Fatal("expected malformed process CPU stat to fail")
			}
		})
	}
}
