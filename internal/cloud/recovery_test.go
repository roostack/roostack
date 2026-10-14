package cloud

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"slices"
	"testing"

	"roostack.cloud/roostack/cloudapi"
)

// The recovery identity is the same for the same recovery key (however it
// was typed), and another key's is different.
func TestRecoveryIdentity(t *testing.T) {
	a, err := RecoveryIdentity("abcDEF-123_xyz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RecoveryIdentity("  abcDEF-123_xyz\n")
	c, _ := RecoveryIdentity("abcDEF-123_xyZ")
	if !a.Equal(b) || a.Equal(c) {
		t.Error("the identity should depend on the key alone")
	}
	if _, err := RecoveryIdentity("  "); err == nil {
		t.Error("an empty key has no identity")
	}
}

// A linked lab registers its recovery identity; with the recovery key
// alone, a new install can then read the lab's backups in Cloud. A new
// recovery key replaces the identity, and the old key stops working.
func TestRestoreFromCloud(t *testing.T) {
	ctx := context.Background()
	f, s, _ := fakeCloud(t)
	key := "the first recovery key"
	s.RecoveryKey = func() (string, error) { return key + "\n", nil }
	f.RestoreRepositories = []string{"s3:https://storage.test/b/labs/l/n-1"}
	link(t, f, s, "")
	labID := s.Status().LabID
	id, _ := RecoveryIdentity(key)
	waitFor(t, func() bool { return bytes.Equal(f.Identities[labID], id.Public().(ed25519.PublicKey)) })

	acc, err := RestoreAccess(ctx, s.cfg, " "+key+" ")
	if err != nil {
		t.Fatal(err)
	}
	if acc.LabID != labID || acc.LabName != "Smith family" || !slices.Equal(acc.Repositories, f.RestoreRepositories) || acc.Keys.AccessKeyID == "" {
		t.Errorf("access = %+v", acc)
	}
	if _, err := RestoreAccess(ctx, s.cfg, "not the key"); err == nil {
		t.Error("another key was let in")
	}

	// A new recovery key: told at the next check (or at once, by the key
	// change), and only once.
	key = "the second recovery key"
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.Requests["PUT "+cloudapi.PathLabRecoveryIdentity]; n != 2 {
		t.Errorf("registered %d times, want 2 (linking, then the new key)", n)
	}
	if _, err := RestoreAccess(ctx, s.cfg, "the first recovery key"); err == nil {
		t.Error("the old recovery key still works")
	}
	if _, err := RestoreAccess(ctx, s.cfg, key); err != nil {
		t.Errorf("the new key: %v", err)
	}
}

// A lab whose certificate Cloud no longer accepts (it expired while the
// lab was off, or a restored backup held an old one) relinks with its
// recovery key at its next check; one that was unlinked can't, and
// forgets the link.
func TestRelinkWithRecoveryKey(t *testing.T) {
	ctx := context.Background()
	f, s, _ := fakeCloud(t)
	s.RecoveryKey = func() (string, error) { return "a recovery key", nil }
	link(t, f, s, "")
	labID := s.Status().LabID
	waitFor(t, func() bool { return f.Identities[labID] != nil })
	before := s.Status().CertFingerprint

	f.Revoke(labID)
	// Cloud busy (rate limited): the link is kept, and tried again.
	f.RestoreStatus = 429
	if err := s.Check(ctx); err == nil || !s.Status().Linked {
		t.Fatalf("relink rate limited: %v, linked %v", err, s.Status().Linked)
	}
	f.RestoreStatus = 0
	if err := s.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if st := s.Status(); !st.Linked || st.CertFingerprint == before || st.LastContact.IsZero() {
		t.Errorf("after relinking: %+v", st)
	}
	if !slices.Equal(f.Proven, []string{cloudapi.ProveRelink}) {
		t.Errorf("proofs = %v", f.Proven)
	}
	// The new certificate works for the lab's own requests.
	if err := s.Check(ctx); err != nil {
		t.Errorf("checking in with the new certificate: %v", err)
	}

	f.Unlink(labID)
	s.Check(ctx)
	if st := s.Status(); st.Linked || st.Forgotten.IsZero() {
		t.Errorf("an unlinked lab relinked: %+v", st)
	}
}

// A roost.ac name stops counting as the lab's address once the lab uses
// its own domain (found on VM3, 2026-10-13: pages still showed the name,
// and unlinking would have dropped the own domain); the same name keeps.
func TestLabDomain(t *testing.T) {
	f, s, _ := fakeCloud(t)
	link(t, f, s, "")
	s.mu.Lock()
	s.st.Name, s.st.Address = "smiths", "smiths.roost.test"
	s.mu.Unlock()
	s.LabDomain("Smiths.roost.test")
	if s.Status().Address != "smiths.roost.test" {
		t.Error("the lab's own roost.ac address was dropped")
	}
	dropped := 0
	s.Dropped = func(context.Context) { dropped++ }
	s.LabDomain("lab.example.com")
	if st := s.Status(); st.Address != "" || st.Name != "smiths" {
		t.Errorf("after choosing an own domain: %+v", st)
	}
	if _, err := s.Unlink(context.Background()); err != nil || dropped != 0 {
		t.Errorf("unlinking dropped the own domain: %v, %d", err, dropped)
	}
}
