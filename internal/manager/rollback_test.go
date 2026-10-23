package manager

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/s3api"
	"roostack.cloud/roostack/internal/s3api/s3fake"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
	"roostack.cloud/roostack/internal/usererr"
)

// s3Lab is a manager with an online storage account (provider:1, not
// Backblaze) whose bucket is b, and a server n-0000000002 backing up
// there.
func s3Lab(t *testing.T, b *s3fake.Bucket) (*Manager, *store.Store) {
	t.Helper()
	m, _ := newManager(t)
	m.Lab.NodeID = "n-0000000001"
	m.Activity = activity.New()
	st := storetest.Open(t)
	m.BackupStore = st
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	m.NewS3 = func(s3 S3Provider) *s3api.Client {
		return &s3api.Client{Endpoint: srv.URL, Bucket: b.Name, AccessKey: s3.AccessKey, SecretKey: s3.SecretKey}
	}
	ctx := context.Background()
	if err := m.AddS3Provider(ctx, "Wasabi", S3Provider{Endpoint: "https://s3.wasabisys.com", Bucket: b.Name,
		AccessKey: "wasabi-id", SecretKey: "wasabi-key"}); err != nil {
		t.Fatal(err)
	}
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: "n-0000000002", Provider: "provider:1",
		Repository: "s3:https://s3.wasabisys.com/" + b.Name + "/x", Created: time.Now().Add(-24 * time.Hour)})
	return m, st
}

// A bucket counts as safe from a taken-over server only when it keeps old
// versions, the key can't erase them (the probe shows it), and they're
// kept long enough (SPEC.md §10.10).
func TestS3BucketKeepsWhatsDeleted(t *testing.T) {
	b := &s3fake.Bucket{Name: "b"}
	m, _ := s3Lab(t, b)
	ctx := context.Background()
	access := func() Access {
		os.Remove(m.s3ChecksFile()) // check again
		return m.BackupAccess(ctx, "n-0000000002", "provider:1")
	}
	if a := access(); !a.CanDelete || a.Versioned || a.Safe() {
		t.Errorf("no versions: %+v", a)
	}
	if len(b.Calls) != 1 {
		t.Errorf("without versions, nothing is written: %v", b.Calls)
	}
	// Versions, but the key can erase them: rollback works, not safe.
	b.Versioning = "Enabled"
	if a := access(); !a.CanDelete || !a.Versioned || a.Safe() {
		t.Errorf("a key that can erase: %+v", a)
	}
	// A key that can't erase them, and no rule: kept for good.
	b.NoErase = true
	if a := access(); a.CanDelete || !a.HidesOnly || a.KeepsHidden != 0 || !a.Safe() {
		t.Errorf("a key that can't erase: %+v", a)
	}
	labID, _ := m.labID(ctx)
	probe := "roostack/" + labID + "/roostack-check"
	if _, ok := b.Current(probe); ok || b.Versions(probe) < 2 {
		t.Errorf("the probe is hidden, its versions kept (%d)", b.Versions(probe))
	}
	// A rule that erases old versions too soon.
	b.Lifecycle = `<LifecycleConfiguration><Rule><Status>Enabled</Status><Filter><Prefix>roostack/</Prefix></Filter>
	  <NoncurrentVersionExpiration><NoncurrentDays>7</NoncurrentDays></NoncurrentVersionExpiration></Rule></LifecycleConfiguration>`
	if a := access(); !a.HidesOnly || a.KeepsHidden != 7 || a.Safe() {
		t.Errorf("seven days: %+v", a)
	}
	b.Lifecycle = strings.Replace(b.Lifecycle, ">7<", ">30<", 1)
	if a := access(); !a.Safe() {
		t.Errorf("thirty days: %+v", a)
	}
	// The check holds (no probe on every page), until the key changes.
	calls := len(b.Calls)
	m.BackupAccess(ctx, "n-0000000002", "provider:1")
	if len(b.Calls) != calls {
		t.Error("a saved check is used")
	}
	if choices, _ := m.BackupChoices(ctx); len(choices) == 0 || !strings.Contains(choices[len(choices)-1].Note, "keeps what's deleted") {
		t.Errorf("choices = %+v", choices)
	}
	// Can't be asked (no such bucket): not counted.
	s3 := m.NewS3
	m.NewS3 = func(p S3Provider) *s3api.Client { c := s3(p); c.Bucket = "elsewhere"; return c }
	if a := access(); !a.CanDelete || a.Versioned {
		t.Errorf("no answer: %+v", a)
	}

	// The weekly prune checks again, and says so.
	m.NewS3 = s3
	m.OpenRepo = func(url string, env []string) *backup.Repo { return &backup.Repo{Bin: "true", URL: url} }
	if err := m.PruneJob().Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	l, _ := m.Activity.Latest(activity.Backup)
	if log := strings.Join(l.Lines, "\n"); !strings.Contains(log, "Checking whether Wasabi keeps what's deleted") ||
		!strings.Contains(log, "Old versions are kept for 30 days") {
		t.Errorf("prune log:\n%s", log)
	}
}

// A taken-over server hid a backup and some data, overwrote the config
// and added a backup of its own; rolling back to before then puts every
// file back as it was, erasing nothing, leaves lock files alone, and can
// itself be undone (SPEC.md §10.10).
func TestRollBack(t *testing.T) {
	t.Parallel()
	for _, markersFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "AWS", true: "Backblaze"}[markersFirst], func(t *testing.T) { testRollBack(t, markersFirst) })
	}
}

// testRollBack rolls back with versions listed as AWS does, or as
// Backblaze does (delete markers first).
func testRollBack(t *testing.T, markersFirst bool) {
	now := time.Now().Add(-10 * time.Hour)
	at := func(h int) time.Time { return now.Add(time.Duration(h) * time.Hour) }
	clock := now
	b := &s3fake.Bucket{Name: "b", Versioning: "Enabled", NoErase: true, PageSize: 3, MarkersFirst: markersFirst,
		Now: func() time.Time { return clock }}
	m, _ := s3Lab(t, b)
	ctx := context.Background()
	labID, _ := m.labID(ctx)
	folder := "roostack/" + labID + "/n-0000000002/"
	clock = at(1)
	for _, f := range []string{"config", "keys/k1", "data/d1", "data/d2", "index/i1", "snapshots/s1", "snapshots/s2", "locks/l1"} {
		b.Put(folder+f, []byte("good "+f))
	}
	// Elsewhere: not touched.
	b.Put("roostack/"+labID+"/n-0000000003/snapshots/s9", []byte("another server's"))
	clock = at(3) // the attack
	b.Hide(folder + "snapshots/s1")
	b.Hide(folder + "data/d1")
	b.Put(folder+"config", []byte("garbage"))
	b.Put(folder+"snapshots/evil", []byte("evil"))
	b.Hide(folder + "locks/l1")
	b.Put(folder+"locks/l2", []byte("lock"))
	b.Hide("roostack/" + labID + "/n-0000000003/snapshots/s9")

	var submitted []maintenance.Job
	m.Submit = func(j maintenance.Job) { submitted = append(submitted, j) }
	dir := t.TempDir()
	restic := filepath.Join(dir, "restic")
	os.WriteFile(restic, []byte("#!/bin/sh\ncase \"$1\" in snapshots) echo '[]';; unlock) echo 'successfully removed 1 locks';; esac\n"), 0o755)
	m.OpenRepo = func(url string, env []string) *backup.Repo { return &backup.Repo{Bin: restic, URL: url} }
	kind := activity.BackupRollbackFor("n-0000000002", "provider:1")
	check := func(before time.Time) []string {
		t.Helper()
		if err := m.CheckRollback(ctx, "n-0000000002", "provider:1", before); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if l, _ := m.Activity.Latest(kind); !l.Running() {
				if l.Err != "" {
					t.Fatalf("check: %s", l.Err)
				}
				return m.RollbackChecked("n-0000000002", "provider:1").Summary
			}
		}
		t.Fatal("the check didn't finish")
		return nil
	}

	// Nothing to roll back to before backups started, or in the future;
	// and not without a check first.
	for _, before := range []time.Time{time.Now().Add(-48 * time.Hour), time.Now().Add(time.Hour)} {
		var fe *usererr.Error
		if err := m.CheckRollback(ctx, "n-0000000002", "provider:1", before); !errors.As(err, &fe) {
			t.Errorf("before %v: %v", before, err)
		}
	}
	if err := m.RollBack(ctx, "n-0000000002", "provider:1", at(2)); err == nil {
		t.Error("a rollback needs a check first")
	}

	lines := check(at(2))
	want := []string{"1 backup (a snapshot) and 1 other file hidden since then come back.",
		"The repository's config file overwritten since then is put back as it was.",
		"1 backup (a snapshot) added since then is hidden."}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("check says:\n%s", strings.Join(lines, "\n"))
	}
	if err := m.RollBack(ctx, "n-0000000002", "provider:1", at(1)); err == nil {
		t.Error("only the time that was checked")
	}
	if err := m.RollBack(ctx, "n-0000000002", "provider:1", at(2)); err != nil || len(submitted) != 1 {
		t.Fatalf("rollback: %v (%d jobs)", err, len(submitted))
	}
	clock = at(5)
	if err := submitted[0].Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"config": "good config", "snapshots/s1": "good snapshots/s1", "data/d1": "good data/d1",
		"snapshots/s2": "good snapshots/s2", "locks/l2": "lock"} {
		if got, ok := b.Current(folder + f); !ok || string(got) != want {
			t.Errorf("%s = %q, %t; want %q", f, got, ok, want)
		}
	}
	for _, f := range []string{folder + "snapshots/evil", folder + "locks/l1", "roostack/" + labID + "/n-0000000003/snapshots/s9"} {
		if _, ok := b.Current(f); ok {
			t.Errorf("%s should stay hidden", f)
		}
	}
	if b.Versions(folder+"config") != 3 {
		t.Errorf("nothing is erased: config has %d versions", b.Versions(folder+"config"))
	}
	l, _ := m.Activity.Latest(kind)
	if log := strings.Join(l.Lines, "\n"); l.Err != "" || !strings.Contains(log, "Cleared 1 old lock") ||
		!strings.Contains(log, "backups are back as they were") {
		t.Errorf("rollback log (%s):\n%s", l.Err, log)
	}

	// Again: nothing changed since then.
	if lines := check(at(2)); len(lines) != 1 || !strings.Contains(lines[0], "Nothing in this backup has changed") {
		t.Errorf("again: %v", lines)
	}
	// Undone: back to just before the rollback.
	lines = check(at(4))
	if !strings.Contains(strings.Join(lines, "\n"), "added since then are hidden") ||
		!strings.Contains(strings.Join(lines, "\n"), "1 backup (a snapshot) hidden since then comes back") {
		t.Errorf("undo:\n%s", strings.Join(lines, "\n"))
	}
}
