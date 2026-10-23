package sleep

import (
	"context"
	"testing"
	"time"
)

func TestFor(t *testing.T) {
	if !For(context.Background(), time.Millisecond) {
		t.Error("a full wait: want true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if For(ctx, time.Hour) {
		t.Error("an ended context: want false")
	}
	if time.Since(start) > time.Second {
		t.Error("an ended context: For waited")
	}
}

func TestEvery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	Every(ctx, time.Millisecond, func() {
		if n++; n == 3 {
			cancel()
		}
	})
	if n != 3 {
		t.Errorf("called %d times, want 3", n)
	}
}
