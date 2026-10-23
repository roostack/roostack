package cloud

import (
	"context"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
)

// TestNews: Cloud's nudge (SPEC.md §6.16) reaches the lab: a linked lab
// that isn't relayed asks for it every so often, a relayed one hears it
// in the relay's answer, and a Cloud from before it (404) is no bother.
func TestNews(t *testing.T) {
	heard := func(t *testing.T, ch chan int64, want int64) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Errorf("heard %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("didn't hear %d", want)
		}
	}

	t.Run("asked", func(t *testing.T) {
		f, s, _ := fakeCloud(t)
		link(t, f, s, "")
		f.Registry = 1792744167
		ch := make(chan int64, 8)
		s.OnRegistry(func(serial int64) { ch <- serial })
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go s.WatchNews(ctx, 10*time.Millisecond)
		heard(t, ch, 1792744167)
	})

	t.Run("relayed", func(t *testing.T) {
		f, s, _ := fakeCloud(t)
		s.WireGuard = &fakeWG{}
		link(t, f, s, "")
		f.Registry = 42
		ch := make(chan int64, 8)
		s.OnRegistry(func(serial int64) { ch <- serial })
		ctx := context.Background()
		if err := s.Relay(ctx, []string{"photos.smiths.roost.test"}, config.Countries{}, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := s.refreshRelay(ctx); err != nil {
			t.Fatal(err)
		}
		heard(t, ch, 42)
	})

	t.Run("older Cloud", func(t *testing.T) {
		f, s, _ := fakeCloud(t)
		link(t, f, s, "")
		f.Registry, f.NoNews = 42, true
		ch := make(chan int64, 8)
		s.OnRegistry(func(serial int64) { ch <- serial })
		ctx, cancel := context.WithCancel(context.Background())
		go s.WatchNews(ctx, 10*time.Millisecond)
		time.Sleep(200 * time.Millisecond)
		cancel()
		if len(ch) != 0 || s.Status().LastError != "" {
			t.Errorf("an older Cloud: heard %d, %q", len(ch), s.Status().LastError)
		}
	})
}
