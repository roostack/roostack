package node

import (
	"context"
	"errors"
	"io"
	"testing"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
)

// memKV is settings in memory.
type memKV map[string]string

func (m memKV) Setting(_ context.Context, k string) (string, error) { return m[k], nil }
func (m memKV) SetSetting(_ context.Context, k, v string) error     { m[k] = v; return nil }

// A new service's install is shown step by step: each container's
// download and start, its web address, its setup; it's forgotten once done.
func TestInstallProgress(t *testing.T) {
	pull := func(context.Context, string, io.Writer) error { return nil }
	// Its setup is due: it's a new install.
	kv := memKV{hookKey("forgejo"): hookPending}
	n := &Local{Reconciler: &reconcile.Reconciler{Pull: pull}, State: kv}
	db, app := "roostack-forgejo-db-main.service", "roostack-forgejo-app.service"
	svc := state.Service{Package: &pkgspec.Package{Name: "forgejo", Title: "Forgejo",
		Hooks: []*pkgspec.Hook{{On: pkgspec.HookInstall, Title: "Create the administrator account"}}}}
	desired := reconcile.Desired{Services: []*generate.Output{{Service: "forgejo", Units: []string{db, app},
		Hosts: []string{"git.lab.example.com"},
		Quadlets: []generate.File{
			{Name: "roostack-forgejo-db-main.container", Content: "Image=docker.io/library/postgres:17-alpine@sha256:ab\n"},
			{Name: "roostack-forgejo-app.container", Content: "Image=codeberg.org/forgejo/forgejo:15.0.9-rootless@sha256:cd\n"},
		}}}}
	plan := &reconcile.Plan{StartUnits: []string{db, app},
		StartNotes: map[string]string{db: "downloads postgres", app: "downloads forgejo"},
		Pulls:      map[string]string{db: "postgres", app: "forgejo"}}
	n.planInstalls([]state.Service{svc}, desired, plan, true)

	p := n.installProgress("forgejo")
	if p == nil {
		t.Fatal("not tracked")
	}
	var titles []string
	for _, s := range p.Steps {
		titles = append(titles, s.Title)
	}
	want := []string{"Downloading the database (Postgres)", "Starting the database (Postgres)", "Downloading Forgejo",
		"Starting Forgejo", "Waiting for Roostack's web proxy to finish building (first time only; a few minutes)",
		"Finishing setup (create the administrator account)"}
	if len(titles) != len(want) {
		t.Fatalf("steps %q", titles)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("step %d: %q, want %q", i, titles[i], want[i])
		}
	}

	for _, ev := range []reconcile.Event{{Stage: reconcile.StageDownload, Unit: db}, {Stage: reconcile.StageDownload, Unit: db, Done: true},
		{Stage: reconcile.StageStart, Unit: db}, {Stage: reconcile.StageStart, Unit: db, Done: true},
		{Stage: reconcile.StageDownload, Unit: app}} {
		n.installEvent(ev)
	}
	p = n.installProgress("forgejo")
	if p.Current() != 2 || p.State(1) != "done" || p.State(2) != "now" || p.State(3) != "todo" || p.Step().Started.IsZero() {
		t.Errorf("after the database: current %d %+v", p.Current(), p.Step())
	}

	for _, ev := range []reconcile.Event{{Stage: reconcile.StageDownload, Unit: app, Done: true},
		{Stage: reconcile.StageStart, Unit: app}, {Stage: reconcile.StageStart, Unit: app, Done: true},
		{Stage: reconcile.StageProxy}, {Stage: reconcile.StageProxy, Done: true}} {
		n.installEvent(ev)
	}
	n.installsApplied()
	if p := n.installProgress("forgejo"); p == nil || p.Step().Key != stageSetup {
		t.Fatalf("waiting for setup: %+v", p)
	}
	n.installStep("forgejo", stageSetup, false)
	n.installStep("forgejo", stageSetup, true)
	if n.installProgress("forgejo") != nil {
		t.Error("a finished install is forgotten")
	}

	// A failed download stops the install, says why, and is cleared by the
	// next apply.
	n.planInstalls([]state.Service{svc}, desired, plan, false)
	n.installEvent(reconcile.Event{Stage: reconcile.StageDownload, Unit: db})
	n.installEvent(reconcile.Event{Stage: reconcile.StageDownload, Unit: db, Done: true, Err: errors.New("no network")})
	if p := n.installProgress("forgejo"); p == nil || p.Failed != "no network" {
		t.Fatalf("failed: %+v", p)
	}
	// It outlives a restart of Roostack (kept in the server's settings),
	// and the next apply starts its containers as new ones again.
	again := &Local{Reconciler: n.Reconciler, State: kv}
	if p := again.installProgress("forgejo"); p == nil || p.Failed != "no network" || len(p.Steps) != 6 {
		t.Fatalf("after a restart: %+v", p)
	}
	if u := again.unfinishedUnits(); len(u) != 2 || !u[db] || !u[app] {
		t.Errorf("unfinished units: %v", u)
	}
	n.planInstalls([]state.Service{svc}, desired, &reconcile.Plan{StartUnits: []string{db, app}}, false)
	if n.installProgress("forgejo") != nil {
		t.Error("the next apply clears a failed install")
	}

	// Without a domain, Caddy waits for it: no web address step, and the
	// install is complete once the apply and its setup are.
	plan.Waiting = []string{"roostack-caddy.container"}
	n.planInstalls([]state.Service{svc}, desired, plan, false)
	for _, s := range n.installProgress("forgejo").Steps {
		if s.Key == reconcile.StageProxy {
			t.Error("no web address step while Caddy waits for the domain")
		}
	}
	n.installsApplied()
	if p := n.installProgress("forgejo"); p == nil || p.Step().Key != stageSetup {
		t.Fatalf("after the apply, only setup is left: %+v", p)
	}
	if u := n.unfinishedUnits(); len(u) != 0 {
		t.Errorf("its containers have started: %v", u)
	}
	// Taken off this server meanwhile: forgotten.
	n.planInstalls(nil, reconcile.Desired{}, &reconcile.Plan{}, false)
	if p := n.installProgress("forgejo"); p != nil || kv[installsKey] != "" {
		t.Errorf("a service no longer here: %+v %q", p, kv[installsKey])
	}

	// Arriving already set up (moved or restored from another server): no
	// setup step, so it doesn't wait for one forever.
	n.installs, kv[hookKey("forgejo")] = nil, ""
	n.planInstalls([]state.Service{svc}, desired, plan, false)
	n.installsApplied()
	if p := n.installProgress("forgejo"); p != nil {
		t.Errorf("a moved service is done once applied: %+v", p)
	}
}

func TestContainerLabel(t *testing.T) {
	for _, c := range []struct{ unit, image, want string }{
		{"roostack-immich-db-redis.service", "docker.io/valkey/valkey:8-alpine@sha256:ab", "the cache (Valkey)"},
		{"roostack-immich-db-main.service", "ghcr.io/immich-app/postgres:14-vectorchord0.4.3@sha256:ab", "the database (Postgres)"},
		{"roostack-immich-machine-learning.service", "ghcr.io/immich-app/immich-machine-learning:v2.1.0", "Immich (machine-learning)"},
	} {
		if got := containerLabel("Immich", "immich", c.unit, c.image, false); got != c.want {
			t.Errorf("%s: %q, want %q", c.unit, got, c.want)
		}
	}
}
