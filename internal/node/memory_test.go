package node

import (
	"strings"
	"testing"
)

func TestParseMeminfo(t *testing.T) {
	m, err := parseMeminfo(strings.NewReader("MemTotal:        4006980 kB\nMemFree:         1796584 kB\nMemAvailable:    3587552 kB\n"))
	if err != nil || m.Total != 4006980*1024 || m.Available != 3587552*1024 {
		t.Errorf("parseMeminfo = %+v, %v", m, err)
	}
	// A kernel without MemAvailable (older than 3.14) isn't one Roostack
	// runs on; it's an error rather than a guess.
	if _, err := parseMeminfo(strings.NewReader("MemTotal: 4006980 kB\n")); err == nil {
		t.Error("no MemAvailable: want an error")
	}
}
