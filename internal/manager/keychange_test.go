package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/backup/restictest"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
)

// Changing the recovery key (SPEC.md §6.9) on real restic repositories:
// the new key is added everywhere before it becomes the lab's key; a
// repository that can't be reached keeps the change waiting, with the old
// key still working; at the end only the new key opens anything.
func TestKeyChange(t *testing.T) {
	t.Parallel()
	bin := restictest.Bin(t)
	ctx := context.Background()
	m, _ := newManager(t)
	m.Lab.NodeID = "n-0000000001"
	m.Activity = activity.New()
	st := storetest.Open(t)
	m.BackupStore = st
	keyFile := backup.KeyFile(m.Lab.Paths)
	cloud := &fakeCloudBackup{keyFile: keyFile}
	m.CloudBackup = cloud
	os.MkdirAll(filepath.Dir(keyFile), 0o700)
	os.WriteFile(keyFile, []byte("the old recovery key\n"), 0o600)
	dir := t.TempDir()
	m.OpenRepo = func(url string, env []string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: url, PasswordFile: keyFile, CacheDir: filepath.Join(dir, "cache")}
	}
	// The controller's main backup (set up by its wizard) and a copy on a
	// drive.
	main, drive := filepath.Join(dir, "main"), filepath.Join(dir, "drive")
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: m.Lab.NodeID, Provider: "setup", Repository: main})
	st.SetBackupCopy(ctx, store.BackupTarget{ServerID: m.Lab.NodeID, Provider: "drive:usb", Repository: drive})
	for _, url := range []string{main, drive} {
		if err := m.OpenRepo(url, nil).Init(ctx); err != nil {
			t.Fatal(err)
		}
	}
	opens := func(url, key string) bool {
		f := filepath.Join(t.TempDir(), "k")
		os.WriteFile(f, []byte(key+"\n"), 0o600)
		r := m.OpenRepo(url, nil)
		r.PasswordFile = f
		_, err := r.Keys(ctx)
		return err == nil
	}

	if err := m.KeyChanges().StartKeyChange(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.KeyChanges().StartKeyChange(ctx); err == nil {
		t.Error("only one change at a time")
	}
	newKey, err := m.KeyChanges().NewRecoveryKey()
	if err != nil || len(newKey) < 32 || newKey == "the old recovery key" {
		t.Fatalf("new key %q %v", newKey, err)
	}
	if err := m.KeyChanges().ConfirmKeyChange(ctx); err != nil {
		t.Fatal(err)
	}
	if k, _ := m.KeyChanges().KeyChangeStatus(); len(k.Repos) != 2 || k.Done() != 0 {
		t.Errorf("the backups to change are listed when confirmed: %+v", k)
	}
	if _, err := m.KeyChanges().NewRecoveryKey(); err == nil {
		t.Error("once confirmed, the new key isn't shown here again (Show recovery key shows the lab's key)")
	}

	// The drive is unplugged: the main backup gets the new key, and the
	// change waits; the lab's key is still the old one.
	os.Rename(drive, drive+".away")
	if err := m.KeyChanges().KeyChangeJob().Run(ctx, nil); err == nil {
		t.Fatal("should wait for the drive")
	}
	k, _ := m.KeyChanges().KeyChangeStatus()
	if k == nil || k.Phase != KeyAdding || k.Done() != 1 || len(k.Repos) != 2 {
		t.Fatalf("waiting: %+v", k)
	}
	if data, _ := os.ReadFile(keyFile); strings.TrimSpace(string(data)) != "the old recovery key" {
		t.Error("the lab's key changed before every backup had the new one")
	}
	if !opens(main, newKey) || !opens(main, "the old recovery key") {
		t.Error("main backup: both keys should open it for now")
	}

	// Back: the change finishes.
	os.Rename(drive+".away", drive)
	if err := m.KeyChanges().KeyChangeJob().Run(ctx, nil); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	if k, err := m.KeyChanges().KeyChangeStatus(); k != nil || err != nil {
		t.Errorf("still in progress: %+v %v", k, err)
	}
	if data, _ := os.ReadFile(keyFile); strings.TrimSpace(string(data)) != newKey {
		t.Error("the new key should be the lab's key")
	}
	// Roostack Cloud is told the new key's identity once it's the lab's.
	if len(cloud.registered) != 1 || cloud.registered[0] != newKey {
		t.Errorf("registered with Cloud: %d times (want once, with the new key)", len(cloud.registered))
	}
	for _, url := range []string{main, drive} {
		if !opens(url, newKey) || opens(url, "the old recovery key") {
			t.Errorf("%s: only the new key should open it", url)
		}
	}
	for _, f := range []string{keyFile + ".new", keyFile + ".old"} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should be gone", f)
		}
	}
}

// A new key that wasn't confirmed can be dropped; nothing changed.
func TestKeyChangeCancel(t *testing.T) {
	m, _ := newManager(t)
	keyFile := backup.KeyFile(m.Lab.Paths)
	if err := m.KeyChanges().StartKeyChange(context.Background()); err == nil {
		t.Error("no recovery key yet")
	}
	os.MkdirAll(filepath.Dir(keyFile), 0o700)
	os.WriteFile(keyFile, []byte("key\n"), 0o600)
	if err := m.KeyChanges().StartKeyChange(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.KeyChanges().CancelKeyChange(); err != nil {
		t.Fatal(err)
	}
	if k, _ := m.KeyChanges().KeyChangeStatus(); k != nil {
		t.Error("cancelled")
	}
	if data, _ := os.ReadFile(keyFile); string(data) != "key\n" {
		t.Error("the key must not change")
	}
}
