package web

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/store"
)

// relayWG is the agent's WireGuard for the relay's interface.
type relayWG struct{}

func (relayWG) ApplyWireGuard(context.Context, agent.WireGuard) (string, error) {
	return "bGFibGFibGFibGFibGFibGFibGFibGFibGFibGFibGE=", nil
}
func (relayWG) RemoveWireGuard(context.Context, string) error { return nil }

// TestCloudStanding: what Roostack Cloud has on hold is shown on its
// settings page and in Needs attention (the account suspended, names
// suspended, refused or waiting), and Public access says when the relay
// was turned off in Cloud, with a button that turns it on, or that Cloud
// suspended it.
func TestCloudStanding(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	link, f := newCloudWith(t)
	h.server.Cloud = link
	link.UseName = func(context.Context, cloudapi.NameClaim) error { return nil }
	link.WireGuard = relayWG{}
	ctx := context.Background()
	if _, err := link.Start(ctx, "", ""); err != nil {
		t.Fatal(err)
	}
	f.Confirm(link.Status().Waiting.Code)
	for i := 0; i < 200 && !link.Status().Linked; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if err := link.ClaimName(ctx, "smiths"); err != nil {
		t.Fatal(err)
	}
	h.server.Domain = "smiths.roost.test"
	if _, body := h.get("/settings/cloud"); strings.Contains(body, "Names on hold") || strings.Contains(body, "has suspended") {
		t.Fatal("something on hold before Cloud said so")
	}

	f.SuspendedFor = "Phishing"
	f.NameStates = []cloudapi.NameStatus{
		{Host: "photos.smiths.roost.test", State: cloudapi.NameSuspended, Reason: "Phishing", Since: time.Now()},
		{Host: "paypal.smiths.roost.test", State: cloudapi.NameRefused, Reason: "It looks like PayPal's"},
		{Host: "nab.smiths.roost.test", State: cloudapi.NameWaiting, Since: time.Now()}}
	if err := link.Check(ctx); err != nil {
		t.Fatal(err)
	}
	_, body := h.get("/settings/cloud")
	for _, want := range []string{"Roostack Cloud has suspended this lab's account:</strong> Phishing.", `id="names"`,
		"<code>photos.smiths.roost.test</code> is suspended: Phishing.", "<code>paypal.smiths.roost.test</code> wasn't approved: It looks like PayPal&#39;s.",
		"<code>nab.smiths.roost.test</code> is waiting for Roostack Cloud's approval, since today"} {
		if !strings.Contains(body, want) {
			t.Errorf("the Cloud page: no %q", want)
		}
	}
	_, home := h.get("/")
	for _, want := range []string{"Roostack Cloud has suspended this lab&#39;s account: Phishing.", "photos.smiths.roost.test is suspended by Roostack Cloud: Phishing.",
		"paypal.smiths.roost.test wasn&#39;t approved by Roostack Cloud", "nab.smiths.roost.test is waiting for Roostack Cloud&#39;s approval"} {
		if !strings.Contains(home, want) {
			t.Errorf("Needs attention: no %q", want)
		}
	}
	// Lifted, decided: nothing left on hold.
	f.SuspendedFor, f.NameStates = "", nil
	link.Check(ctx)
	if _, home := h.get("/"); strings.Contains(home, "Roostack Cloud has suspended") || strings.Contains(home, "nab.smiths") {
		t.Error("still on hold after Cloud said it isn't")
	}

	// The relay, turned off in Cloud: its answers say so.
	fm.relay = true
	if err := link.Relay(ctx, []string{"smiths.roost.test"}, config.Countries{}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, body := h.get("/settings/public"); strings.Contains(body, "turned off in Roostack Cloud") {
		t.Error("the relay is off before Cloud said so")
	}
	f.RelayOff = time.Now()
	if err := link.Relay(ctx, []string{"photos.smiths.roost.test", "smiths.roost.test"}, config.Countries{}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, body := h.get("/settings/public"); !strings.Contains(body, "The relay was turned off in Roostack Cloud today") ||
		!strings.Contains(body, `action="/settings/public/relay-on#way-in"`) {
		t.Errorf("Public access with the relay off: %s", body)
	}
	if _, home := h.get("/"); !strings.Contains(home, "it was turned off in Roostack Cloud") {
		t.Error("Needs attention doesn't say the relay is off")
	}
	// Turning it on is the Owner's.
	m, _ := loggedIn(t, auth.RoleMember)
	m.server.Cloud = link
	if code, _ := m.post("/settings/public/relay-on", nil, m.srv.URL); code == 200 || f.RelayOff.IsZero() {
		t.Errorf("a Member turning the relay on: %d", code)
	}
	if _, body := h.post("/settings/public/relay-on", nil, h.srv.URL); !strings.Contains(body, "The relay is on again") ||
		strings.Contains(body, "The relay was turned off") || !f.RelayOff.IsZero() {
		t.Errorf("after turning it on: %s", body)
	}
	for _, r := range f.Relayed {
		if !r.TurnOn {
			t.Error("Cloud wasn't asked to turn it on")
		}
	}

	// Suspended: said, and no button.
	f.RelaySuspended = "Phishing through the relay"
	link.Relay(ctx, []string{"smiths.roost.test"}, config.Countries{}, "", "")
	if _, body := h.get("/settings/public"); !strings.Contains(body, "Roostack Cloud has suspended this lab's relay:</strong> Phishing through the relay.") ||
		strings.Contains(body, "relay-on") {
		t.Errorf("Public access with the relay suspended: %s", body)
	}
}

// TestProblemRecipients: the addresses the lab reports to Roostack Cloud
// are those its problems are emailed to: people with an address who get
// Problems (never Members, nor someone who turned it off), each once.
func TestProblemRecipients(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	ctx := context.Background()
	u, _ := h.store.UserByUsername(ctx, "u")
	h.store.SetEmail(ctx, u.ID, "Owner@Example.com")
	hash, _ := authkit.HashPassword("a long password")
	for _, p := range []struct{ name, role, email, notify string }{
		{"admin", auth.RoleAdmin, "admin@example.com", ""},
		{"quiet", auth.RoleAdmin, "quiet@example.com", "waiting,weekly"},
		{"member", auth.RoleMember, "member@example.com", ""},
		{"twin", auth.RoleOwner, "owner@example.com", ""},
		{"nobody", auth.RoleOwner, "", ""},
	} {
		id, err := h.store.CreateUser(ctx, store.User{Username: p.name, PasswordHash: hash, Role: p.role})
		if err != nil {
			t.Fatal(err)
		}
		h.store.SetEmail(ctx, id, p.email)
		if p.notify != "" {
			h.store.SetNotify(ctx, id, p.notify)
		}
	}
	got, err := h.server.ProblemRecipients(ctx)
	slices.Sort(got)
	if want := []string{"admin@example.com", "owner@example.com"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("recipients: %v %v, want %v", got, err, want)
	}
}

// TestCloudRecipients: Settings → Email and Settings → Roostack Cloud say
// how each address getting the lab's problems stands with Roostack Cloud
// (waiting to confirm, confirmed), once the lab is linked and reports.
func TestCloudRecipients(t *testing.T) {
	h, _, _ := notifyHarness(t, auth.RoleOwner)
	if _, body := h.get("/settings/email"); strings.Contains(body, "Roostack Cloud") {
		t.Error("Roostack Cloud named before the lab is linked")
	}
	link, f := newCloudWith(t)
	h.server.Cloud = link
	ctx := context.Background()
	if _, err := link.Start(ctx, "", ""); err != nil {
		t.Fatal(err)
	}
	f.Confirm(link.Status().Waiting.Code)
	for i := 0; i < 200 && !link.Status().Linked; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if _, body := h.get("/settings/email"); !strings.Contains(body, "Roostack Cloud hears of them at the lab's next daily check-in") {
		t.Error("not told before the first report")
	}
	link.Report = func(ctx context.Context) (cloudapi.LabReport, error) {
		emails, err := h.server.ProblemRecipients(ctx)
		return cloudapi.LabReport{Emails: emails}, err
	}
	f.AskedAt = time.Now()
	if err := link.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, body := h.get("/settings/email"); !strings.Contains(body, "Hasn't confirmed Roostack Cloud's emails about the lab yet: Cloud emailed a link") {
		t.Errorf("waiting:\n%s", body[strings.Index(body, `id="who"`):])
	}
	f.Confirmed = map[string]string{"sam@example.com": cloudapi.RecipientConfirmed}
	if err := link.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, body := h.get("/settings/email"); !strings.Contains(body, "Gets Roostack Cloud's emails about the lab") {
		t.Error("confirmed: not shown on Settings → Email")
	}
	_, body := h.get("/settings/cloud")
	for _, want := range []string{"<dt>Emails to</dt>", "<li>sam@example.com<br>", "Gets Roostack Cloud's emails about the lab"} {
		if !strings.Contains(body, want) {
			t.Errorf("the Cloud page: no %q", want)
		}
	}
}
