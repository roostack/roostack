package cloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/cloud/cloudtest"
	"roostack.cloud/roostack/internal/config"
)

// fakeCloud starts a fake Cloud over HTTPS and a lab's link to it.
func fakeCloud(t *testing.T) (*cloudtest.Fake, *Service, string) {
	t.Helper()
	PollEvery = 10 * time.Millisecond
	f, cfg := cloudtest.Start(t)
	state := t.TempDir()
	s, err := New(state, cfg, "test", func() string { return "Smith family" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return f, s, state
}

// link links s, confirming in f.
func link(t *testing.T, f *cloudtest.Fake, s *Service, name string) {
	t.Helper()
	open, err := s.Start(context.Background(), name, "")
	if err != nil {
		t.Fatal(err)
	}
	w := s.Status().Waiting
	if w == nil || w.Code == "" || open == "" {
		t.Fatalf("waiting = %+v, open %q", w, open)
	}
	if lab, asked := f.Asked(w.Code); lab != "Smith family" || asked != name {
		t.Fatalf("Cloud was sent %q, %q", lab, asked)
	}
	if err := f.Confirm(w.Code); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return s.Status().Linked })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 300 && !ok(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !ok() {
		t.Fatal("timed out")
	}
}

func TestLinkCheckUnlink(t *testing.T) {
	f, s, state := fakeCloud(t)
	link(t, f, s, "smiths")
	st := s.Status()
	if st.Account == "" || st.LabID == "" || st.Name != "smiths" || st.CertFingerprint == "" || st.Waiting != nil {
		t.Fatalf("status = %+v", st)
	}
	fi, err := os.Stat(filepath.Join(Dir(state), "lab.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	// The link survives a restart, and checking in uses the certificate.
	s2, err := New(state, s.cfg, "test", s.labName, s.log)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s2.Status().Linked || f.Requests["GET /v1/lab"] != 1 {
		t.Fatalf("after check: %+v, requests %v", s2.Status(), f.Requests)
	}
	reached, err := s2.Unlink(context.Background())
	if err != nil || !reached {
		t.Fatalf("unlink: %v %v", reached, err)
	}
	if s2.Status().Linked || len(f.Labs()) != 0 {
		t.Fatalf("still linked: %+v %v", s2.Status(), f.Labs())
	}
	if _, err := os.Stat(filepath.Join(Dir(state), "lab.key")); !os.IsNotExist(err) {
		t.Fatalf("key kept: %v", err)
	}
	reqs := s2.Requests()
	if len(reqs) == 0 || reqs[0].Method != "DELETE" {
		t.Fatalf("requests = %+v", reqs)
	}
	// Asking about the link again and again is one line.
	polls := 0
	for _, r := range reqs {
		if r.Path == cloudapi.PathLinkStatus {
			polls++
		}
	}
	if polls > 2 {
		t.Errorf("%d lines of polls: %+v", polls, reqs)
	}
}

// A lab unlinked in Cloud (or restored from a backup made before it was
// unlinked) forgets the link at its next check, and says so.
// A suspended account's lab stays linked and shows Cloud's message; only
// a 401 means Cloud doesn't know the certificate (review M6).
func TestSuspendedKeepsLink(t *testing.T) {
	f, s, _ := fakeCloud(t)
	link(t, f, s, "")
	f.Suspended = true
	if err := s.Check(context.Background()); err == nil || errors.Is(err, ErrUnlinked) {
		t.Fatalf("check = %v", err)
	}
	if st := s.Status(); !st.Linked || !st.Forgotten.IsZero() || st.Refused != "This account is suspended." || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	f.Suspended = false
	if err := s.Check(context.Background()); err != nil || s.Status().Refused != "" {
		t.Fatalf("after the suspension: %v, %+v", err, s.Status())
	}
}

func TestUnlinkedFromCloud(t *testing.T) {
	f, s, state := fakeCloud(t)
	link(t, f, s, "")
	// A backup of the state, while linked.
	backup := t.TempDir()
	copyDir(t, Dir(state), backup)
	f.Unlink(s.Status().LabID)
	if err := s.Check(context.Background()); !errors.Is(err, ErrUnlinked) {
		t.Fatalf("check = %v", err)
	}
	st := s.Status()
	if st.Linked || st.Forgotten.IsZero() || st.ForgottenAccount == "" {
		t.Fatalf("status = %+v", st)
	}
	if err := s.DismissForgotten(); err != nil || !s.Status().Forgotten.IsZero() {
		t.Fatalf("dismiss: %v", err)
	}

	// Restored: linked again on paper, until it checks in.
	os.RemoveAll(Dir(state))
	copyDir(t, backup, Dir(state))
	r, err := New(state, s.cfg, "test", s.labName, s.log)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Status().Linked {
		t.Fatal("restored link not loaded")
	}
	r.Check(context.Background())
	if r.Status().Linked || r.Status().Forgotten.IsZero() {
		t.Fatalf("restored: %+v", r.Status())
	}
}

func TestRenew(t *testing.T) {
	f, s, _ := fakeCloud(t)
	f.CertLife = time.Hour
	link(t, f, s, "")
	before := s.Status().CertFingerprint
	s.now = func() time.Time { return time.Now().Add(50 * time.Minute) }
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := s.Status().CertFingerprint; after == before || f.Requests["POST /v1/lab/renew"] != 1 {
		t.Fatalf("not renewed: %s %s %v", before, after, f.Requests)
	}
	// The renewed certificate works.
	s.now = time.Now
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelAndUnreachable(t *testing.T) {
	f, s, _ := fakeCloud(t)
	if _, err := s.Start(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	s.Cancel(context.Background())
	if s.Status().Waiting != nil || len(f.Codes()) != 0 {
		t.Fatalf("still waiting: %+v %v", s.Status(), f.Codes())
	}
	// The name an attempt asked for is kept once it ends without a link,
	// until the next one starts.
	if _, err := s.Start(context.Background(), "smiths", ""); err != nil || s.Status().TriedName != "" {
		t.Fatalf("start: %v, tried %q", err, s.Status().TriedName)
	}
	s.Cancel(context.Background())
	if s.Status().TriedName != "smiths" {
		t.Errorf("cancelled: tried %q", s.Status().TriedName)
	}
	a, err := s.NameAvailable(context.Background(), "taken")
	if err != nil || a.Available || a.Reason == "" {
		t.Fatalf("taken: %+v %v", a, err)
	}
	if _, err := s.Start(context.Background(), "taken", ""); err == nil || s.Status().StartError == "" {
		t.Fatalf("taken name started: %v", err)
	}

	gone, err := New(t.TempDir(), config.Cloud{URL: "https://127.0.0.1:1"}, "test", s.labName, s.log)
	if err != nil {
		t.Fatal(err)
	}
	var u *Unreachable
	if _, err := gone.Start(context.Background(), "", ""); !errors.As(err, &u) {
		t.Fatalf("start = %v", err)
	}
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	os.MkdirAll(to, 0o700)
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(to, e.Name()), data, 0o600)
	}
}

// Linking again after unlinking uses the new certificate: TLS sends one
// only when a connection opens, so connections made with the old one
// mustn't be reused.
func TestLinkAgain(t *testing.T) {
	f, s, _ := fakeCloud(t)
	link(t, f, s, "")
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unlink(context.Background()); err != nil {
		t.Fatal(err)
	}
	link(t, f, s, "")
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("check after linking again: %v", err)
	}
}

// A certificate from another Cloud (its authority unknown here) is refused
// in the TLS handshake itself: the lab is no longer linked, not "can't
// reach Cloud".
func TestRefusedInHandshake(t *testing.T) {
	f, s, state := fakeCloud(t)
	link(t, f, s, "")
	_, other := cloudtest.Start(t)
	s2, err := New(state, other, "test", s.labName, s.log)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Check(context.Background()); !errors.Is(err, ErrUnlinked) || s2.Status().Forgotten.IsZero() {
		t.Fatalf("check = %v, %+v", err, s2.Status())
	}
}

// TestNameSuffix: the suffix is the one Cloud says, kept once said;
// roost.ac before any Cloud has answered.
func TestNameSuffix(t *testing.T) {
	f, s, _ := fakeCloud(t)
	ctx := context.Background()
	if got := s.NameSuffix(ctx); got != "roost.test" {
		t.Errorf("from the fake: %q", got)
	}
	f.NameSuffix = "roostack.dev"
	if got := s.NameSuffix(ctx); got != "roost.test" {
		t.Errorf("kept for a day: %q", got)
	}
	gone, err := New(t.TempDir(), config.Cloud{URL: "https://127.0.0.1:1"}, "test", s.labName, s.log)
	if err != nil {
		t.Fatal(err)
	}
	if got := gone.NameSuffix(ctx); got != cloudapi.DefaultNameSuffix {
		t.Errorf("Cloud never reached: %q", got)
	}
}

// TestCheckClaim: a claimed name's login must write a challenge value,
// and for a public name the value must show in its zone's DNS, before
// the lab uses the name.
func TestCheckClaim(t *testing.T) {
	f, s, _ := fakeCloud(t)
	var claim cloudapi.NameClaim
	s.UseName = func(_ context.Context, c cloudapi.NameClaim) error { claim = c; return nil }
	link(t, f, s, "")
	ctx := context.Background()
	if err := s.ClaimName(ctx, "smiths"); err != nil || claim.Name != "smiths.roost.test" {
		t.Fatalf("claim: %v %+v", err, claim)
	}
	sub := claim.Credentials[cloudapi.CredSubdomain]
	if len(f.TXT[sub]) != 1 || len(f.TXT[sub][0]) != 43 {
		t.Errorf("the test value: %v", f.TXT[sub])
	}
	// A login the challenge server refuses.
	bad := claim
	bad.Credentials = maps.Clone(claim.Credentials)
	bad.Credentials[cloudapi.CredPassword] = "wrong"
	if err := s.checkClaim(ctx, bad); err == nil || !strings.Contains(err.Error(), "refused the name's login") {
		t.Errorf("a refused login: %v", err)
	}
	// A public name: its value has to show in DNS.
	public := claim
	public.Name = "smiths.roostack.dev"
	s.LookupTXT = func(_ context.Context, name string) ([]string, error) {
		if name != "_acme-challenge.smiths.roostack.dev" {
			t.Errorf("looked up %s", name)
		}
		return f.TXT[sub], nil
	}
	if err := s.checkClaim(ctx, public); err != nil {
		t.Errorf("seen in DNS: %v", err)
	}
	CheckWithin, CheckEvery = time.Millisecond, time.Millisecond
	defer func() { CheckWithin, CheckEvery = 45*time.Second, 3*time.Second }()
	s.LookupTXT = func(context.Context, string) ([]string, error) { return []string{"something else"}, nil }
	if err := s.checkClaim(ctx, public); err == nil || !strings.Contains(err.Error(), "didn't show in public DNS") {
		t.Errorf("not in DNS: %v", err)
	}
	// A claim whose check fails doesn't become the lab's address.
	claim = cloudapi.NameClaim{}
	f.NameSuffix = "roostack.dev"
	if err := s.ClaimName(ctx, "jones"); err == nil || claim.Name != "" || !strings.Contains(s.Status().AddressError, "didn't show in public DNS") {
		t.Errorf("used despite the check: %v %+v %q", err, claim, s.Status().AddressError)
	}
}

// fakeWG is the agent's WireGuard for the relay.
type fakeWG struct {
	applied []agent.WireGuard
	removed []string
	// fail is what applying returns (nil: it works).
	fail error
}

func (f *fakeWG) ApplyWireGuard(_ context.Context, wg agent.WireGuard) (string, error) {
	if err := wg.Validate(); err != nil {
		return "", err
	}
	if f.fail != nil {
		return "", f.fail
	}
	f.applied = append(f.applied, wg)
	return "bGFibGFibGFibGFibGFibGFibGFibGFibGFibGFibGE=", nil
}

func (f *fakeWG) RemoveWireGuard(_ context.Context, iface string) error {
	f.removed = append(f.removed, iface)
	return nil
}

// TestUnlinkedRelay: a relayed lab unlinked, here or in Cloud, says the
// relay went with the link (the lab's settings turn it off); one not
// relayed says nothing.
func TestUnlinkedRelay(t *testing.T) {
	ctx := context.Background()
	for _, inCloud := range []bool{false, true} {
		for _, relayed := range []bool{false, true} {
			f, s, _ := fakeCloud(t)
			s.WireGuard = &fakeWG{}
			unrelayed := 0
			s.Unrelayed = func(context.Context) { unrelayed++ }
			link(t, f, s, "")
			if relayed {
				if err := s.Relay(ctx, []string{"photos.smiths.roost.test"}, config.Countries{}, "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if inCloud {
				f.Unlink(s.Status().LabID)
				s.Check(ctx)
			} else if _, err := s.Unlink(ctx); err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{true: 1}[relayed]; unrelayed != want || s.Status().Linked {
				t.Errorf("in Cloud %v, relayed %v: Unrelayed called %d times", inCloud, relayed, unrelayed)
			}
		}
	}
}

// TestRelayEdgesChanged: an edge rebuilt with a new address and key
// (Cloud's `cloud edge-update`) reaches a relayed lab at its next refresh:
// the relay's WireGuard dials the new edge, then it's kept and the lab
// applies; dialling failing keeps the old edge for the next refresh, and
// the same edges again change nothing. Turning the relay on again takes
// changed edges too.
func TestRelayEdgesChanged(t *testing.T) {
	f, s, state := fakeCloud(t)
	wg := &fakeWG{}
	s.WireGuard = wg
	applied := make(chan struct{}, 4)
	s.EdgesChanged = func() { applied <- struct{}{} }
	ctx := context.Background()
	link(t, f, s, "")
	if err := s.Relay(ctx, []string{"photos.smiths.roost.test"}, config.Countries{}, "", ""); err != nil {
		t.Fatal(err)
	}
	old := s.RelayEdges()
	n := len(wg.applied)
	if err := s.refreshRelay(ctx); err != nil || len(wg.applied) != n || len(applied) != 0 {
		t.Errorf("the same edges: %v, %d applies, %d changes", err, len(wg.applied)-n, len(applied))
	}
	rebuilt := cloudapi.RelayEdge{Name: "syd-1", Endpoint: "203.0.113.20:51830",
		PublicKey: "bmV3ZWRnZW5ld2VkZ2VuZXdlZGdlbmV3ZWRnZW5ld2U=", Addr: "100.81.0.1", PublicIPv4: "203.0.113.20"}
	f.RelayEdges = []cloudapi.RelayEdge{rebuilt}
	wg.fail = errors.New("the agent is away")
	if err := s.refreshRelay(ctx); err == nil || !slices.Equal(s.RelayEdges(), old) || len(applied) != 0 {
		t.Errorf("dialling failed: %v, edges %+v", err, s.RelayEdges())
	}
	wg.fail = nil
	if err := s.refreshRelay(ctx); err != nil {
		t.Fatal(err)
	}
	last := wg.applied[len(wg.applied)-1]
	if len(last.Peers) != 1 || last.Peers[0].PublicKey != rebuilt.PublicKey || last.Peers[0].Endpoint != rebuilt.Endpoint ||
		last.Address != "100.81.1.1/16" {
		t.Errorf("the interface: %+v", last)
	}
	if got := s.RelayEdges(); !slices.Equal(got, f.RelayEdges) {
		t.Errorf("edges kept: %+v", got)
	}
	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Error("the lab didn't apply the new edges")
	}
	// Turning the relay on again, with an edge rebuilt meanwhile: dialled
	// at once (a later refresh would see nothing to change).
	again := rebuilt
	again.PublicKey, again.Endpoint = "YWdhaW5hZ2FpbmFnYWluYWdhaW5hZ2FpbmFnYWluYWc=", "203.0.113.30:51830"
	f.RelayEdges = []cloudapi.RelayEdge{again}
	if err := s.TurnRelayOn(ctx); err != nil {
		t.Fatal(err)
	}
	if last := wg.applied[len(wg.applied)-1]; len(last.Peers) != 1 || last.Peers[0].PublicKey != again.PublicKey ||
		!slices.Equal(s.RelayEdges(), f.RelayEdges) {
		t.Errorf("turned on: %+v, edges %+v", last, s.RelayEdges())
	}
	select {
	case <-applied:
	case <-time.After(5 * time.Second):
		t.Error("the lab didn't apply the edges it was turned on with")
	}
	// Kept across a restart (link.json).
	s2, err := New(state, s.cfg, "test", s.labName, s.log)
	if err != nil || !slices.Equal(s2.RelayEdges(), f.RelayEdges) {
		t.Errorf("after a restart: %+v %v", s2.RelayEdges(), err)
	}
}

// TestRelay: the relay's interface is made (its key) before Cloud is
// asked, then dials every edge at the address Cloud gave; the same names
// again change nothing; stopping tells Cloud and removes the interface.
func TestRelay(t *testing.T) {
	f, s, _ := fakeCloud(t)
	wg := &fakeWG{}
	s.WireGuard = wg
	ctx := context.Background()
	if err := s.Relay(ctx, []string{"photos.smiths.roost.test"}, config.Countries{}, "", ""); err == nil {
		t.Error("relayed before linking")
	}
	link(t, f, s, "")
	f.NoRelay = true
	if err := s.Relay(ctx, []string{"photos.smiths.roost.test"}, config.Countries{}, "", ""); err == nil || !strings.Contains(err.Error(), "isn't open") {
		t.Errorf("refused by Cloud: %v", err)
	}
	f.NoRelay = false
	wg.applied = nil
	if err := s.Relay(ctx, []string{"photos.smiths.roost.test", "smiths.roost.test"}, config.Countries{}, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(wg.applied) != 2 || len(wg.applied[0].Peers) != 0 || wg.applied[0].Interface != agent.RelayInterface {
		t.Fatalf("first the key: %+v", wg.applied)
	}
	final := wg.applied[1]
	if final.Address != "100.81.1.1/16" || len(final.Peers) != 1 || final.Peers[0].Endpoint != "198.51.100.10:51830" ||
		final.Peers[0].AllowedIPs[0] != "100.81.0.1/32" || final.Peers[0].Keepalive != 25 {
		t.Errorf("then the edges: %+v", final)
	}
	for _, rel := range f.Relayed {
		if rel.PublicKey != "bGFibGFibGFibGFibGFibGFibGFibGFibGFibGFibGE=" || len(rel.Hosts) != 2 {
			t.Errorf("Cloud was asked %+v", rel)
		}
	}
	if es := s.RelayEdges(); len(es) != 1 || es[0].Addr != "100.81.0.1" {
		t.Errorf("edges: %+v", es)
	}
	n := len(wg.applied)
	if err := s.Relay(ctx, []string{"smiths.roost.test", "photos.smiths.roost.test"}, config.Countries{}, "", ""); err != nil || len(wg.applied) != n {
		t.Errorf("the same names again: %v, %d applies", err, len(wg.applied)-n)
	}
	// Countries: Cloud is told, once.
	nz := config.Countries{Allow: []string{"NZ"}}
	if err := s.Relay(ctx, []string{"smiths.roost.test", "photos.smiths.roost.test"}, nz, "Australia/Sydney", ""); err != nil {
		t.Fatal(err)
	}
	for _, rel := range f.Relayed {
		if !slices.Equal(rel.Countries.Allow, []string{"NZ"}) || rel.TimeZone != "Australia/Sydney" {
			t.Errorf("countries: %+v, time zone %q", rel.Countries, rel.TimeZone)
		}
	}
	n = len(wg.applied)
	if s.Relay(ctx, []string{"smiths.roost.test", "photos.smiths.roost.test"}, nz, "Australia/Sydney", ""); len(wg.applied) != n {
		t.Error("the same countries again: Cloud asked again")
	}
	// Bans go to Cloud while relayed, at most MaxRelayBans of them.
	many := make([]string, cloudapi.MaxRelayBans+5)
	for i := range many {
		many[i] = fmt.Sprintf("203.0.%d.%d", i/250, i%250)
	}
	if err := s.sendBans(ctx, func(context.Context) ([]string, error) { return many, nil }); err != nil {
		t.Fatal(err)
	}
	for _, b := range f.Bans {
		if len(b) != cloudapi.MaxRelayBans || b[0] != "203.0.0.0" {
			t.Errorf("bans: %d, %v", len(b), b[:1])
		}
	}
	if len(f.Bans) != 1 {
		t.Errorf("bans sent for %d labs", len(f.Bans))
	}
	// Without CrowdSec (nothing public), the log says so once; a failure
	// while CrowdSec should be there is a warning each round.
	sendFor := func(err error) string {
		var buf bytes.Buffer
		logs := s.log
		s.log = slog.New(slog.NewTextHandler(&buf, nil))
		defer func() { s.log = logs }()
		bctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		s.SendBans(bctx, time.Millisecond, func(context.Context) ([]string, error) { return nil, err })
		return buf.String()
	}
	if out := sendFor(ErrNoCrowdSec); strings.Count(out, "\n") != 1 || !strings.Contains(out, "level=INFO") {
		t.Errorf("without CrowdSec:\n%s", out)
	}
	if out := sendFor(errors.New("asking CrowdSec: exit status 125")); strings.Count(out, "level=WARN") < 2 {
		t.Errorf("CrowdSec failing:\n%s", out)
	}
	// The lab's own domain: its relay name and proof; asked again while
	// it isn't proven (Cloud looks again), not once it is.
	own := []string{"vault.lab.example.com"}
	if err := s.Relay(ctx, own, nz, "Australia/Sydney", "lab.example.com"); err != nil {
		t.Fatal(err)
	}
	name, d := s.RelayName()
	if name != "r-test.relay.roost.test" || d == nil || d.Verified || d.ProofName != "_roostack.lab.example.com" || d.ProofValue == "" {
		t.Errorf("own domain: %q %+v", name, d)
	}
	f.DomainVerified = true
	if err := s.Relay(ctx, own, nz, "Australia/Sydney", "lab.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, d := s.RelayName(); d == nil || !d.Verified {
		t.Errorf("not asked again while unproven: %+v", d)
	}
	n = len(wg.applied)
	s.Relay(ctx, own, nz, "Australia/Sydney", "lab.example.com")
	if len(wg.applied) != n {
		t.Error("asked again once proven")
	}
	// Refreshed from Cloud: the proof gone missing.
	f.DomainVerified = false
	rctx, stop := context.WithCancel(ctx)
	go s.RefreshRelay(rctx, 10*time.Millisecond)
	for i := 0; i < 200; i++ {
		if _, d := s.RelayName(); d != nil && !d.Verified {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if _, d := s.RelayName(); d == nil || d.Verified {
		t.Errorf("after refreshing: %+v", d)
	}
	// Cloud backup: refused without a quota; keys for a server's folder.
	if _, err := s.StorageKeys(ctx, cloudapi.StorageRequest{Purpose: cloudapi.StorageBackup, Server: "n-1"}); err == nil {
		t.Error("storage keys without a quota")
	}
	f.StorageQuota = 1_000_000_000
	if k, err := s.StorageKeys(ctx, cloudapi.StorageRequest{Purpose: cloudapi.StorageBackup, Server: "n-1"}); err != nil || !strings.HasSuffix(k.Prefix, "/n-1/") {
		t.Errorf("storage keys: %+v %v", k, err)
	}
	if u, err := s.StorageUsage(ctx); err != nil || u.QuotaBytes != 1_000_000_000 {
		t.Errorf("usage: %+v %v", u, err)
	}
	// Statistics, asked for by the day.
	f.RelayStats = cloudapi.RelayStats{Periods: []cloudapi.RelayPeriod{{Day: "2026-10-12", Visitors: 5}}}
	if st, err := s.RelayStats(ctx, 30, true); err != nil || len(st.Periods) != 1 || f.StatsQuery != "days=30&by=day" {
		t.Errorf("stats: %+v %v (asked %q)", st, err, f.StatsQuery)
	}
	if err := s.StopRelay(ctx); err != nil || len(f.Relayed) != 0 || !slices.Contains(wg.removed, agent.RelayInterface) || s.RelayEdges() != nil {
		t.Errorf("stop: %v %v %v", err, f.Relayed, wg.removed)
	}
}
