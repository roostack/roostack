package manager

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/config"
)

// What the domain itself shows (SPEC.md §5.3): saved, carried to Caddy by
// the apply, and among the lab's public names (and Roostack Cloud's, as
// Roostack's own) only while it's public.
func TestDomainItself(t *testing.T) {
	m, _ := newManager(t)
	if err := m.SetDomainItself(config.DomainItself{Show: config.ShowAddress, Address: "ftp://x"}); err == nil {
		t.Error("an incomplete choice was saved")
	}
	if err := m.SetDomainItself(config.DomainItself{Show: config.ShowAddress, Address: "https://blog.example/"}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	cf, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddyDir(), "Caddyfile"))
	if !strings.Contains(string(cf), "redir https://blog.example/ 302") || slices.Contains(m.PublicHosts(), "lab.example.com") {
		t.Errorf("home-only: %v\n%s", m.PublicHosts(), cf)
	}
	if err := m.SetDomainItself(config.DomainItself{Show: config.ShowAccount, Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	cf, _ = os.ReadFile(filepath.Join(m.Lab.Paths.CaddyDir(), "Caddyfile"))
	if !strings.Contains(string(cf), "redir https://my.lab.example.com/ 302") || !slices.Contains(m.PublicHosts(), "lab.example.com") {
		t.Errorf("public: %v\n%s", m.PublicHosts(), cf)
	}
	m.Lab.Public.Relay = true
	m.saveLab(func(l *config.Lab) { l.Public.Relay = true })
	if told := m.CloudPublicHosts(); !slices.Contains(told, cloudapi.PublicHost{Host: "lab.example.com", Kind: cloudapi.HostRoostack}) {
		t.Errorf("Roostack Cloud is told %+v", told)
	}
}
