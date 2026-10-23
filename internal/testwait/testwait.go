// Package testwait is the tests' one way to wait for something another
// goroutine does (quality review T4): poll until a condition holds, and
// fail the test when the deadline passes, rather than carry on and check
// a result that isn't there yet. It has no dependencies, so any package's
// tests can use it.
package testwait

import (
	"testing"
	"time"
)

// For polls cond every 10ms until it's true, and fails the test if it
// still isn't after timeout. Give a generous timeout: a passing test never
// waits for all of it, and a busy machine (CI, or the whole suite at once)
// can be many times slower than an idle one.
func For(t testing.TB, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting after %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
