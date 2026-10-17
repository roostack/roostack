package cloud

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
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
// recovery key replaces the identity, and the old key stops working. A
// lab unlinked and linked again has two labs' backups with its key.
func TestRestoreFromCloud(t *testing.T) {
	ctx := context.Background()
	f, s, _ := fakeCloud(t)
	key := "the first recovery key"
	s.RecoveryKey = func() (string, error) { return key + "\n", nil }
	link(t, f, s, "")
	labID := s.Status().LabID
	f.RestoreRepositories = map[string][]string{labID: {"s3:https://storage.test/b/labs/l/n-1"}}
	id, _ := RecoveryIdentity(key)
	waitFor(t, func() bool { return bytes.Equal(f.Identities[labID], id.Public().(ed25519.PublicKey)) })
	// The registration was signed over the lab's ID (review M10): one
	// signed for another lab's, or unsigned, is refused.
	other, _ := RecoveryIdentity("another lab's key")
	pub := other.Public().(ed25519.PublicKey)
	for _, sig := range [][]byte{nil, ed25519.Sign(other, cloudapi.RestoreMessage(cloudapi.ProveRegister, "lab-other"))} {
		req := cloudapi.RecoveryIdentity{PublicKey: pub, Signature: sig}
		if err := s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabRecoveryIdentity, req, nil); err == nil {
			t.Errorf("registered with signature %x", sig)
		}
	}

	acc, err := RestoreAccess(ctx, s.cfg, " "+key+" ")
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.Labs) != 1 || acc.Labs[0].LabID != labID || acc.Labs[0].LabName != "Smith family" ||
		!slices.Equal(acc.Labs[0].Repositories, f.RestoreRepositories[labID]) || acc.Labs[0].Keys.AccessKeyID == "" {
		t.Errorf("access = %+v", acc)
	}
	if _, err := RestoreAccess(ctx, s.cfg, "not the key"); err == nil {
		t.Error("another key was let in")
	}

	// A new recovery key: told at the next check (or at once, by the key
	// change), and only once.
	registered := f.Requests["PUT "+cloudapi.PathLabRecoveryIdentity]
	key = "the second recovery key"
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.Requests["PUT "+cloudapi.PathLabRecoveryIdentity] - registered; n != 1 {
		t.Errorf("registered %d times for the new key, want 1", n)
	}
	if _, err := RestoreAccess(ctx, s.cfg, "the first recovery key"); err == nil {
		t.Error("the old recovery key still works")
	}
	if _, err := RestoreAccess(ctx, s.cfg, key); err != nil {
		t.Errorf("the new key: %v", err)
	}

	// Unlinked in Cloud, the lab forgets the link; linked again, it's a
	// new lab with the same key. Both labs' backups are offered, the
	// linked one first (review M10: only one was).
	f.Unlink(labID)
	s.Check(ctx)
	if s.Status().Linked {
		t.Fatal("still linked after Cloud unlinked it")
	}
	link(t, f, s, "")
	newID := s.Status().LabID
	waitFor(t, func() bool { return f.Identities[newID] != nil })
	acc, err = RestoreAccess(ctx, s.cfg, key)
	if err != nil || len(acc.Labs) != 2 || acc.Labs[0].LabID != newID || !acc.Labs[0].UnlinkedAt.IsZero() ||
		acc.Labs[1].LabID != labID || acc.Labs[1].UnlinkedAt.IsZero() || len(acc.Labs[1].Repositories) != 1 {
		t.Errorf("after linking again: %+v %v", acc, err)
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
