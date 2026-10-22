package web

import (
	"sync"
	"time"
)

// expiring is a map whose entries end at a time: what the web UI keeps in
// memory for a few minutes (logins waiting for a two-factor code, sign-ins
// on their way from the sign-in page, the setup code's passes). It's safe
// for concurrent use, and each put drops the entries that have ended. In
// memory only: after a restart, people log in again.
type expiring[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]expiringEntry[V]
}

type expiringEntry[V any] struct {
	v     V
	until time.Time
}

// put keeps v under k until a time, first dropping the entries that have
// ended.
func (e *expiring[K, V]) put(k K, v V, until time.Time) {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.m == nil {
		e.m = map[K]expiringEntry[V]{}
	}
	for key, x := range e.m {
		if now.After(x.until) {
			delete(e.m, key)
		}
	}
	e.m[k] = expiringEntry[V]{v, until}
}

// get is k's value while it lasts; one that has ended is dropped.
func (e *expiring[K, V]) get(k K) (V, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lookup(k, false)
}

// take is get, and drops k.
func (e *expiring[K, V]) take(k K) (V, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lookup(k, true)
}

// lookup is get and take, with e locked.
func (e *expiring[K, V]) lookup(k K, drop bool) (V, bool) {
	x, ok := e.m[k]
	ended := ok && time.Now().After(x.until)
	if ok && (drop || ended) {
		delete(e.m, k)
	}
	if !ok || ended {
		var zero V
		return zero, false
	}
	return x.v, true
}

// delete drops k.
func (e *expiring[K, V]) delete(k K) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.m, k)
}

// deleteIf drops the entries whose keys match.
func (e *expiring[K, V]) deleteIf(match func(K) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k := range e.m {
		if match(k) {
			delete(e.m, k)
		}
	}
}
