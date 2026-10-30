package cloud

import (
	"context"
	"testing"

	"roostack.cloud/roostack/cloudapi"
)

// TestPublic: the lab tells Cloud what serves its public addresses when
// they change and at each check-in, keeps Cloud's answer, and hears of an
// approval at the next check-in; a Cloud from before it carries
// everything.
func TestPublic(t *testing.T) {
	f, s, _ := fakeCloud(t)
	s.UseName = func(context.Context, cloudapi.NameClaim) error { return nil }
	ctx := context.Background()
	photos := cloudapi.PublicHost{Host: "photos.smiths.roost.test", Kind: cloudapi.HostRegistry, Package: "immich"}
	shots := cloudapi.PublicHost{Host: "shots.smiths.roost.test", Kind: cloudapi.HostRegistry, Package: "zipline", Approval: true}
	mine := cloudapi.PublicHost{Host: "mine.smiths.roost.test", Kind: cloudapi.HostOwn}
	hosts := []cloudapi.PublicHost{shots, photos, mine}
	s.Public = func() []cloudapi.PublicHost { return hosts }
	changed := 0
	s.PublicChanged = func() { changed++ }

	// Not linked: nothing is sent, and Cloud has no say.
	if err := s.SendPublic(ctx); err != nil || f.Requests["PUT "+cloudapi.PathLabPublic] != 0 {
		t.Fatalf("sent while unlinked: %v, %v", err, f.Requests)
	}
	if refused, known := s.PublicVerdict(mine); refused != nil || !known {
		t.Errorf("unlinked: %v %v", refused, known)
	}

	link(t, f, s, "smiths")
	// Linked, before Cloud has answered: what it may refuse waits.
	if _, known := s.PublicVerdict(mine); known {
		t.Error("known before Cloud answered")
	}
	if err := s.SendPublic(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.Public[s.Status().LabID].Hosts; len(got) != 3 || got[0] != mine || got[1] != photos || got[2] != shots {
		t.Errorf("Cloud was told %+v", got)
	}
	for h, want := range map[cloudapi.PublicHost]string{photos: "", shots: cloudapi.NameNeedsApproval, mine: cloudapi.NameOwnPackage} {
		refused, known := s.PublicVerdict(h)
		if got := ""; !known {
			t.Errorf("%s: not known", h.Host)
		} else if refused != nil {
			got = refused.State
			if got != want || refused.Reason != "Roostack Cloud will allow it once your account is approved." {
				t.Errorf("%s: %+v, want %q", h.Host, refused, want)
			}
		} else if want != "" {
			t.Errorf("%s: carried, want %q", h.Host, want)
		}
	}
	if changed != 1 || len(s.Status().Names) != 2 {
		t.Errorf("changed %d times; names %+v", changed, s.Status().Names)
	}
	// Unchanged: not sent again; changed what serves it: unknown until sent.
	s.SendPublic(ctx)
	if n := f.Requests["PUT "+cloudapi.PathLabPublic]; n != 1 {
		t.Errorf("sent %d times", n)
	}
	unflagged := shots
	unflagged.Approval = false
	if _, known := s.PublicVerdict(unflagged); known {
		t.Error("known for what Cloud wasn't told")
	}

	// The account approved: the next check-in says so, and tells Cloud again.
	f.Approved = true
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if refused, known := s.PublicVerdict(mine); refused != nil || !known || changed != 2 || len(s.Status().Names) != 0 {
		t.Errorf("after approval: %v %v, changed %d, names %+v", refused, known, changed, s.Status().Names)
	}
	if n := f.Requests["PUT "+cloudapi.PathLabPublic]; n != 2 {
		t.Errorf("check-in sent %d in all", n)
	}
	// Other names Cloud has on hold stay as they were.
	f.Approved = false
	f.NameStates = []cloudapi.NameStatus{{Host: "paypal.smiths.roost.test", State: cloudapi.NameRefused}}
	s.Check(ctx)
	if names := s.Status().Names; len(names) != 3 || s.st.onHold() {
		t.Errorf("with a refused name: %+v, on hold %v", names, s.st.onHold())
	}

	// A Cloud from before it: everything is carried.
	f.NoPublic, f.NameStates = true, nil
	hosts = append(hosts, cloudapi.PublicHost{Host: "notes.smiths.roost.test", Kind: cloudapi.HostOwn})
	if err := s.SendPublic(ctx); err != nil {
		t.Fatal(err)
	}
	if refused, known := s.PublicVerdict(mine); refused != nil || !known {
		t.Errorf("older Cloud: %v %v", refused, known)
	}
}
