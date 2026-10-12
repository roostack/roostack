// Package cloudkeys keeps Roostack Cloud backup's short-lived keys on a
// server (Cloud's DESIGN §6(b), "Keys and layout"; SPEC.md §6.9): a backup
// asks for fresh ones when those it has expire within the hour, and uses
// what it has (still valid for a few minutes) if Cloud can't be reached
// meanwhile. Kept 0600 in the state folder, never logged.
package cloudkeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"roostack.cloud/roostack/cloudapi"
)

// File is where a server keeps its keys for purpose ("backup", "prune"). The
// settings backup leaves these out: they expire within hours, so a copy
// could only be misused.
func File(stateDir, purpose string) string {
	return filepath.Join(stateDir, "cloud-"+purpose+"-keys.json")
}

// Source asks for keys (the controller asks Cloud; another server asks
// the controller).
type Source func(ctx context.Context) (cloudapi.StorageKeys, error)

// Cache is one set of keys (a server's backups, or the lab's prune).
type Cache struct {
	Path   string
	Source Source
	Now    func() time.Time

	mu sync.Mutex
}

// Refresh and lastUse: keys are renewed when they have less than an hour
// left, and not used with under five minutes (a restic command must
// finish before they expire; backups are many short commands).
const (
	refresh = time.Hour
	lastUse = 5 * time.Minute
)

// Keys are the keys to use now: those kept, while fresh enough, or new
// ones.
func (c *Cache) Keys(ctx context.Context) (cloudapi.StorageKeys, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	kept, keptErr := c.read()
	if keptErr == nil && kept.Expires.Sub(now) > refresh {
		return kept, nil
	}
	fresh, err := c.Source(ctx)
	if err == nil {
		// Kept for next time; if that fails, these still work now.
		_ = c.write(fresh)
		return fresh, nil
	}
	if keptErr == nil && kept.Expires.Sub(now) > lastUse {
		return kept, nil // Cloud out of reach: carry on with what's kept
	}
	return cloudapi.StorageKeys{}, fmt.Errorf("asking for Roostack Cloud backup's keys: %w", err)
}

// Env is restic's environment for the keys.
func (c *Cache) Env(ctx context.Context) ([]string, error) {
	k, err := c.Keys(ctx)
	if err != nil {
		return nil, err
	}
	return Env(k), nil
}

// Env is restic's environment for keys.
func Env(k cloudapi.StorageKeys) []string {
	return []string{"AWS_ACCESS_KEY_ID=" + k.AccessKeyID, "AWS_SECRET_ACCESS_KEY=" + k.SecretAccessKey,
		"AWS_SESSION_TOKEN=" + k.SessionToken, "AWS_DEFAULT_REGION=" + k.Region}
}

func (c *Cache) read() (cloudapi.StorageKeys, error) {
	var k cloudapi.StorageKeys
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return k, err
	}
	if err := json.Unmarshal(data, &k); err != nil || k.AccessKeyID == "" {
		return k, errors.New("kept keys don't read")
	}
	return k, nil
}

func (c *Cache) write(k cloudapi.StorageKeys) error {
	data, _ := json.Marshal(k)
	tmp := c.Path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path)
}
