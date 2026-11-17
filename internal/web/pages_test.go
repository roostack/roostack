package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMemorySwap: memory in use counts what programs hold in swap too, so
// a server that has moved more than it has into swap says so (the
// maintainer's lab read as having plenty while 1 GB was in swap).
func TestMemorySwap(t *testing.T) {
	gb := int64(1 << 30)
	u := serverUsage{MemUsed: gb, MemTotal: 2 * gb, MemPercent: 50, MemSwapped: gb + gb/2}
	if !u.MemOver() || u.MemLevel() != "warning" || !u.MemShown() {
		t.Errorf("over: %v %q %v", u.MemOver(), u.MemLevel(), u.MemShown())
	}
	if b := u.MemBar(); b != [2]int{40, 60} {
		t.Errorf("bar %v", b)
	}
	u.MemSwapped = 10 << 20
	if u.MemOver() || u.MemLevel() != "" || u.MemShown() || u.MemBar() != [2]int{50, 0} {
		t.Errorf("a little swap: %+v %v", u, u.MemBar())
	}
	var f homeFigures
	f.addUsage(map[string]serverUsage{"a": {MemUsed: gb, MemTotal: 2 * gb, MemSwapped: 2 * gb}})
	if !f.MemOver() || f.MemSwapped != 2*gb {
		t.Errorf("figures %+v", f)
	}
}

// TestSlowPage: a page that takes slowPage or longer is logged with its
// timed parts; a quick one isn't.
func TestSlowPage(t *testing.T) {
	old := slowPage
	slowPage = 20 * time.Millisecond
	defer func() { slowPage = old }()
	var buf bytes.Buffer
	s := &Server{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	h := s.timePages(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			done := timed(r.Context(), "disks")
			time.Sleep(30 * time.Millisecond)
			done()
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/quick", nil))
	if buf.Len() != 0 {
		t.Errorf("a quick page logged: %s", buf.String())
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/slow", nil))
	if got := buf.String(); !strings.Contains(got, `msg="slow page"`) || !strings.Contains(got, "path=/slow") || !strings.Contains(got, "parts=\"disks 3") {
		t.Errorf("log: %s", got)
	}
}
