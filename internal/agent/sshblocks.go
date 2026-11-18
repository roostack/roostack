package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/trust"
)

// Blocking SSH password guessers (SPEC.md §6.10 item 3): an address that
// fails blockAfter times within blockWindow can't reach SSH for blockFor,
// or blockAgain if it was blocked in the last day. A block is an element
// with a timeout in the firewall's ssh_blocked sets, so the kernel lifts
// it by itself; the agent puts its blocks back each time the firewall is
// loaded again. An IPv6 address is blocked with its /64, since one person
// usually has a whole /64.
const (
	blockAfter  = 5
	blockWindow = 10 * time.Minute
	blockFor    = time.Hour
	blockAgain  = 24 * time.Hour
)

// The firewall's sets of blocked addresses (Firewall.Ruleset).
const (
	blockedSet4 = "ssh_blocked4"
	blockedSet6 = "ssh_blocked6"
)

// SSHBlock is an address, or an IPv6 address's /64, that can't reach SSH
// until a time.
type SSHBlock struct {
	Address string    `json:"address"`
	Until   time.Time `json:"until"`
}

// UnblockedFile keeps when the Owner unblocked each address, so that
// reading the last day again when the agent starts (after an update, say)
// doesn't block it again for the same wrong passwords.
const UnblockedFile = "/etc/roostack/ssh-unblocked.json"

// sshBlocks blocks this server's guessers.
var sshBlocks = &blocker{exempt: neverBlocked, nft: runNft, file: UnblockedFile}

type blocker struct {
	mu      sync.Mutex
	tries   map[netip.Prefix][]time.Time // recent failed passwords
	blocked map[netip.Prefix]time.Time   // until when
	last    map[netip.Prefix]time.Time   // when last blocked, for blockAgain
	in      map[netip.Prefix]time.Time   // when last logged in
	// unblocked is when the Owner unblocked each address, kept in file
	// (none in tests): wrong passwords from before then don't count.
	unblocked map[netip.Prefix]time.Time
	file      string
	seen      int // failed passwords since the last sweep
	// exempt says an address is never blocked; nft runs nft; log, if
	// set, hears when nft fails.
	exempt func(netip.Addr) bool
	nft    func(args ...string) error
	log    *slog.Logger
}

// blockKey is what's blocked for an address: itself, or its /64.
func blockKey(a netip.Addr) netip.Prefix {
	a = a.Unmap()
	if a.Is4() {
		return netip.PrefixFrom(a, 32)
	}
	p, _ := a.Prefix(64)
	return p
}

// neverBlocked: the home network, Roostack's own network (the lab's
// servers and VPN devices), and this server. Nor is an address that
// logged in over SSH in the last day (loggedIn), such as the Owner's
// office: they mistype a password there now and then.
func neverBlocked(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || trust.IsHome(a) || trust.OverlayRange.Contains(a)
}

// loggedIn notes a successful SSH login from addr at a time.
func (b *blocker) loggedIn(at time.Time, addr netip.Addr) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.in == nil {
		b.in = map[netip.Prefix]time.Time{}
	}
	b.in[blockKey(addr)] = at
}

// failed notes a failed password from addr at a time, and blocks addr if
// it's the blockAfter'th within blockWindow and the block isn't over by
// now (lines read when the agent starts can be up to a day old).
func (b *blocker) failed(at time.Time, addr netip.Addr, now time.Time) {
	k := blockKey(addr)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tries == nil {
		b.tries, b.blocked, b.last = map[netip.Prefix][]time.Time{}, map[netip.Prefix]time.Time{}, map[netip.Prefix]time.Time{}
	}
	if b.seen++; b.seen >= 1000 {
		b.sweep(now)
	}
	if until, ok := b.blocked[k]; ok && until.After(at) {
		return // blocked already: a connection from before the block
	}
	if u, ok := b.unblocked[k]; ok && !at.After(u) {
		return
	}
	tries := slices.DeleteFunc(b.tries[k], func(t time.Time) bool { return at.Sub(t) >= blockWindow })
	if tries = append(tries, at); len(tries) < blockAfter {
		b.tries[k] = tries
		return
	}
	delete(b.tries, k)
	if in, ok := b.in[k]; b.exempt(addr) || ok && at.Sub(in) < guessDay {
		return
	}
	d := blockFor
	if last, ok := b.last[k]; ok && at.Sub(last) < guessDay {
		d = blockAgain
	}
	b.last[k] = at
	until := at.Add(d)
	if !until.After(now) {
		return
	}
	b.blocked[k] = until
	b.add(map[netip.Prefix]time.Time{k: until}, now)
}

// sweep forgets what can't matter any more, so a day of many guessers
// doesn't pile up.
func (b *blocker) sweep(now time.Time) {
	b.seen = 0
	for k, tries := range b.tries {
		if len(tries) == 0 || now.Sub(tries[len(tries)-1]) >= blockWindow {
			delete(b.tries, k)
		}
	}
	for k, until := range b.blocked {
		if !until.After(now) {
			delete(b.blocked, k)
		}
	}
	for _, m := range []map[netip.Prefix]time.Time{b.last, b.in, b.unblocked} {
		for k, at := range m {
			if now.Sub(at) >= guessDay {
				delete(m, k)
			}
		}
	}
}

// add puts blocks in the firewall's sets. Without the sets (the firewall
// isn't loaded yet, or is the one for setup), they wait for reload.
func (b *blocker) add(blocks map[netip.Prefix]time.Time, now time.Time) {
	var v4, v6 []string
	for k, until := range blocks {
		secs := int(math.Ceil(until.Sub(now).Seconds()))
		if secs <= 0 {
			continue
		}
		e := fmt.Sprintf("%s timeout %ds", elementOf(k), secs)
		if k.Addr().Is4() {
			v4 = append(v4, e)
		} else {
			v6 = append(v6, e)
		}
	}
	for set, elems := range map[string][]string{blockedSet4: v4, blockedSet6: v6} {
		if len(elems) == 0 {
			continue
		}
		if err := b.nft("add", "element", "inet", "roostack", set, "{", strings.Join(elems, ", "), "}"); err != nil && b.log != nil {
			b.log.Warn("blocking SSH password guessers in the firewall: they'll be blocked once it's loaded again", "err", err)
		}
	}
}

// elementOf is a block as the set holds it: an IPv4 address, or a /64.
func elementOf(k netip.Prefix) string {
	if k.Addr().Is4() {
		return k.Addr().String()
	}
	return k.String()
}

// reload puts every block back, after the firewall was loaded again
// (which empties its sets).
func (b *blocker) reload(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.add(b.blocked, now)
}

// list returns the blocks in place now, the soonest over first.
func (b *blocker) list(now time.Time) []SSHBlock {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []SSHBlock
	for k, until := range b.blocked {
		if until.After(now) {
			out = append(out, SSHBlock{Address: elementOf(k), Until: until.UTC()})
		}
	}
	slices.SortFunc(out, func(a, b SSHBlock) int {
		if c := a.Until.Compare(b.Until); c != 0 {
			return c
		}
		return strings.Compare(a.Address, b.Address)
	})
	return out
}

// unblock lifts a block at once, and forgets it was there, so the next
// one is for blockFor again.
func (b *blocker) unblock(address string) error {
	return b.unblockAt(address, time.Now())
}

func (b *blocker) unblockAt(address string, now time.Time) error {
	k, err := netip.ParsePrefix(address)
	if err != nil {
		a, aerr := netip.ParseAddr(address)
		if aerr != nil {
			return fmt.Errorf("%q isn't an address", address)
		}
		k = netip.PrefixFrom(a, a.BitLen())
	}
	k = blockKey(k.Addr())
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.blocked, k)
	delete(b.last, k)
	delete(b.tries, k)
	if b.unblocked == nil {
		b.unblocked = map[netip.Prefix]time.Time{}
	}
	b.unblocked[k] = now
	if err := b.save(now); err != nil {
		return fmt.Errorf("remembering the unblock: %w", err)
	}
	set := blockedSet4
	if !k.Addr().Is4() {
		set = blockedSet6
	}
	// Not there (the kernel lifted it as it ran out): nothing to do.
	b.nft("delete", "element", "inet", "roostack", set, "{", elementOf(k), "}")
	return nil
}

// load reads when addresses were unblocked, before the last day is read.
func (b *blocker) load() error {
	if b.file == "" {
		return nil
	}
	data, err := os.ReadFile(b.file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved map[string]time.Time
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("%s: %w", b.file, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unblocked = map[netip.Prefix]time.Time{}
	for s, at := range saved {
		if k, err := netip.ParsePrefix(s); err == nil {
			b.unblocked[k] = at
		}
	}
	return nil
}

// save writes when addresses were unblocked, those in the last day.
func (b *blocker) save(now time.Time) error {
	if b.file == "" {
		return nil
	}
	saved := map[string]time.Time{}
	for k, at := range b.unblocked {
		if now.Sub(at) < guessDay {
			saved[k.String()] = at.UTC()
		}
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(b.file, append(data, '\n'), 0o644)
}

// runNft runs nft.
func runNft(args ...string) error {
	if out, err := exec.Command("nft", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
