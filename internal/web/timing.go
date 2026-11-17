package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A page that takes slowPage or longer is logged with how long its parts
// took (timed), so that a slow page can be explained from the log rather
// than guessed at (the maintainer's lab, 2026-11-18: Home took 13 seconds
// once, on a small server whose memory was tight).
var slowPage = 2 * time.Second

// pageTimer collects a request's timed parts.
type pageTimer struct {
	mu    sync.Mutex
	parts []string
}

type pageTimerKey struct{}

// timed starts timing a part of the page r is building, and returns what
// ends it: `defer timed(ctx, "the servers' disks")()`. Parts may run at
// once; outside a request, it does nothing.
func timed(ctx context.Context, part string) func() {
	t, _ := ctx.Value(pageTimerKey{}).(*pageTimer)
	if t == nil {
		return func() {}
	}
	start := time.Now()
	return func() {
		d := time.Since(start)
		t.mu.Lock()
		t.parts = append(t.parts, fmt.Sprintf("%s %s", part, d.Round(time.Millisecond)))
		t.mu.Unlock()
	}
}

// timePages logs every request that takes slowPage or longer, with its
// timed parts. A download takes as long as it takes, so isn't timed.
func (s *Server) timePages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") || strings.Contains(r.URL.Path, "/download") {
			next.ServeHTTP(w, r)
			return
		}
		t := &pageTimer{}
		start := time.Now()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), pageTimerKey{}, t)))
		if d := time.Since(start); d >= slowPage {
			t.mu.Lock()
			parts := strings.Join(t.parts, ", ")
			t.mu.Unlock()
			s.Log.Warn("slow page", "method", r.Method, "path", r.URL.Path, "took", d.Round(time.Millisecond).String(), "parts", parts)
		}
	})
}
