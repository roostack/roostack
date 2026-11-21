package web

import (
	"strings"
	"sync"
	"time"
)

// Wrong passwords for one username (SPEC.md §5): Caddy limits each
// address, but guesses from many addresses at one account would get past
// that, so each username's wrong passwords are counted too. Past
// passwordFreeTries in an hour, each try must wait longer after the last
// wrong one (doubling up to passwordLongestWait), and it's refused before
// the password is checked, which saves the check's memory too. Nothing is
// ever locked, so nobody can shut someone out by guessing badly on
// purpose. Counted by the name typed, an account's or not, so the wait
// doesn't tell anyone which accounts exist; kept in memory, since it only
// slows guessing down.
const (
	passwordFreeTries   = 10
	passwordTriesWindow = time.Hour
	passwordLongestWait = 30 * time.Second
	// passwordWarnTries a day tell the lab's Owners (AuditPasswordGuesses).
	passwordWarnTries = 50
	// passwordTriesKept is the most usernames counted at once, so names
	// made up by the million can't fill the memory.
	passwordTriesKept = 10000
)

type passwordTries struct {
	mu     sync.Mutex
	byName map[string]*nameTries
}

// nameTries are one username's wrong passwords: in the hour since first
// (for the wait) and in the day since dayStart (for the Owners), from
// which addresses that day.
type nameTries struct {
	n, day      int
	first, last time.Time
	dayStart    time.Time
	addrs       map[string]bool
	warned      bool
}

// wait is how long a try for name must still wait, or 0.
func (p *passwordTries) wait(name string, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.byName[strings.ToLower(name)]
	if t == nil || now.Sub(t.first) > passwordTriesWindow || t.n < passwordFreeTries {
		return 0
	}
	d := time.Second << min(t.n-passwordFreeTries, 5)
	return max(t.last.Add(min(d, passwordLongestWait)).Sub(now), 0)
}

// fail counts a wrong password for name from addr. It returns the day's
// count and addresses the first time the day reaches passwordWarnTries,
// otherwise 0.
func (p *passwordTries) fail(name, addr string, now time.Time) (day, addrs int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToLower(name)
	t := p.byName[key]
	if t == nil {
		if p.byName == nil {
			p.byName = map[string]*nameTries{}
		}
		if len(p.byName) >= passwordTriesKept {
			p.prune(now)
			if len(p.byName) >= passwordTriesKept {
				return 0, 0
			}
		}
		t = &nameTries{first: now, dayStart: now}
		p.byName[key] = t
	}
	if now.Sub(t.first) > passwordTriesWindow {
		t.n, t.first = 0, now
	}
	if now.Sub(t.dayStart) > 24*time.Hour {
		t.day, t.dayStart, t.addrs, t.warned = 0, now, nil, false
	}
	t.n++
	t.day++
	t.last = now
	if t.addrs == nil {
		t.addrs = map[string]bool{}
	}
	if len(t.addrs) < 1000 {
		t.addrs[addr] = true
	}
	if t.day >= passwordWarnTries && !t.warned {
		t.warned = true
		return t.day, len(t.addrs)
	}
	return 0, 0
}

// clear forgets name's wrong passwords for the wait, once the right one
// came: the day's count stays, for the Owners.
func (p *passwordTries) clear(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.byName[strings.ToLower(name)]; t != nil {
		t.n = 0
	}
}

// prune forgets names with nothing in the last day.
func (p *passwordTries) prune(now time.Time) {
	for k, t := range p.byName {
		if now.Sub(t.last) > 24*time.Hour {
			delete(p.byName, k)
		}
	}
}
