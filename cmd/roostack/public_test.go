package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
)

// roostack public pauses and opens public access from the command line
// (SPEC.md §6.16), in lab.yaml and the instance files the running
// Roostack reads when it starts, records it for the Owners' email, and
// says what's paused.
func TestPublicCmd(t *testing.T) {
	dir := t.TempDir()
	labFile := filepath.Join(dir, "lab.yaml")
	os.WriteFile(labFile, []byte("domain: lab.example.com\npaths:\n  state_dir: "+dir+"\n  catalog_dir: "+pkgtest.Dir()+"\n"), 0o600)
	lab, err := config.Load(labFile)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.KeepPackage(lab.Paths, p.Name, p.Version); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(lab.Paths.InstancesDir(), 0o700)
	in := &instance.Instance{Package: p.Name, Version: p.Version, Updates: "notify",
		Routes: map[string]instance.RouteOverride{"web": {Exposure: pkgspec.ExposurePublic}}}
	if err := in.Save(state.InstanceFile(lab, "vaultwarden")); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		t.Helper()
		return publicCmd(append(args[:1:1], append([]string{"-lab", labFile}, args[1:]...)...))
	}
	if err := run("pause"); err == nil || !strings.Contains(err.Error(), "say what to pause") {
		t.Errorf("nothing chosen: %v", err)
	}
	if err := run("pause", "-service", "nothing"); err == nil || !strings.Contains(err.Error(), "no service named nothing") {
		t.Errorf("an unknown service: %v", err)
	}
	if err := run("pause", "-service", "vaultwarden"); err != nil {
		t.Fatal(err)
	}
	if err := run("pause", "-all", "-built-with", "php, rust"); err != nil {
		t.Fatal(err)
	}
	lab, _ = config.Load(labFile)
	services, _ := state.LoadServices(lab)
	pp := manager.PublicPausesOf(lab, services)
	if len(pp.Services) != 1 || pp.Services[0].Own == nil || pp.Services[0].Own.By != "" || pp.All == nil ||
		len(pp.BuiltWith) != 2 || strings.Join(pp.Services[0].ByBuiltWith, " ") != "rust" {
		t.Fatalf("paused: %+v", pp)
	}
	if s := pauseStatus(pp); !strings.Contains(s, "Everything public is paused, since") || !strings.Contains(s, "from the command line") ||
		!strings.Contains(s, "Vaultwarden (vaultwarden): paused (its own pause, since") {
		t.Errorf("status:\n%s", s)
	}
	if err := run("resume", "-all", "-built-with", "php,rust", "-service", "vaultwarden"); err != nil {
		t.Fatal(err)
	}
	lab, _ = config.Load(labFile)
	services, _ = state.LoadServices(lab)
	if pp := manager.PublicPausesOf(lab, services); pp.Services[0].Paused() || !lab.Public.Pause.IsZero() {
		t.Errorf("opened again: %+v", pp)
	}
	st, err := store.Open(lab.Paths.DatabaseFile())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	entries, _ := st.AuditSince(context.Background(), 0)
	var got []string
	for _, e := range entries {
		got = append(got, e.Action+": "+e.Detail)
	}
	want := attention.AuditPublicPaused + ": Vaultwarden via command line|" + attention.AuditPublicPaused +
		": everything and services built with php and rust via command line|" + attention.AuditPublicOpened +
		": everything, services built with php and rust and Vaultwarden via command line"
	if strings.Join(got, "|") != want {
		t.Errorf("audit:\n%q", got)
	}
}
