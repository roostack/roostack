package testwait

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestFor(t *testing.T) {
	var n atomic.Int32
	go func() {
		time.Sleep(30 * time.Millisecond)
		n.Store(1)
	}()
	For(t, 10*time.Second, func() bool { return n.Load() == 1 })
}

// A deadline that passes fails the test, rather than letting it carry on.
func TestForFailsOnItsDeadline(t *testing.T) {
	ft := &fakeT{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		For(ft, 30*time.Millisecond, func() bool { return false })
	}()
	<-done
	if !ft.failed {
		t.Error("For returned after its deadline without failing")
	}
}

// fakeT records Fatalf and ends its goroutine, as testing.T's does.
type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Helper() {}

func (f *fakeT) Fatalf(string, ...any) {
	f.failed = true
	runtime.Goexit()
}
