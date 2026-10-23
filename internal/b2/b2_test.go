package b2

import (
	"context"
	"errors"
	"slices"
	"testing"

	"roostack.cloud/roostack/internal/b2/b2test"
)

// fakeB2 is a fake B2 (b2test) that accepts the master key with these
// capabilities and has the bucket "backups", with lifecycle rules if
// given; one nil rule leaves them out of its answer.
func fakeB2(t *testing.T, capabilities []string, rules ...map[string]any) *b2test.B2 {
	t.Helper()
	f := b2test.New(t)
	f.Keys["master-id master-key"] = capabilities
	f.Buckets["backups"] = b2test.Bucket{ID: "bkt1", Rules: rules}
	if len(rules) == 1 && rules[0] == nil {
		f.Buckets["backups"] = b2test.Bucket{ID: "bkt1", NoRules: true}
	}
	return f
}

func TestCreateKey(t *testing.T) {
	srv := fakeB2(t, []string{"listBuckets", "writeKeys", "deleteKeys"})
	c := &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
	ctx := context.Background()
	k, err := c.CreateKey(ctx, "roostack-lab1-n-0000000002", "backups", "roostack/lab1/n-0000000002/")
	if err != nil || k.ID != "scoped-1" || k.Secret != "scoped-1-secret" {
		t.Fatalf("key = %+v, %v", k, err)
	}
	got := srv.Created()[0]
	if got["bucketId"] != "bkt1" || got["namePrefix"] != "roostack/lab1/n-0000000002/" || got["keyName"] != "roostack-lab1-n-0000000002" {
		t.Errorf("created with %v", got)
	}
	// A server's key adds backups but can't erase them (SPEC.md §10.10).
	caps, _ := got["capabilities"].([]any)
	if len(caps) == 0 || slices.Contains(caps, any("deleteFiles")) || !slices.Contains(caps, any("writeFiles")) {
		t.Errorf("capabilities = %v", caps)
	}
	if _, err := c.CreateKey(ctx, "x", "other", "p/"); err == nil {
		t.Error("unknown bucket")
	}
	if err := c.DeleteKey(ctx, "scoped-1"); err != nil {
		t.Error(err)
	}
	bad := &Client{KeyID: "master-id", Key: "wrong", API: srv.URL}
	if _, err := bad.CreateKey(ctx, "x", "backups", "p/"); err == nil {
		t.Error("wrong key")
	}
}

func TestCantCreateKeys(t *testing.T) {
	srv := fakeB2(t, []string{"listFiles", "readFiles"})
	c := &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
	if _, err := c.CreateKey(context.Background(), "x", "backups", "p/"); !errors.Is(err, ErrCantCreateKeys) {
		t.Errorf("err = %v", err)
	}
}

func TestCanMakeKeys(t *testing.T) {
	ctx := context.Background()
	srv := fakeB2(t, []string{"listBuckets", "writeKeys"})
	c := &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
	if ok, err := c.CanMakeKeys(ctx, "backups"); !ok || err != nil {
		t.Errorf("can make keys = %v, %v", ok, err)
	}
	if _, err := c.CanMakeKeys(ctx, "other"); err == nil {
		t.Error("a bucket the key doesn't reach")
	}
	if _, err := (&Client{KeyID: "master-id", Key: "wrong", API: srv.URL}).CanMakeKeys(ctx, "backups"); err == nil {
		t.Error("wrong key")
	}
	// A key from Backblaze's website (docs/ASSUMPTIONS.md N6).
	srv = fakeB2(t, []string{"listBuckets", "listFiles", "readFiles", "writeFiles"})
	c = &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
	if ok, err := c.CanMakeKeys(ctx, "backups"); ok || err != nil {
		t.Errorf("can make keys = %v, %v", ok, err)
	}
}

func TestKeepsHidden(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		rules []map[string]any
		want  int
	}{
		{"default: keep all versions", nil, 0},
		{"keep only the last version", []map[string]any{{"fileNamePrefix": "", "daysFromHidingToDeleting": 1}}, 1},
		{"another folder's rule", []map[string]any{{"fileNamePrefix": "photos/", "daysFromHidingToDeleting": 1}}, 0},
		{"rules inside the folder count", []map[string]any{{"fileNamePrefix": "roostack/", "daysFromHidingToDeleting": 30},
			{"fileNamePrefix": "roostack/lab1/n-1/locks/", "daysFromHidingToDeleting": 2}}, 2},
		{"only hides, never erases", []map[string]any{{"fileNamePrefix": "", "daysFromUploadingToHiding": 7, "daysFromHidingToDeleting": nil}}, 0},
	} {
		srv := fakeB2(t, []string{"listBuckets"}, tc.rules...)
		c := &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
		if got, err := c.KeepsHidden(ctx, "backups", "roostack/lab1/n-1/"); err != nil || got != tc.want {
			t.Errorf("%s: %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}

// An answer without the bucket's rules isn't "kept for good".
func TestKeepsHiddenUnknown(t *testing.T) {
	srv := fakeB2(t, []string{"listBuckets"}, nil)
	c := &Client{KeyID: "master-id", Key: "master-key", API: srv.URL}
	if _, err := c.KeepsHidden(context.Background(), "backups", "roostack/lab1/n-1/"); err == nil {
		t.Error("no rules in the answer should be an error")
	}
}

func TestIsB2(t *testing.T) {
	if !IsB2("https://s3.us-west-004.backblazeb2.com") || IsB2("https://s3.wasabisys.com") || IsB2("https://evilbackblazeb2.com") {
		t.Error("IsB2")
	}
}
