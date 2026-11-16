package manager

import (
	"context"
	"errors"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
)

// A service's extra environment variables (SPEC.md §8.1): kept in its
// instance; a secret's value handed to its server as a Podman secret of
// its revision, a new value a new revision, kept when none is given; the
// package's own variables, and a secret without a value, refused.
func TestSetEnv(t *testing.T) {
	m, home, _ := setUpMove(t)
	ctx := context.Background()
	vars := []instance.EnvVar{{Container: "app", Name: "FORGEJO__ui__DEFAULT_THEME", Value: "forgejo-dark"},
		{Container: "app", Name: "FORGEJO__mailer__PASSWD", Secret: true}}
	if err := m.SetEnv(ctx, "forgejo", vars, nil); !isUserErr(err) {
		t.Errorf("a secret without a value: %v", err)
	}
	if err := m.SetEnv(ctx, "forgejo", vars, map[string]string{EnvKey("app", "FORGEJO__mailer__PASSWD"): "app-pass-1"}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	home.mu.Lock()
	got := home.last.Secrets["roostack-forgejo-env-app-FORGEJO__mailer__PASSWD-1"]
	home.mu.Unlock()
	if string(got) != "app-pass-1" {
		t.Errorf("the secret handed over: %q", got)
	}
	in := loadInstance(t, m, "forgejo")
	if len(in.Env) != 2 || in.Env[0].Value != "forgejo-dark" || !in.Env[1].Secret || in.Env[1].Rev != 1 || in.Env[1].Value != "" {
		t.Fatalf("kept: %+v", in.Env)
	}
	// Saved again without a value: the secret keeps its revision.
	if err := m.SetEnv(ctx, "forgejo", vars, nil); err != nil {
		t.Fatal(err)
	}
	if in := loadInstance(t, m, "forgejo"); in.Env[1].Rev != 1 {
		t.Errorf("kept secret: %+v", in.Env[1])
	}
	// A new value is a new revision.
	if err := m.SetEnv(ctx, "forgejo", vars, map[string]string{EnvKey("app", "FORGEJO__mailer__PASSWD"): "app-pass-2"}); err != nil {
		t.Fatal(err)
	}
	if in := loadInstance(t, m, "forgejo"); in.Env[1].Rev != 2 {
		t.Errorf("new value: %+v", in.Env[1])
	}
	for _, bad := range [][]instance.EnvVar{
		{{Container: "app", Name: "GITEA_APP_INI", Value: "/tmp/x"}},
		{{Container: "app", Name: "A", Value: "1"}, {Container: "app", Name: "A", Value: "2"}},
		{{Container: "app", Name: "A", Value: "two\nlines"}},
	} {
		if err := m.SetEnv(ctx, "forgejo", bad, nil); !isUserErr(err) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if err := m.SetEnv(ctx, "nope", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown service: %v", err)
	}
	e, err := m.ServiceEnv("forgejo")
	if err != nil || e.Containers[0] != "app" || len(e.Vars) != 2 || len(e.Managed["app"]) == 0 {
		t.Errorf("ServiceEnv: %+v %v", e, err)
	}
}

func loadInstance(t *testing.T, m *Manager, name string) *instance.Instance {
	t.Helper()
	in, err := instance.Load(state.InstanceFile(m.Lab, name))
	if err != nil {
		t.Fatal(err)
	}
	return in
}
