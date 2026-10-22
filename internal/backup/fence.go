package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
)

// Fencing off an out-of-date copy of a lab (SPEC.md §6.9, "Two copies of
// one lab"). A full-lab restore makes the new machine the same lab (same
// IDs, keys and domain), so if the old one comes back, both would back up
// into the same repositories and update the same DNS records. The backups
// are the referee both can see: every snapshot is tagged with the lab's
// generation, which a full-lab restore raises, and a server that finds a
// newer generation in its own repository stands down. The controller also
// leaves a "removed" mark in the repository of a server it removed while
// it couldn't be reached, so that one stands down if it comes back.

// Tags for fencing.
const (
	genPrefix  = "gen="
	TagRemoved = "fence=removed"
)

// GenTag tags a run's snapshots with the lab's generation.
func GenTag(gen int) string { return genPrefix + strconv.Itoa(gen) }

// Gen is the generation a snapshot was made in (0 if untagged).
func (s Snapshot) Gen() int {
	for _, t := range s.Tags {
		if v, ok := strings.CutPrefix(t, genPrefix); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

// GenerationFile holds the lab's generation (a number; missing is 0). It's
// part of Roostack's state, so it's backed up and restored with the lab,
// then raised by the restore.
func GenerationFile(paths config.Paths) string {
	return filepath.Join(paths.StateDir, "lab-generation")
}

// ReadGeneration reads the lab's generation (0 if never set).
func ReadGeneration(paths config.Paths) int {
	data, err := os.ReadFile(GenerationFile(paths))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

// WriteGeneration saves the lab's generation.
func WriteGeneration(paths config.Paths, gen int) error {
	return fsutil.WritePrivate(GenerationFile(paths), []byte(strconv.Itoa(gen)+"\n"))
}

// Fence says why this server stood down.
type Fence struct {
	// Reason is FenceRestored (the lab was restored on another machine) or
	// FenceRemoved (the lab removed this server).
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
	// Host is the machine that made the newer backups; Gen their
	// generation (FenceRestored).
	Host string `json:"host,omitempty"`
	Gen  int    `json:"gen,omitempty"`
}

// Fence reasons.
const (
	FenceRestored = "restored"
	FenceRemoved  = "removed"
)

// FenceError stops a backup: this copy is out of date.
type FenceError struct{ Fence Fence }

func (e *FenceError) Error() string { return e.Fence.Message() }

// Message says what happened, for people.
func (f Fence) Message() string {
	if f.Reason == FenceRemoved {
		return "This server was removed from its lab, so it no longer backs up there"
	}
	return fmt.Sprintf("This lab was restored on another machine (%s) on %s, so this copy is out of date and no longer backs up",
		f.Host, f.Since.Local().Format("2 January 15:04"))
}

// ParseFence reads a fence as saved (FenceKey); nil if none.
func ParseFence(v string) *Fence {
	var f Fence
	if v == "" || json.Unmarshal([]byte(v), &f) != nil {
		return nil
	}
	return &f
}

// ErrFenced means backups stood down (Status.Fenced says why).
var ErrFenced = errors.New("backups stood down: this copy of the lab is out of date")

// checkFence looks in a repository (which exists: upload opened it) for a
// newer generation or a removed mark. If its backups can't be listed, the
// backup fails, to be tried again, rather than going ahead without
// knowing whether this copy of the lab is out of date (security review
// of 2026-10-16).
func checkFence(ctx context.Context, repo *Repo, gen int) error {
	snaps, err := repo.Snapshots(ctx, TagRoostack)
	if err != nil {
		return fmt.Errorf("couldn't list the backups there, to check this copy of the lab is the current one: %w", err)
	}
	for _, s := range snaps {
		if hasTag(s, TagRemoved) {
			return &FenceError{Fence{Reason: FenceRemoved, Since: s.Time}}
		}
		if s.Gen() > gen {
			return &FenceError{Fence{Reason: FenceRestored, Since: s.Time, Host: s.Hostname, Gen: s.Gen()}}
		}
	}
	return nil
}

func hasTag(s Snapshot, tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// MarkRemoved leaves the removed mark in a removed server's repository
// (the controller, with a key that can write there).
func (r *Repo) MarkRemoved(ctx context.Context, server string) error {
	cmd := r.command(ctx, "backup", "--json", "--stdin", "--stdin-filename", "removed-from-lab.txt", "--host", "roostack-controller",
		"--tag", TagRoostack, "--tag", TagRemoved)
	cmd.Stdin = strings.NewReader("Server " + server + " was removed from its lab on " + time.Now().UTC().Format(time.RFC3339) +
		". It no longer backs up here.\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restic backup (removed mark): %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FenceKey is where a server's fence is kept (its store's settings): set
// means this copy stood down.
const FenceKey = "backup.fence"

// loadFence reads this server's fence, if any.
func (s *Service) loadFence(ctx context.Context) *Fence {
	v, err := s.Store.Setting(ctx, FenceKey)
	if err != nil {
		return nil
	}
	return ParseFence(v)
}

// setFence records (or with nil, clears) this server's fence.
func (s *Service) setFence(ctx context.Context, f *Fence) error {
	v := ""
	if f != nil {
		data, _ := json.Marshal(f)
		v = string(data)
	}
	return s.Store.SetSetting(ctx, FenceKey, v)
}

// Fenced reports why this server's backups stood down (nil: they didn't).
func (s *Service) Fenced(ctx context.Context) *Fence { return s.loadFence(ctx) }

// KeepThisCopy is the Owner saying this copy of the lab is the right one
// after all (e.g. the other was a test restore): its generation goes past
// the other's, so the other stands down at its next backup, and this one
// backs up again.
func (s *Service) KeepThisCopy(ctx context.Context) error {
	f := s.loadFence(ctx)
	if f == nil || f.Reason != FenceRestored || s.Deps == nil {
		return nil
	}
	gen := max(f.Gen, s.Deps.Generation) + 1
	if err := WriteGeneration(s.Deps.Paths, gen); err != nil {
		return err
	}
	s.Deps.Generation = gen
	return s.setFence(ctx, nil)
}
