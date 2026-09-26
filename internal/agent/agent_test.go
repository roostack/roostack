package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeHost struct {
	portsSet bool
	ruleset  string
	tz       string
}

func (f *fakeHost) SetUnprivilegedPortStart() error { f.portsSet = true; return nil }
func (f *fakeHost) ApplyFirewall(r string) error    { f.ruleset = r; return nil }
func (f *fakeHost) SetTimezone(name string) error   { f.tz = name; return nil }

// start runs a real server on a Unix socket, so SO_PEERCRED is exercised.
func start(t *testing.T, allowed []uint32) (*Client, *fakeHost) {
	t.Helper()
	host := &fakeHost{}
	s := &Server{Host: host, AllowedUIDs: allowed, Version: "test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	socket := filepath.Join(t.TempDir(), "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ctx, socket, os.Getgid()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return NewClient(socket), host
}

func TestAPI(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	ctx := context.Background()

	if v, err := c.Health(ctx); err != nil || v != "test" {
		t.Fatalf("health = %q, %v", v, err)
	}
	if err := c.SetUnprivilegedPortStart(ctx); err != nil || !host.portsSet {
		t.Errorf("ports: %v, set=%v", err, host.portsSet)
	}
	if err := c.ApplyFirewall(ctx, DefaultFirewall()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(host.ruleset, "tcp dport 443 accept") {
		t.Errorf("ruleset not applied:\n%s", host.ruleset)
	}

	if err := c.SetTimezone(ctx, "Australia/Melbourne"); err != nil || host.tz != "Australia/Melbourne" {
		t.Errorf("timezone: %v, set=%q", err, host.tz)
	}
	for _, tz := range []string{"", "../etc/passwd", "-h", "Europe/../../x", "a/b/c/d", "Europe/Berlin;id"} {
		if err := c.SetTimezone(ctx, tz); err == nil {
			t.Errorf("timezone %q accepted", tz)
		}
	}

	bad := DefaultFirewall()
	bad.Allow = append(bad.Allow, Rule{Port: 0, Proto: "tcp", From: FromAny})
	if err := c.ApplyFirewall(ctx, bad); err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("want invalid port error, got %v", err)
	}
}

func TestRejectsOtherUsers(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid()) + 12345})
	if err := c.SetUnprivilegedPortStart(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want 403, got %v", err)
	}
	if host.portsSet {
		t.Error("action must not run for a rejected caller")
	}
}

func TestRuleset(t *testing.T) {
	fw := DefaultFirewall()
	fw.Allow = append(fw.Allow, Rule{Port: 53, Proto: "udp", From: FromLAN, Reason: "pihole dns"})
	got := fw.Ruleset()
	for _, want := range []string{
		"policy drop;",
		"ip saddr @lan4 tcp dport 22 accept", // SSH is LAN-only by default
		"ip6 saddr @lan6 udp dport 53 accept",
		"tcp dport 80 accept",
		"udp dport 443 accept",
		"delete table inet mylab",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ruleset missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "dport 22") > strings.Index(got, "dport 53") {
		t.Error("rules should be sorted by port")
	}
}

func TestFirewallValidate(t *testing.T) {
	for _, fw := range []Firewall{
		{SSHFrom: "everyone"},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "sctp", From: FromAny}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp", From: "internet"}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp", From: FromAny, Reason: "x\"\n}"}}},
	} {
		if err := fw.Validate(); err == nil {
			t.Errorf("expected an error for %+v", fw)
		}
	}
}
