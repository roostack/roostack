package cloud

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/config"
)

// TestReport: after checking in, the lab tells Cloud its report (cut to
// what Cloud keeps); a Cloud from before reports (404) or a report that
// can't be made doesn't fail the check.
func TestReport(t *testing.T) {
	f, s, _ := fakeCloud(t)
	link(t, f, s, "")
	ctx := context.Background()
	emails := make([]string, cloudapi.MaxReportEmails+3)
	for i := range emails {
		emails[i] = "someone@example.com"
	}
	last := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	s.Report = func(context.Context) (cloudapi.LabReport, error) {
		return cloudapi.LabReport{Emails: emails, Backups: []cloudapi.ServerBackup{{Server: "n-1", Name: "nuc", Last: last}}}, nil
	}
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	r := f.Reports[s.Status().LabID]
	if len(r.Emails) != cloudapi.MaxReportEmails || len(r.Backups) != 1 || r.Backups[0].Name != "nuc" || !r.Backups[0].Last.Equal(last) {
		t.Errorf("Cloud was told %+v", r)
	}
	reported := s.Status().Reported
	if reported.IsZero() {
		t.Error("the report's time isn't kept")
	}

	// An older Cloud: no reports, and no fuss.
	f.NoReports = true
	f.Reports = nil
	if err := s.Check(ctx); err != nil || !s.Status().Reported.Equal(reported) || s.Status().LastError != "" {
		t.Errorf("an older Cloud: %v, %+v", err, s.Status())
	}
	// A report that can't be made is tried at the next check.
	f.NoReports = false
	s.Report = func(context.Context) (cloudapi.LabReport, error) { return cloudapi.LabReport{}, errors.New("no users") }
	if err := s.Check(ctx); err != nil || f.Reports != nil {
		t.Errorf("a report that failed: %v, %+v", err, f.Reports)
	}
}

// TestReportName: the report carries the lab's name as it is now (the
// wizard and Settings rename it while Roostack runs), left out when Cloud
// wouldn't take it, so Cloud keeps the one it has.
func TestReportName(t *testing.T) {
	f, s, _ := fakeCloud(t)
	name := "My lab"
	s.labName = func() string { return name }
	ctx := context.Background()
	// Linked from the wizard's address step: named "Test" by then, though
	// Roostack started before it was named.
	name = "Test"
	open, err := s.Start(ctx, "", "")
	if err != nil || open == "" {
		t.Fatal(err)
	}
	if lab, _ := f.Asked(s.Status().Waiting.Code); lab != "Test" {
		t.Errorf("linked as %q", lab)
	}
	if err := f.Confirm(s.Status().Waiting.Code); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.Status().Linked })
	s.Report = func(context.Context) (cloudapi.LabReport, error) { return cloudapi.LabReport{}, nil }
	for _, c := range []struct{ name, want string }{
		{"Test", "Test"},
		{"  The Smiths' lab ", "The Smiths' lab"},
		{strings.Repeat("a", 70) + ".example.com", ""},
		{"Two\nlines", ""},
	} {
		name = c.name
		if err := s.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.Reports[s.Status().LabID].Name; got != c.want {
			t.Errorf("named %q: Cloud was told %q", c.name, got)
		}
	}
}

// TestStanding: what Cloud says of the lab when it checks in (an account
// or a name suspended, a name waiting or refused) is kept and shown, and
// the lab checks in hourly while something's on hold; a claimed name
// that waits says so at once.
func TestStanding(t *testing.T) {
	f, s, _ := fakeCloud(t)
	s.UseName = func(context.Context, cloudapi.NameClaim) error { return nil }
	link(t, f, s, "")
	ctx := context.Background()
	if s.st.onHold() {
		t.Error("on hold before anything is")
	}
	f.SuspendedFor = "Phishing"
	f.NameStates = []cloudapi.NameStatus{{Host: "photos.smiths.roost.test", State: cloudapi.NameSuspended, Reason: "Phishing", Since: time.Now()}}
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Linked || st.Suspended != "Phishing" || len(st.Names) != 1 || st.Names[0].State != cloudapi.NameSuspended || st.Refused != "" {
		t.Errorf("suspended: %+v", st)
	}
	if !s.st.onHold() {
		t.Error("not on hold while suspended")
	}
	// Refused only: nothing for Cloud to decide, so daily checks again.
	f.SuspendedFor = ""
	f.NameStates = []cloudapi.NameStatus{{Host: "paypal.smiths.roost.test", State: cloudapi.NameRefused, Reason: "A bank's"}}
	s.Check(ctx)
	if st := s.Status(); st.Suspended != "" || len(st.Names) != 1 || st.Names[0].Host != "paypal.smiths.roost.test" || s.st.onHold() {
		t.Errorf("after the lift: %+v", st)
	}

	// A claim that waits for approval.
	f.NameStates = []cloudapi.NameStatus{{Host: "mygov.roost.test", State: cloudapi.NameWaiting}}
	if err := s.ClaimName(ctx, "mygov"); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); len(st.Names) != 2 || st.Names[1].Host != "mygov.roost.test" || st.Names[1].State != cloudapi.NameWaiting {
		t.Errorf("after claiming a protected name: %+v", st.Names)
	}
	// An older Cloud says nothing: nothing is shown.
	f.NameStates = nil
	s.Check(ctx)
	if st := s.Status(); len(st.Names) != 0 {
		t.Errorf("after Cloud says nothing: %+v", st.Names)
	}
}

// TestRelayTurnedOff: the lab hears its account turned the relay off at
// Cloud (and that it's suspended) when it asks how the relay stands; its
// own updates leave it off, and TurnRelayOn turns it on.
func TestRelayTurnedOff(t *testing.T) {
	f, s, _ := fakeCloud(t)
	s.WireGuard = &fakeWG{}
	link(t, f, s, "")
	ctx := context.Background()
	if err := s.TurnRelayOn(ctx); err == nil {
		t.Error("turned on a relay there isn't")
	}
	hosts := []string{"smiths.roost.test"}
	if err := s.Relay(ctx, hosts, config.Countries{}, "Australia/Sydney", ""); err != nil {
		t.Fatal(err)
	}
	if off, sus := s.RelayStanding(); !off.IsZero() || sus != "" {
		t.Fatalf("on: %v %q", off, sus)
	}
	f.RelayOff, f.RelaySuspended = time.Now(), "Phishing"
	refresh := func(want func() bool) {
		t.Helper()
		rctx, stop := context.WithCancel(ctx)
		defer stop()
		go s.RefreshRelay(rctx, 5*time.Millisecond)
		waitFor(t, want)
	}
	refresh(func() bool { off, sus := s.RelayStanding(); return !off.IsZero() && sus == "Phishing" })
	if !s.st.onHold() {
		t.Error("not on hold while the relay is suspended")
	}
	// The lab's own updates don't turn it on.
	if err := s.Relay(ctx, append(hosts, "photos.smiths.roost.test"), config.Countries{}, "Australia/Sydney", ""); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Relayed {
		if r.TurnOn {
			t.Error("an update asked to turn the relay on")
		}
	}
	if off, _ := s.RelayStanding(); off.IsZero() {
		t.Error("an update's answer lost that it's off")
	}
	// The Owner does, with the names last asked.
	if err := s.TurnRelayOn(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Relayed {
		if !r.TurnOn || !slices.Equal(r.Hosts, []string{"photos.smiths.roost.test", "smiths.roost.test"}) || r.TimeZone != "Australia/Sydney" {
			t.Errorf("turning on asked %+v", r)
		}
	}
	if off, _ := s.RelayStanding(); !off.IsZero() || !f.RelayOff.IsZero() {
		t.Errorf("still off: %v", off)
	}
}
