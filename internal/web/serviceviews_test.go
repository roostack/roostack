package web

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
)

// fakeServiceSource is what the servers report, and what the controller
// knows, for ServiceViews.
type fakeServiceSource struct {
	infos     map[string]manager.ServiceInfo
	withdrawn map[string]*manager.Withdrawal
	updates   map[string]*manager.Update
	updating  map[string]bool
	switching map[string]bool
}

func (f fakeServiceSource) ServiceInfos(context.Context) map[string]manager.ServiceInfo {
	return f.infos
}
func (f fakeServiceSource) Withdrawals(context.Context) map[string]*manager.Withdrawal {
	return f.withdrawn
}
func (f fakeServiceSource) AvailableUpdates(context.Context) map[string]*manager.Update {
	return f.updates
}
func (f fakeServiceSource) Switching(name string) bool { return f.switching[name] }
func (f fakeServiceSource) Updating(name string) *manager.ServiceUpdate {
	if f.updating[name] {
		return &manager.ServiceUpdate{}
	}
	return nil
}

// NodeByID knows one server, gone or not.
func (fakeServiceSource) NodeByID(id string) node.Node {
	if id == "n-9876543210" {
		return otherServer{&fakeManager{}}
	}
	return nil
}

// The dashboard's list: each service with what its server reports, its
// address, an update, and the status the controller decides (ServiceStatus).
func TestServiceViews(t *testing.T) {
	lab := config.Default()
	lab.Domain = "lab.example.com"
	var services []state.Service
	add := func(name string, change func(*instance.Instance)) {
		p, err := pkgspec.Load(pkgtest.File(name))
		if err != nil {
			t.Fatal(err)
		}
		in := &instance.Instance{Package: name, Version: p.Version, Updates: "notify",
			Routes: map[string]instance.RouteOverride{"web": {Port: 20001 + len(services)}}}
		if change != nil {
			change(in)
		}
		services = append(services, state.Service{Package: p, Instance: in})
	}
	add("vaultwarden", nil)
	add("immich", nil)
	add("forgejo", func(in *instance.Instance) { in.Waiting, in.Host = true, "n-9876543210" })
	add("mealie", func(in *instance.Instance) { in.Version = "0.0.1" }) // its files can't be made
	add("stirling-pdf", func(in *instance.Instance) { in.Held = &instance.Hold{Taken: time.Now()} })
	src := fakeServiceSource{
		infos: map[string]manager.ServiceInfo{
			"vaultwarden": {ServiceDetail: node.ServiceDetail{Status: "running"}, Server: "nuc"},
			"immich":      {ServiceDetail: node.ServiceDetail{Status: "running"}, Server: "nuc"},
			"forgejo":     {ServiceDetail: node.ServiceDetail{Status: "running"}, Server: "nas"},
		},
		withdrawn: map[string]*manager.Withdrawal{"immich": {Withdrawal: registry.Withdrawal{Level: "stop"}, Stopped: true}},
		updates:   map[string]*manager.Update{"vaultwarden": {Service: "vaultwarden", To: "1.35.0-roostack.1"}},
		updating:  map[string]bool{"immich": true},
	}
	views := ServiceViews(t.Context(), lab, services, src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(views) != len(services) {
		t.Fatalf("%d views for %d services", len(views), len(services))
	}
	byName := map[string]ServiceView{}
	for _, v := range views {
		byName[v.Name] = v
	}
	for name, want := range map[string]struct {
		status, server, wasOn, update string
		urls                          bool
	}{
		"vaultwarden":  {status: "running", server: "nuc", update: "1.35.0", urls: true},
		"immich":       {status: StatusUpdating, server: "nuc", urls: true},
		"forgejo":      {status: StatusWaiting, wasOn: "vps", urls: true},
		"mealie":       {status: StatusFailed},
		"stirling-pdf": {status: StatusHeld, urls: true},
	} {
		v := byName[name]
		if v.Status != want.status || v.Server != want.server || v.WasOn != want.wasOn || v.Update != want.update ||
			(len(v.URLs) > 0) != want.urls || v.NoAddress {
			t.Errorf("%s: %+v, want %+v", name, v, want)
		}
	}
	// Without an address, a service with web routes can't be reached.
	lab.Domain = ""
	if v := ServiceViews(t.Context(), lab, services[:1], src, slog.New(slog.NewTextHandler(io.Discard, nil))); !v[0].NoAddress {
		t.Errorf("no address: %+v", v[0])
	}
}

// The setup code is kept until an account exists, and goes then.
func TestSetupToken(t *testing.T) {
	st := storetest.Open(t)
	path := filepath.Join(t.TempDir(), "setup-token")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	token, err := SetupToken(t.Context(), st, path, log)
	if err != nil || len(token) < 20 {
		t.Fatalf("token %q %v", token, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("its file: %v %v", fi, err)
	}
	if again, err := SetupToken(t.Context(), st, path, log); err != nil || again != token {
		t.Errorf("after a restart: %q %v, want %q", again, err, token)
	}
	if _, err := st.CreateUser(t.Context(), store.User{Username: "anna", PasswordHash: "x", Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	if done, err := SetupToken(t.Context(), st, path, log); err != nil || done != "" {
		t.Errorf("with an account: %q %v", done, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
}
