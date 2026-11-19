package agent

import (
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// CrowdSec's community blocklist at SSH (SPEC.md §6.10 item 3): addresses
// CrowdSec's community has seen attacking, which the lab's CrowdSec pulls
// while anything is public, can't reach SSH either. The roostack daemon
// reads the list (cscli, in CrowdSec's container) and sends it here: the
// agent has no network of its own (RestrictAddressFamilies). It replaces
// the firewall's ssh_community sets at once, each address with a timeout
// of communityFor, so a list that stops coming (CrowdSec stopped, nothing
// public any more) lifts by itself. Never in it: what neverBlocked
// exempts, and an address that logged in over SSH in the last day.
const (
	communitySet4 = "ssh_community4"
	communitySet6 = "ssh_community6"
	// communityFor is how long each address stays without a new list; the
	// daemon sends the list again well before (node.communityResend).
	communityFor = 6 * time.Hour
	// communityMax is the most addresses a list may have: CrowdSec's
	// community list has a few thousand (4,173 on the maintainer's lab,
	// 2026-11-19).
	communityMax = 100_000
	// The widest ranges taken: a list blocking more than this is wrong,
	// and would keep the Owner out with everyone else.
	communityMinBits4 = 16
	communityMinBits6 = 32
)

// CommunityRequest is CrowdSec's community blocklist, for SSH (POST
// /v1/ssh/community): addresses and ranges ("192.0.2.7", "198.51.100.0/24").
type CommunityRequest struct {
	Addresses []string `json:"addresses"`
}

// Only the count: the list is long.
func (r CommunityRequest) logAttrs() []any { return []any{"addresses", len(r.Addresses)} }

// communityList checks a list and makes it the prefixes the sets will
// hold: each an address or range on the internet, at most communityMax,
// none inside another (an interval set refuses overlaps).
func communityList(addrs []string) ([]netip.Prefix, error) {
	if len(addrs) > communityMax {
		return nil, fmt.Errorf("%d addresses is more than the %d a list may have", len(addrs), communityMax)
	}
	var out []netip.Prefix
	for _, s := range addrs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, fmt.Errorf("%q isn't an address or range", s)
			}
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Addr().Is4In6() {
			p = netip.PrefixFrom(p.Addr().Unmap(), max(0, p.Bits()-96))
		}
		p = p.Masked()
		a := p.Addr()
		if a.Is4() && p.Bits() < communityMinBits4 || !a.Is4() && p.Bits() < communityMinBits6 {
			return nil, fmt.Errorf("%s is wider than a list may block", p)
		}
		if !a.IsGlobalUnicast() || a.IsPrivate() || neverBlocked(a) {
			continue // not on the internet, or never blocked: left out, not an error
		}
		out = append(out, p)
	}
	// Widest first, so a range comes before the addresses inside it.
	slices.SortFunc(out, func(x, y netip.Prefix) int {
		if c := x.Addr().Compare(y.Addr()); c != 0 {
			return c
		}
		return x.Bits() - y.Bits()
	})
	kept := out[:0]
	for _, p := range out {
		if n := len(kept); n > 0 && kept[n-1].Overlaps(p) {
			continue // inside the range before it, or the same
		}
		kept = append(kept, p)
	}
	return kept, nil
}

// setCommunity replaces the community sets with list, leaving out
// addresses that logged in over SSH in the last day. Without the sets
// (the firewall isn't loaded yet, or is the one for setup), the list
// waits for reload, as blocks do.
func (b *blocker) setCommunity(list []netip.Prefix, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.community, b.communityAt = list, now
	if err := b.loadCommunity(now); err != nil && b.log != nil {
		b.log.Warn("putting CrowdSec's community blocklist at SSH: it'll be there once the firewall is loaded again", "err", err)
	}
}

// loadCommunity writes the community sets in one nft transaction (b.mu
// held): flushed, then filled, so the firewall never has half a list.
func (b *blocker) loadCommunity(now time.Time) error {
	left := communityFor - now.Sub(b.communityAt)
	if b.communityAt.IsZero() || left <= 0 {
		return nil
	}
	secs := int(left.Seconds())
	var v4, v6 []string
	for _, p := range b.community {
		if b.loggedInRecently(p, now) {
			continue
		}
		e := fmt.Sprintf("%s timeout %ds", communityElement(p), secs)
		if p.Addr().Is4() {
			v4 = append(v4, e)
		} else {
			v6 = append(v6, e)
		}
	}
	var script strings.Builder
	for _, s := range []struct {
		set   string
		elems []string
	}{{communitySet4, v4}, {communitySet6, v6}} {
		fmt.Fprintf(&script, "flush set inet roostack %s\n", s.set)
		if len(s.elems) > 0 {
			fmt.Fprintf(&script, "add element inet roostack %s { %s }\n", s.set, strings.Join(s.elems, ", "))
		}
	}
	return b.nftScript(script.String())
}

// loggedInRecently: an address in p logged in over SSH in the last day.
func (b *blocker) loggedInRecently(p netip.Prefix, now time.Time) bool {
	for k, at := range b.in {
		if now.Sub(at) < guessDay && p.Overlaps(k) {
			return true
		}
	}
	return false
}

// communityElement is a prefix as the set holds it: a single address
// without its length.
func communityElement(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// communityCount is how many addresses and ranges the list has, and when
// it came (zero if none has, or it has run out).
func (b *blocker) communityCount(now time.Time) (int, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.communityAt.IsZero() || now.Sub(b.communityAt) >= communityFor {
		return 0, time.Time{}
	}
	return len(b.community), b.communityAt
}

// runNftScript runs nft on a script, as one transaction.
func runNftScript(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetSSHCommunity replaces CrowdSec's community blocklist at SSH.
func (h LinuxHost) SetSSHCommunity(addrs []string) error {
	list, err := communityList(addrs)
	if err != nil {
		return err
	}
	sshBlocks.setCommunity(list, time.Now())
	return nil
}
