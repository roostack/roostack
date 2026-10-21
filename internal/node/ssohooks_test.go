package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// photosPackage is an app set through its own API, like Immich: its
// install makes a key Roostack keeps, Connect makes one again, and its sso
// hook uses it (SPEC.md §5.2, "Connecting the remaining apps").
const photosPackage = `
schema: roostack/v1alpha1
name: photos
title: Photos
version: 1.0.0-roostack.1
settings:
  - { key: admin_email, title: Email, type: string, required: true, install_only: true }
  - { key: admin_password, title: Password, type: secret, install_only: true }
  - { key: settings_key, title: Roostack's key to Photos' settings, type: secret, from_hook: true }
containers:
  server:
    image: example.org/photos:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
routes:
  web: { container: server, port: 2283, subdomain: photos, exposure: private }
hooks:
  - on: install
    container: server
    secrets: { ADMIN_PASSWORD: admin_password }
    saves: settings_key
    command: [make-key]
  - on: action
    name: connect
    title: Connect Roostack to Photos
    container: server
    inputs:
      - { key: password, title: Password, type: secret }
    secrets: { ADMIN_PASSWORD: inputs.password }
    saves: settings_key
    command: [make-key]
  - on: sso
    container: server
    env: { ISSUER: "{{ .SSO.Issuer }}" }
    secrets: { API_KEY: settings_key, CLIENT_SECRET: sso.client_secret }
    command: [set-sign-in]
sso:
  oidc:
    route: web
    redirect_paths: [/auth/login]
    groups: { users: user, admins: admin }
`

// photosExec plays the app: make-key prints a new key on standard output
// and its log on standard error; set-sign-in echoes what it was given.
type photosExec struct {
	mu    sync.Mutex
	keys  int
	calls []map[string]string
	print string // make-key's standard output, if set
}

func (f *photosExec) exec(_ context.Context, _ string, env map[string]string, cmd []string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, env)
	switch cmd[0] {
	case "make-key":
		if env["ADMIN_PASSWORD"] != "correct horse" {
			return nil, []byte("Signing in failed: wrong password " + env["ADMIN_PASSWORD"]), errors.New("exit status 1")
		}
		f.keys++
		if f.print != "" {
			return []byte(f.print), nil, nil
		}
		return []byte("key-" + string(rune('0'+f.keys)) + "-abcdef\n"), []byte("Made a key for Roostack.\n"), nil
	case "set-sign-in":
		return []byte("Using key " + env["API_KEY"] + " and secret " + env["CLIENT_SECRET"] + " (" + env["ROOSTACK_SSO"] + ")\n"), nil, nil
	}
	return nil, nil, errors.New("unexpected command")
}

type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) List(context.Context) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for k := range s.m {
		out[k] = true
	}
	return out, nil
}
func (s *memSecrets) Read(_ context.Context, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[name]
	if !ok {
		return nil, errors.New("no such secret")
	}
	return v, nil
}
func (s *memSecrets) Remove(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
	return nil
}
func (s *memSecrets) Replace(_ context.Context, name string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = v
	return nil
}

func TestSavedKeyAndSSOHook(t *testing.T) {
	p, err := pkgspec.Parse([]byte(photosPackage))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := generate.Check(p); err != nil {
		t.Fatal(err)
	}
	lab := config.Default()
	lab.Domain = "lab.example.com"
	in := &instance.Instance{Package: "photos", Version: p.Version, Updates: instance.UpdatesNotify, SSO: true,
		Routes: map[string]instance.RouteOverride{"web": {Port: 20040}}}
	svc := state.Service{Package: p, Instance: in}
	secrets := &memSecrets{m: map[string][]byte{
		"roostack-photos-admin_password":     []byte("correct horse"),
		generate.SSOSecretName("photos"):     []byte("client-secret-xyz"),
		"roostack-photos-sso-env-old-0123ab": []byte("left over"),
	}}
	ex := &photosExec{}
	kv := memKV{}
	n := &Local{Lab: lab, State: kv, Secrets: secrets, Exec: ex.exec, Activity: activity.New(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), HookRetry: time.Millisecond, HookDeadline: 50 * time.Millisecond}
	n.assignment.Services = []state.Service{svc}
	ctx := context.Background()

	// Install: the key Photos made is kept, and only the log is shown.
	if err := n.runInstallHooks(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if got := string(secrets.m["roostack-photos-settings_key"]); got != "key-1-abcdef" {
		t.Errorf("kept %q", got)
	}
	setupLog, _ := n.Activity.Latest(activity.Setup)
	logText := strings.Join(setupLog.Lines, "\n")
	if strings.Contains(logText, "key-1") || !strings.Contains(logText, "Made a key for Roostack.") {
		t.Errorf("setup log:\n%s", logText)
	}
	kv[hookKey("photos")] = hookDone

	// Not while it waits for its data: its app isn't running.
	in.AwaitingData = true
	calls := len(ex.calls)
	n.runSSOHooks(ctx)
	if n.ssoRecord(ctx, "photos") != nil || len(ex.calls) != calls {
		t.Fatal("its sso hook ran while it waits for its data")
	}
	in.AwaitingData = false

	// Its sso hook gets the key and the client secret; neither is shown.
	n.runSSOHooks(ctx)
	st := n.ssoStatus(ctx, "photos")
	if !st.On || st.Failed != "" || st.NeedsKey != "" {
		t.Fatalf("status %+v", st)
	}
	if strings.Contains(st.Output, "key-1") || strings.Contains(st.Output, "client-secret-xyz") ||
		!strings.Contains(st.Output, "Using key [secret] and secret [secret] (on)") {
		t.Errorf("output %q", st.Output)
	}
	if _, left := secrets.m["roostack-photos-sso-env-old-0123ab"]; left {
		t.Error("an old secret env value wasn't removed")
	}

	// The key is lost: the hook fails, saying to connect, and the card
	// offers Connect.
	delete(secrets.m, "roostack-photos-settings_key")
	lab.Domain = "home.example.com" // its issuer changes, so it runs again
	n.Lab = lab
	n.runSSOHooks(ctx)
	st = n.ssoStatus(ctx, "photos")
	if st.NeedsKey != "connect" || !strings.Contains(st.Failed, `use "Connect Roostack to Photos"`) {
		t.Errorf("missing key: %+v", st)
	}

	// Connect with a wrong password: the password isn't shown back.
	_, err = n.RunAction(ctx, "photos", "connect", map[string]string{"password": "wrong horse"})
	var ae *ActionError
	if !errors.As(err, &ae) || strings.Contains(ae.Output, "wrong horse") || !strings.Contains(ae.Output, "wrong password [secret]") {
		t.Errorf("wrong password: %v %+v", err, ae)
	}
	out, err := n.RunAction(ctx, "photos", "connect", map[string]string{"password": "correct horse"})
	if err != nil || strings.Contains(out, "key-2") || !strings.Contains(out, "Roostack kept the key") {
		t.Fatalf("connect: %q %v", out, err)
	}
	if got := string(secrets.m["roostack-photos-settings_key"]); got != "key-2-abcdef" {
		t.Errorf("kept %q", got)
	}
	if !n.retrySSO(ctx, "photos") {
		t.Fatal("nothing to try again")
	}
	n.runSSOHooks(ctx)
	if st := n.ssoStatus(ctx, "photos"); st.Failed != "" || st.NeedsKey != "" || !st.On {
		t.Errorf("after connecting: %+v", st)
	}

	// Something that isn't a key isn't kept.
	ex.print = "line one\nline two"
	if _, err := n.RunAction(ctx, "photos", "connect", map[string]string{"password": "correct horse"}); err == nil ||
		!strings.Contains(err.Error(), "isn't a key") {
		t.Errorf("not a key: %v", err)
	}
	if got := string(secrets.m["roostack-photos-settings_key"]); got != "key-2-abcdef" {
		t.Errorf("kept %q after a bad key", got)
	}

	// Off: told off without the client secret, which then goes.
	in.SSO = false
	n.runSSOHooks(ctx)
	last := ex.calls[len(ex.calls)-1]
	if last["ROOSTACK_SSO"] != "off" || last["CLIENT_SECRET"] != "" || last["API_KEY"] != "key-2-abcdef" {
		t.Errorf("off run's env: %v", last)
	}
	if _, kept := secrets.m[generate.SSOSecretName("photos")]; kept {
		t.Error("the client secret must go once it's off")
	}
	if _, kept := secrets.m["roostack-photos-settings_key"]; !kept {
		t.Error("the key must stay: turning it on again uses it")
	}
}
