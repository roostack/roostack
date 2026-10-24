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

// TestCache: keys are fetched once and kept (0600) for an hour, then
// fetched again, so that each command starts with nearly all of their 12
// hours; with Cloud out of reach, kept keys with a few minutes left still
// do, and none at all is an error.
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
	now = now.Add(50 * time.Minute)
	if _, err := c.Keys(ctx); err != nil || calls != 1 {
		t.Errorf("50 minutes old: %v, %d calls", err, calls)
	}
	now = now.Add(20 * time.Minute) // over an hour old, 11 hours left: fetched again
	k, err := c.Keys(ctx)
	if err != nil || calls != 2 || !k.Expires.Equal(now.Add(12*time.Hour)) {
		t.Errorf("an hour old: %v %v, %d calls", err, k.Expires, calls)
	}
	now = now.Add(11*time.Hour + 30*time.Minute) // half an hour left: fetched again
	fail = true
	if k, err := c.Keys(ctx); err != nil || calls != 3 || k.AccessKeyID != "K" {
		t.Errorf("Cloud out of reach, kept keys still valid: %v %+v, %d calls", err, k, calls)
	}
	now = now.Add(28 * time.Minute) // two minutes left: not enough
	if _, err := c.Keys(ctx); err == nil {
		t.Error("expiring keys used")
	}
	// Keys kept before the time they were fetched was: renewed.
	os.WriteFile(c.Path, []byte(`{"access_key_id":"OLD","secret_access_key":"S","session_token":"T","region":"r","expires":"`+
		now.Add(6*time.Hour).Format(time.RFC3339)+`"}`), 0o600)
	fail = false
	if k, err := c.Keys(ctx); err != nil || k.AccessKeyID != "K" {
		t.Errorf("older kept keys: %+v %v", k, err)
	}
	if env := Env(k); len(env) != 4 || env[2] != "AWS_SESSION_TOKEN=T" {
		t.Errorf("env: %v", env)
	}
}
