package cloudkeys

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
)

// TestCache: keys are fetched once and kept (0600) while they have over
// an hour left, then fetched again; with Cloud out of reach, kept keys
// with a few minutes left still do, and none at all is an error.
func TestCache(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	calls, fail := 0, false
	c := &Cache{Path: filepath.Join(t.TempDir(), "keys.json"), Now: func() time.Time { return now },
		Source: func(context.Context) (cloudapi.StorageKeys, error) {
			calls++
			if fail {
				return cloudapi.StorageKeys{}, errors.New("unreachable")
			}
			return cloudapi.StorageKeys{AccessKeyID: "K", SecretAccessKey: "S", SessionToken: "T", Region: "r", Expires: now.Add(12 * time.Hour)}, nil
		}}
	ctx := context.Background()
	if _, err := c.Keys(ctx); err != nil || calls != 1 {
		t.Fatalf("first: %v, %d calls", err, calls)
	}
	if fi, err := os.Stat(c.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("kept: %v %v", fi, err)
	}
	now = now.Add(10 * time.Hour)
	if _, err := c.Keys(ctx); err != nil || calls != 1 {
		t.Errorf("two hours left: %v, %d calls", err, calls)
	}
	now = now.Add(90 * time.Minute) // half an hour left: fetched again
	fail = true
	if k, err := c.Keys(ctx); err != nil || calls != 2 || k.AccessKeyID != "K" {
		t.Errorf("Cloud out of reach, kept keys still valid: %v %+v, %d calls", err, k, calls)
	}
	now = now.Add(28 * time.Minute) // two minutes left: not enough
	if _, err := c.Keys(ctx); err == nil {
		t.Error("expiring keys used")
	}
	fail = false
	env, err := c.Env(ctx)
	if err != nil || len(env) != 4 || env[2] != "AWS_SESSION_TOKEN=T" {
		t.Errorf("env: %v %v", env, err)
	}
}
