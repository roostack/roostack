package agent

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func testBlocker() (*blocker, *[]string) {
	var ran []string
	b := &blocker{
		exempt: func(a netip.Addr) bool { return a.String() == "198.51.100.7" },
		nft: func(args ...string) error {
			ran = append(ran, strings.Join(args, " "))
			return nil
		},
	}
	return b, &ran
}

// Five failed passwords within ten minutes block an address for an hour;
// four, or five spread out, don't. Blocked again within a day, it's for a
// day.
func TestBlockGuesser(t *testing.T) {
	b, ran := testBlocker()
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	a := netip.MustParseAddr("203.0.113.9")
	for i := range 4 {
		b.failed(t0.Add(time.Duration(i)*time.Minute), a, t0.Add(4*time.Minute))
	}
	if len(b.list(t0)) != 0 || len(*ran) != 0 {
		t.Fatalf("blocked after four: %v", *ran)
	}
	// Spread out: the first falls out of the ten minutes.
	b.failed(t0.Add(10*time.Minute), a, t0.Add(10*time.Minute))
	if len(b.list(t0.Add(10*time.Minute))) != 0 {
		t.Fatal("blocked for five over ten minutes")
	}
	// Two more: those from 2, 3 and 10 minutes in and these make five
	// within ten minutes.
	at := t0.Add(11 * time.Minute)
	b.failed(at, a, at)
	b.failed(at, a, at)
	got := b.list(at)
	if len(got) != 1 || got[0].Address != "203.0.113.9" || !got[0].Until.Equal(at.Add(time.Hour)) {
		t.Fatalf("blocks = %+v", got)
	}
	if !slices.Equal(*ran, []string{"add element inet roostack ssh_blocked4 { 203.0.113.9 timeout 3600s }"}) {
		t.Errorf("nft = %q", *ran)
	}
	// Lines from connections made before the block took don't count.
	b.failed(at.Add(time.Second), a, at.Add(time.Second))
	if len(*ran) != 1 {
		t.Errorf("counted while blocked: %q", *ran)
	}
	if len(b.list(at.Add(time.Hour))) != 0 {
		t.Error("still listed once over")
	}
	// Again the same day: a day.
	again := at.Add(3 * time.Hour)
	for i := range 5 {
		b.failed(again.Add(time.Duration(i)*time.Second), a, again.Add(5*time.Second))
	}
	if got := b.list(again); len(got) != 1 || !got[0].Until.Equal(again.Add(4*time.Second+24*time.Hour)) {
		t.Errorf("blocked again = %+v", got)
	}
}

// Never blocked: the exempt (the home network, the lab) and an address
// that logged in in the last day; an IPv6 address is blocked with its /64; a block from a day-old
// line that's over by now isn't added; unblocking lifts it and starts
// over; a reload puts every block back.
func TestBlockRules(t *testing.T) {
	b, ran := testBlocker()
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	five := func(addr string, at time.Time) {
		for i := range 5 {
			b.failed(at.Add(time.Duration(i)*time.Second), netip.MustParseAddr(addr), now)
		}
	}
	five("198.51.100.7", now)
	b.loggedIn(now.Add(-23*time.Hour), netip.MustParseAddr("198.51.100.8"))
	five("198.51.100.8", now)
	five("192.0.2.1", now.Add(-3*time.Hour)) // over an hour ago
	if len(b.list(now)) != 0 || len(*ran) != 0 {
		t.Fatalf("blocked: %v %q", b.list(now), *ran)
	}
	five("2001:db8:1:2::77", now)
	if got := b.list(now); len(got) != 1 || got[0].Address != "2001:db8:1:2::/64" {
		t.Fatalf("IPv6 = %+v", got)
	}
	if !strings.Contains((*ran)[0], "ssh_blocked6 { 2001:db8:1:2::/64 timeout") {
		t.Errorf("nft = %q", *ran)
	}
	five("203.0.113.9", now)
	*ran = nil
	b.reload(now)
	if len(*ran) != 2 {
		t.Errorf("reload = %q", *ran)
	}
	*ran = nil
	if err := b.unblock("2001:db8:1:2::/64"); err != nil {
		t.Fatal(err)
	}
	if err := b.unblock("not an address"); err == nil {
		t.Error("unblocked nonsense")
	}
	if got := b.list(now); len(got) != 1 || got[0].Address != "203.0.113.9" {
		t.Errorf("after unblock = %+v", got)
	}
	if !slices.Equal(*ran, []string{"delete element inet roostack ssh_blocked6 { 2001:db8:1:2::/64 }"}) {
		t.Errorf("nft = %q", *ran)
	}
	// A login more than a day before doesn't count.
	b.loggedIn(now.Add(-25*time.Hour), netip.MustParseAddr("198.51.100.9"))
	five("198.51.100.9", now)
	if got := b.list(now); len(got) != 2 {
		t.Errorf("an old login kept an address from being blocked: %+v", got)
	}
	// Unblocked, it starts over: an hour, not a day.
	later := now.Add(time.Hour)
	for i := range 5 {
		b.failed(later.Add(time.Duration(i)*time.Second), netip.MustParseAddr("2001:db8:1:2::99"), later)
	}
	for _, bl := range b.list(later) {
		if bl.Address == "2001:db8:1:2::/64" && !bl.Until.Equal(later.Add(4*time.Second+time.Hour)) {
			t.Errorf("after unblocking = %+v", bl)
		}
	}
}

// An unblock is kept on disk: read again after a restart, the same wrong
// passwords don't block the address again, but new ones do.
func TestUnblockKept(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ssh-unblocked.json")
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	a := netip.MustParseAddr("203.0.113.9")
	five := func(b *blocker, at time.Time) {
		for i := range 5 {
			b.failed(at.Add(time.Duration(i)*time.Second), a, now.Add(10*time.Minute))
		}
	}
	b, _ := testBlocker()
	b.file = file
	five(b, now)
	if err := b.unblockAt("203.0.113.9", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// The agent starts again and reads the day again.
	b2, _ := testBlocker()
	b2.file = file
	if err := b2.load(); err != nil {
		t.Fatal(err)
	}
	five(b2, now)
	if got := b2.list(now.Add(10 * time.Minute)); len(got) != 0 {
		t.Fatalf("blocked again for the same lines: %+v", got)
	}
	five(b2, now.Add(5*time.Minute))
	if got := b2.list(now.Add(10 * time.Minute)); len(got) != 1 {
		t.Errorf("new wrong passwords didn't block: %+v", got)
	}
}
