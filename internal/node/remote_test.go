package node

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/state"
)

// fakeNode records what the controller asked of it.
type fakeNode struct {
	applied  chan Assignment
	settings *fakeSettings
	healthy  atomic.Bool  // its last apply worked (else "image pull failed")
	builtIns atomic.Int32 // calls
}

func (f *fakeNode) Identity() nodeid.Identity {
	return nodeid.Identity{ID: "n-0000000002", Name: "vps"}
}
func (f *fakeNode) Apply(a Assignment) { f.applied <- a }
func (f *fakeNode) Status() ApplyStatus {
	if f.healthy.Load() {
		return ApplyStatus{Finished: true}
	}
	return ApplyStatus{Finished: true, LastErr: errors.New("image pull failed")}
}
func (f *fakeNode) ServiceStates(context.Context) (map[string]string, error) {
	return map[string]string{"vaultwarden": "running"}, nil
}
func (f *fakeNode) ServiceDetails(context.Context) (map[string]ServiceDetail, error) {
	return map[string]ServiceDetail{"vaultwarden": {Status: "running", Containers: []Container{{Name: "roostack-vaultwarden-app",
		Image: "docker.io/vaultwarden/server:1.34", State: "active (running)"}}, DataSize: 1234}}, nil
}
func (f *fakeNode) Memory(context.Context) (MemoryInfo, error) {
	return MemoryInfo{Total: 8_000_000_000, Available: 3_600_000_000}, nil
}
func (f *fakeNode) BuiltIns(context.Context) ([]BuiltIn, error) {
	f.builtIns.Add(1)
	return []BuiltIn{{Name: "Roostack agent", Kind: "roostack", Status: "active (running)"}}, nil
}
func (f *fakeNode) InstallStatus(_ context.Context, s string) HookStatus {
	return HookStatus{Pending: s == "forgejo"}
}
func (f *fakeNode) RetryInstall(_ context.Context, s string) error {
	if s != "forgejo" {
		return ErrNotFound
	}
	return nil
}
func (f *fakeNode) RunAction(_ context.Context, s, a string, in map[string]string) (string, error) {
	switch {
	case in["password"] == "short":
		return "", &FormError{Msg: "Password must be at least 8 characters."}
	case a == "fail":
		return "", &ActionError{Title: "Reset a password", Output: "database not ready", Err: errors.New("exit status 1")}
	}
	return "ok " + in["username"], nil
}
func (f *fakeNode) RevealSecret(_ context.Context, s, k string) (string, error) { return "s3cret", nil }
func (f *fakeNode) LatestActivity(kind string) (activity.Snapshot, bool) {
	return activity.Snapshot{Title: "Applying changes", Status: "All changes applied"}, kind == activity.Apply
}
func (f *fakeNode) Backups() Backups   { return nil }
func (f *fakeNode) Restores() Restores { return nil }
func (f *fakeNode) Updates() Updates   { return nil }
func (f *fakeNode) Settings() Settings { return f.settings }

type fakeSettings struct{ zone string }

func (f *fakeSettings) Timezones() (string, []string, error) {
	return f.zone, []string{"Etc/UTC", "Europe/London"}, nil
}
func (f *fakeSettings) SetTimezone(_ context.Context, name string) (bool, error) {
	if name == "Mars" {
		return false, &FormError{Msg: "Choose a time zone from the list."}
	}
	f.zone = name
	return true, nil
}
func (f *fakeSettings) SSHAccess(bool) (SSHAccess, error)                           { return SSHAccess{Choice: "lan"}, nil }
func (f *fakeSettings) SetSSH(context.Context, SSHChange) error                     { return nil }
func (f *fakeSettings) SetRestartPolicy(context.Context, string, int) error         { return nil }
func (f *fakeSettings) RestartPolicy() (string, int)                                { return "notify", 3 }
func (f *fakeSettings) PromoteBackupCopy(context.Context, string, BackupCopy) error { return nil }
func (f *fakeSettings) SetBackupCopies(context.Context, []BackupCopy) error         { return nil }
func (f *fakeSettings) SetBackupDestination(context.Context, BackupDestination) error {
	return nil
}

// lab returns a controller identity, a node identity and a catalog.
func servers(t *testing.T) (ctrl, node labnet.Identity, lab config.Lab) {
	t.Helper()
	ca, err := labnet.LoadOrCreateCA(t.TempDir(), "home")
	if err != nil {
		t.Fatal(err)
	}
	issue := func(id string, addr string) labnet.Identity {
		m := labnet.Identity{Dir: t.TempDir()}
		csr, _ := m.CSR(id)
		cert, _, err := ca.Issue(csr, id, netip.MustParseAddr(addr))
		if err != nil {
			t.Fatal(err)
		}
		m.Save(cert, labnet.CertPEM(ca.Cert))
		return m
	}
	lab = config.Default()
	lab.Domain = "lab.example.com"
	lab.Paths.CatalogDir = t.TempDir()
	// The lab's copies of its packages go here (SPEC.md §8.3.1).
	lab.Paths.StateDir = t.TempDir()
	pkgtest.Catalog(t, lab.Paths.CatalogDir)
	return issue("n-0000000001", "100.96.0.1"), issue("n-0000000002", "100.96.0.2"), lab
}

func TestRemote(t *testing.T) {
	ctrl, nodeID, lab := servers(t)
	fn := &fakeNode{applied: make(chan Assignment, 1), settings: &fakeSettings{zone: "Etc/UTC"}}
	saved := make(chan WireAssignment, 1)
	api := &Server{Node: fn, ControllerID: "n-0000000001", Saved: func(w WireAssignment) { saved <- w }}
	srv := httptest.NewUnstartedServer(api.Handler())
	srv.TLS, _ = nodeID.ServerTLS()
	srv.StartTLS()
	defer srv.Close()

	r, err := NewRemote(ctrl, nodeid.Identity{ID: "n-0000000002", Name: "vps"}, netip.MustParseAddr("100.96.0.2"), lab,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.URL = srv.URL
	ctx := context.Background()

	if st := r.Status(); st.LastErr == nil || st.LastErr.Error() != "image pull failed" || !st.Finished {
		t.Errorf("status = %+v", st)
	}
	if states, err := r.ServiceStates(ctx); err != nil || states["vaultwarden"] != "running" {
		t.Errorf("states = %v, %v", states, err)
	}
	if d, err := r.ServiceDetails(ctx); err != nil || d["vaultwarden"].Containers[0].Image != "docker.io/vaultwarden/server:1.34" ||
		d["vaultwarden"].DataSize != 1234 {
		t.Errorf("details = %+v, %v", d, err)
	}
	if m, err := r.Memory(ctx); err != nil || m.Total != 8_000_000_000 || m.Available != 3_600_000_000 {
		t.Errorf("memory = %+v, %v", m, err)
	}
	if b, err := r.BuiltIns(ctx); err != nil || len(b) != 1 || b[0].Name != "Roostack agent" {
		t.Errorf("built-ins = %+v, %v", b, err)
	}
	if snap, ok := r.LatestActivity(activity.Apply); !ok || snap.Status != "All changes applied" {
		t.Errorf("activity = %+v", snap)
	}
	if _, ok := r.LatestActivity(activity.Backup); ok {
		t.Error("no backup activity")
	}
	if st := r.InstallStatus(ctx, "forgejo"); !st.Pending {
		t.Errorf("setup = %+v", st)
	}
	if err := r.RetryInstall(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("retry unknown: %v", err)
	}
	if out, err := r.RunAction(ctx, "forgejo", "reset", map[string]string{"username": "mum"}); err != nil || out != "ok mum" {
		t.Errorf("action: %q %v", out, err)
	}
	var fe *FormError
	if _, err := r.RunAction(ctx, "forgejo", "reset", map[string]string{"password": "short"}); !errors.As(err, &fe) {
		t.Errorf("form error lost: %v", err)
	}
	var ae *ActionError
	if _, err := r.RunAction(ctx, "forgejo", "fail", nil); !errors.As(err, &ae) || ae.Output != "database not ready" {
		t.Errorf("action error lost: %v", err)
	}
	if v, err := r.RevealSecret(ctx, "vaultwarden", "admin_token"); v != "s3cret" || err != nil {
		t.Errorf("reveal: %q %v", v, err)
	}
	if r.Backups() != nil || r.Settings() == nil {
		t.Error("maintenance should follow what the node has")
	}
	if changed, err := r.Settings().SetTimezone(ctx, "Europe/London"); !changed || err != nil || fn.settings.zone != "Europe/London" {
		t.Errorf("set zone: %v %v", changed, err)
	}
	// Reads the pages refresh are shared for a few seconds; a change
	// forgets them.
	r.BuiltIns(ctx)
	calls := fn.builtIns.Load()
	if b, err := r.BuiltIns(ctx); err != nil || len(b) != 1 || fn.builtIns.Load() != calls {
		t.Errorf("a second read so soon should be shared: %d calls, %v", fn.builtIns.Load()-calls, err)
	}
	r.Settings().SetTimezone(ctx, "Etc/UTC")
	if r.BuiltIns(ctx); fn.builtIns.Load() != calls+1 {
		t.Error("a change should forget shared reads")
	}
	if mode, hour := r.Settings().RestartPolicy(); mode != "notify" || hour != 3 {
		t.Errorf("restart policy %q %d", mode, hour)
	}
	if _, err := r.Settings().SetTimezone(ctx, "Mars"); !errors.As(err, &fe) {
		t.Errorf("set zone form error: %v", err)
	}

	// Assignments: pushed, with services and secrets; saved without them.
	svcs := loadVaultwarden(t, lab)
	r.Apply(Assignment{Services: svcs, Secrets: map[string][]byte{"roostack-vaultwarden-x": []byte("typed")}, Lab: LabContext{Domain: lab.Domain}})
	select {
	case a := <-fn.applied:
		if len(a.Services) != 1 || string(a.Secrets["roostack-vaultwarden-x"]) != "typed" || a.Lab.Domain != "lab.example.com" {
			t.Errorf("assignment = %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("assignment not delivered")
	}
	if w := <-saved; w.Secrets != nil || len(w.Services) != 1 {
		t.Error("saved assignments keep services, never secrets")
	}
	// pushed waits for a send to finish: the node applies an assignment
	// before its reply reaches the controller, which only then counts it
	// as delivered.
	pushed := func() {
		for deadline := time.Now().Add(5 * time.Second); ; {
			r.mu.Lock()
			pushing := r.pushing
			r.mu.Unlock()
			if !pushing || time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Once the send has finished, the secrets aren't kept.
	pushed()
	if w, ok := r.Pending(); !ok || w.Secrets != nil {
		t.Error("delivered secrets must not be sent again")
	}
	// The same assignment again: a server whose last apply failed gets it
	// (its retry); a healthy one isn't bothered; a change always goes.
	again := func(a Assignment) bool {
		r.mu.Lock()
		r.statusAt = time.Time{}
		r.mu.Unlock()
		r.Apply(a)
		select {
		case <-fn.applied:
			<-saved
			pushed()
			return true
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}
	same := Assignment{Services: svcs, Lab: LabContext{Domain: lab.Domain}}
	if !again(same) {
		t.Error("a server whose last apply failed should get it again")
	}
	fn.healthy.Store(true)
	if again(same) {
		t.Error("a healthy server that has the assignment was sent it again")
	}
	if !again(Assignment{Services: svcs, Lab: LabContext{Domain: "new.example.com"}}) {
		t.Error("a changed assignment must be sent")
	}
	if !again(Assignment{Services: svcs, Lab: LabContext{Domain: "new.example.com"}, SetUp: []string{"vaultwarden"}}) {
		t.Error("set-ups to hand over must be sent")
	}
	fn.healthy.Store(false)

	// An older assignment is ignored.
	if err := api.Take(WireAssignment{Version: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fn.applied:
		t.Error("an older assignment was applied")
	default:
	}

	// Only the controller may call.
	other := &http.Client{Transport: &http.Transport{TLSClientConfig: mustTLS(t, nodeID, "n-0000000002")}}
	resp, err := other.Get(srv.URL + "/v1/status")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("another server calling: %d", resp.StatusCode)
		}
	}
}

func mustTLS(t *testing.T, id labnet.Identity, peer string) *tls.Config {
	c, err := id.ClientTLS(peer)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func loadVaultwarden(t *testing.T, lab config.Lab) []state.Service {
	t.Helper()
	// As the controller does: the lab's copy of the catalog's package.
	cat, err := state.LoadCatalogPackage(lab.Paths, "vaultwarden")
	if err != nil {
		t.Fatal(err)
	}
	p, err := state.LoadPackage(lab.Paths, "vaultwarden", cat.Version)
	if err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "vaultwarden", Version: p.Version, Settings: map[string]any{},
		Routes: map[string]instance.RouteOverride{"web": {Port: 18000}}}
	return []state.Service{{Instance: in, Package: p}}
}

// Everything a server needs crosses the wire, other labs' stores included
// (a node can offer storage for others).
func TestWireCarriesGuests(t *testing.T) {
	a := Assignment{Storage: &Storage{Users: map[string]string{"n-1": "h"}},
		Guests: []generate.GuestStorage{{User: "g1", Port: 8100, Quota: 1 << 30, Users: map[string]string{"n-2": "h2"}, AdminHash: "a"}}, GuestAddr: "100.103.66.1"}
	w, err := Encode(config.Default(), a, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(w)
	var back WireAssignment
	json.Unmarshal(data, &back)
	got, err := Decode(back)
	if err != nil || len(got.Guests) != 1 || !reflect.DeepEqual(got.Guests[0], a.Guests[0]) || got.GuestAddr != a.GuestAddr || got.Storage == nil {
		t.Errorf("decoded %+v, %v", got, err)
	}
}

// A server that stops answering costs one wait, not one per call: calls
// fail at once while a background check sees whether it's back.
func TestRemoteDown(t *testing.T) {
	ctrl, nodeID, lab := servers(t)
	fn := &fakeNode{applied: make(chan Assignment, 1), settings: &fakeSettings{zone: "Etc/UTC"}}
	api := &Server{Node: fn, ControllerID: "n-0000000001"}
	srv := httptest.NewUnstartedServer(api.Handler())
	srv.TLS, _ = nodeID.ServerTLS()
	srv.StartTLS()
	defer srv.Close()
	r, err := NewRemote(ctrl, nodeid.Identity{ID: "n-0000000002", Name: "vps"}, netip.MustParseAddr("100.96.0.2"), lab,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Accepts connections and never answers, like a server that's off.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	r.URL = "https://" + ln.Addr().String()
	ctx := context.Background()

	if r.Settings() != nil {
		t.Fatal("it doesn't answer")
	}
	start := time.Now()
	for range 5 {
		if _, err := r.ServiceStates(ctx); !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "can't reach vps") {
			t.Fatalf("err = %v", err)
		}
		if r.Settings() != nil {
			t.Error("still down")
		}
	}
	if time.Since(start) > time.Second {
		t.Errorf("calls to a server that's down took %v", time.Since(start))
	}

	// It's back: the next check (a background one) finds it.
	r.URL = srv.URL
	r.mu.Lock()
	r.downAt = time.Now().Add(-downFor) // the 30 seconds are up
	r.statusAt = time.Time{}
	r.mu.Unlock()
	if r.Settings() == nil {
		t.Error("back once it answers")
	}
	if _, err := r.ServiceStates(ctx); err != nil {
		t.Errorf("calls go through again: %v", err)
	}
}

// A server that left the lab gets nothing more, and a retry stops.
func TestRemoteClosed(t *testing.T) {
	ctrl, _, lab := servers(t)
	r, err := NewRemote(ctrl, nodeid.Identity{ID: "n-0000000002", Name: "vps"}, netip.MustParseAddr("100.96.0.2"), lab,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.URL = "https://127.0.0.1:1" // nothing there
	r.Close()
	r.Apply(Assignment{})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pushing || r.latest != nil {
		t.Error("a closed server is sent nothing")
	}
}
