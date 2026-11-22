package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/public"
)

// CrowdSec's community blocklist at SSH (SPEC.md §6.10 item 3): every
// communityEvery, the controller reads the list its CrowdSec pulled and
// sends it to each server's agent, which keeps those addresses away from
// SSH. A server gets it again when it changes, or after communityResend
// whatever happens, well before the agent's six hours run out.
const (
	communityEvery  = 15 * time.Minute
	communityResend = 2 * time.Hour
)

// communitySeen is the list as last read, for Public access's card.
type communitySeen struct {
	mu    sync.Mutex
	count int
	at    time.Time
}

func (c *communitySeen) get() (int, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, c.at
}

// sendCommunity reads and sends the list until ctx ends.
func (c *controller) sendCommunity(seen *communitySeen) {
	type sent struct {
		sum [32]byte
		at  time.Time
	}
	last := map[string]sent{}
	unit := filepath.Join(c.lab.Paths.QuadletDir(), generate.CrowdSecUnit+".container")
	t := time.NewTimer(2 * time.Minute) // once CrowdSec has had time to start
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		t.Reset(communityEvery)
		// CrowdSec runs while anything is public, its unit there with it
		// (SPEC.md §6.11). Without it, the agents' lists will run out.
		if _, err := os.Stat(unit); errors.Is(err, os.ErrNotExist) {
			seen.mu.Lock()
			seen.count, seen.at = 0, time.Time{}
			seen.mu.Unlock()
			continue
		}
		list, err := public.CrowdSecCommunity(c.ctx, userLaunch, generate.CrowdSecUnit)
		if err != nil {
			c.log.Warn("reading CrowdSec's community blocklist for SSH", "err", err)
			continue
		}
		now := time.Now()
		seen.mu.Lock()
		seen.count, seen.at = len(list), now
		seen.mu.Unlock()
		sum := sha256.Sum256([]byte(strings.Join(list, "\n")))
		for id, set := range c.mgr.ServerSettings() {
			if s, ok := last[id]; ok && s.sum == sum && now.Sub(s.at) < communityResend {
				continue
			}
			ctx, cancel := context.WithTimeout(c.ctx, time.Minute)
			err := set.SetSSHCommunity(ctx, list)
			cancel()
			if err != nil {
				// A server from before this answers 404: it will get the
				// list once it's updated.
				c.log.Warn("sending CrowdSec's community blocklist to a server's SSH", "server", id, "err", err)
				continue
			}
			last[id] = sent{sum, now}
		}
	}
}
