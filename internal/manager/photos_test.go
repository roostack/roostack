package manager

import (
	"context"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
)

// sentMail records the emails sent.
type sentMail struct{ sent []mail.Message }

func (f *sentMail) Send(_ context.Context, m mail.Message) error {
	f.sent = append(f.sent, m)
	return nil
}

// Someone who isn't an Owner migrating photos: the Owners hear when it
// starts, and the person and the Owners when it ends.
func TestPhotoEmails(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	p, err := m.Package("immich")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.KeepPackage(m.Lab.Paths, p.Name, p.Version); err != nil {
		t.Fatal(err)
	}
	if err := (&instance.Instance{Package: "immich", Version: p.Version, Title: "Family photos"}).Save(state.InstanceFile(m.Lab, "immich")); err != nil {
		t.Fatal(err)
	}
	st := storetest.Open(t)
	for _, u := range []store.User{{Username: "sam", Email: "sam@example.com", Role: auth.RoleOwner},
		{Username: "kim", Email: "kim@example.com", Role: auth.RoleMember}} {
		u.PasswordHash = "x"
		if _, err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	sent := &sentMail{}
	ph := m.Photos()
	ph.Mail, ph.Users = sent, st
	to := func() (out []string) {
		for _, msg := range sent.sent {
			out = append(out, strings.Join(msg.To, ",")+": "+msg.Subject)
		}
		sent.sent = nil
		return out
	}

	kim, _ := st.UserByUsername(ctx, "kim")
	ph.MigrationStarted(ctx, "immich", kim)
	if got, want := to(), []string{"sam@example.com: kim is migrating photos into Family photos"}; !slices.Equal(got, want) {
		t.Errorf("started: %q, want %q", got, want)
	}
	sam, _ := st.UserByUsername(ctx, "sam")
	if ph.MigrationStarted(ctx, "immich", sam); len(to()) != 0 {
		t.Error("an Owner's own migration emailed the Owners")
	}

	ph.MigrationEnded(ctx, node.PhotoSummary{Service: "immich", Who: "kim",
		Run: &node.PhotoRun{Tally: photoimport.Tally{Sent: 1200, Had: 50, Failed: 2}}})
	want := []string{"kim@example.com: Your photos are in Family photos", "sam@example.com: kim: Your photos are in Family photos"}
	if got := to(); !slices.Equal(got, want) {
		t.Errorf("ended: %q, want %q", got, want)
	}
	ph.MigrationEnded(ctx, node.PhotoSummary{Service: "immich", Who: "sam", Run: &node.PhotoRun{Stopped: true}})
	if got, want := to(), []string{"sam@example.com: Migrating photos into Family photos was stopped"}; !slices.Equal(got, want) {
		t.Errorf("an Owner's stopped: %q, want %q", got, want)
	}
}

// Members' shares (the security review of 2026-11-19, M4): off unless the
// Owner allows them, and then only on a NAS the lab already uses; Owners
// and Admins choose any.
func TestPhotoShareSource(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	p, err := m.Package("immich")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.KeepPackage(m.Lab.Paths, p.Name, p.Version); err != nil {
		t.Fatal(err)
	}
	if err := (&instance.Instance{Package: "immich", Version: p.Version}).Save(state.InstanceFile(m.Lab, "immich")); err != nil {
		t.Fatal(err)
	}
	st := storetest.Open(t)
	m.Settings, m.Libraries = &memKV{m: map[string]string{}}, st
	if err := st.SetMediaLibrary(ctx, store.MediaLibrary{ServerID: m.Lab.NodeID, Name: "films", Title: "Films",
		ShareType: "smb", ShareServer: "nas.home", SharePath: "films"}); err != nil {
		t.Fatal(err)
	}
	ph := m.Photos()
	share := func(server string) node.PhotoSource {
		return node.PhotoSource{Kind: "share", Library: agent.Library{Share: &agent.Share{Type: "smb", Server: server, Path: "kim"}}}
	}
	if err := ph.CheckPhotoSource(ctx, "immich", share("nas.home"), false); err == nil || !strings.Contains(err.Error(), "Shares can't be used here") {
		t.Errorf("a member's share, not allowed: %v", err)
	}
	if err := ph.CheckPhotoSource(ctx, "immich", share("evil.example"), true); err != nil {
		t.Errorf("an Admin's share: %v", err)
	}
	// As SetPhotoSettings keeps them, without a server to tell the memory.
	if err := store.PutJSON(ctx, m.Settings, photoSettingsKey("immich"), PhotoSettings{MemberShares: true}); err != nil {
		t.Fatal(err)
	}
	if got := ph.ShareServers(ctx); !slices.Equal(got, []string{"nas.home"}) {
		t.Errorf("share servers %q", got)
	}
	if err := ph.CheckPhotoSource(ctx, "immich", share("nas.home"), false); err != nil {
		t.Errorf("a member's share on the lab's NAS: %v", err)
	}
	if err := ph.CheckPhotoSource(ctx, "immich", share("evil.example"), false); err == nil || !strings.Contains(err.Error(), "servers listed") {
		t.Errorf("a member's share elsewhere: %v", err)
	}
}
