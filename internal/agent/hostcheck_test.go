package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAptSources(t *testing.T) {
	readFile = func(name string) ([]byte, error) {
		switch name {
		case "/etc/apt/mirrors/debian.list":
			return []byte("https://deb.debian.org/debian\n"), nil
		case "/etc/apt/mirrors/other.list":
			return []byte("https://deb.debian.org/debian\nhttps://mirror.example.com/debian\n"), nil
		}
		return nil, os.ErrNotExist
	}
	defer func() { readFile = os.ReadFile }()
	files := map[string]string{
		"/etc/apt/sources.list.d/cloud.sources": "Types: deb\nURIs: mirror+file:///etc/apt/mirrors/debian.list\n\nTypes: deb\nURIs: mirror+file:///etc/apt/mirrors/other.list\n",
		"/etc/apt/sources.list.d/debian.sources": `Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main

Types: deb
URIs: http://security.debian.org/debian-security
Suites: trixie-security
`,
		"/etc/apt/sources.list.d/docker.list":  "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable\n",
		"/etc/apt/sources.list":                "# deb http://example.com/old stable main\ndeb http://ftp.au.debian.org/debian trixie main\n",
		"/etc/apt/sources.list.d/off.sources":  "Types: deb\nURIs: https://repo.example.com/apt\nEnabled: no\n",
		"/etc/apt/sources.list.d/tail.sources": "Types: deb\nURIs: https://pkgs.tailscale.com/stable/debian\nSuites: trixie\n",
		// Roostack's own, here from a mirror: still checked with its key.
		"/etc/apt/sources.list.d/roostack.sources": "# Roostack\nTypes: deb\nURIs: https://mirror.example.com/roostack\nSuites: 0.4\n" +
			"Signed-By: /usr/share/keyrings/roostack-archive-keyring.gpg\n",
		// The same name without Roostack's key is someone else's.
		"/var/x/roostack.sources": "Types: deb\nURIs: https://evil.example.com/apt\nSuites: 0.4\nSigned-By: /etc/apt/keyrings/other.gpg\n",
	}
	got := aptSources(files)
	want := []string{"cloud.sources: mirror+file:///etc/apt/mirrors/other.list",
		"docker.list: https://download.docker.com/linux/debian", "tail.sources: https://pkgs.tailscale.com/stable/debian",
		"roostack.sources: https://evil.example.com/apt"}
	if !slices.Equal(got, want) {
		t.Errorf("sources = %q, want %q", got, want)
	}
}

func TestForeignTables(t *testing.T) {
	got := foreignTables("table inet roostack\ntable ip filter\ntable ip6 nat\n")
	if !slices.Equal(got, []string{"nftables: ip filter", "nftables: ip6 nat"}) {
		t.Errorf("tables = %q", got)
	}
	if foreignTables("table inet roostack\n") != nil {
		t.Error("Roostack's own table isn't a change")
	}
}

func TestHostChangesAPI(t *testing.T) {
	c, _ := start(t, []uint32{uint32(os.Getuid())})
	hc, err := c.HostChanges(context.Background())
	if err != nil || !hc.Any() || hc.Held[0] != "linux-image-amd64" || hc.Fingerprint() == (HostChanges{}).Fingerprint() {
		t.Errorf("host changes = %+v, %v", hc, err)
	}
}

// A cloud provider's Debian mirror isn't software from outside Debian:
// apt's release files for it (checked against Debian's keys) say
// "Origin: Debian". Another repository's say otherwise.
func TestDebianMirrors(t *testing.T) {
	dir := t.TempDir()
	aptLists = dir
	defer func() { aptLists = "/var/lib/apt/lists" }()
	for name, origin := range map[string]string{
		"mirrors.linode.com_debian_dists_trixie_InRelease":                           "Debian",
		"mirrors.linode.com_debian-security_dists_trixie-security_updates_InRelease": "Debian",
		"download.docker.com_linux_debian_dists_trixie_InRelease":                    "Docker",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("-----BEGIN PGP SIGNED MESSAGE-----\nOrigin: "+origin+"\nLabel: x\n"), 0o644)
	}
	files := map[string]string{
		"/etc/apt/sources.list": "deb http://mirrors.linode.com/debian trixie main\n" +
			"deb http://mirrors.linode.com/debian-security trixie-security/updates main\n" +
			"deb https://download.docker.com/linux/debian trixie stable\n" +
			"deb http://mirrors.example.net/debian trixie main\n", // never downloaded: unknown
	}
	got := aptSources(files)
	want := []string{"sources.list: https://download.docker.com/linux/debian", "sources.list: http://mirrors.example.net/debian"}
	if !slices.Equal(got, want) {
		t.Errorf("sources = %q, want %q", got, want)
	}
}

// The OS updates log leaves out unattended-upgrade's dpkg log markers.
func TestUpdatesLogFilter(t *testing.T) {
	var out strings.Builder
	f := &lineFilter{w: &out, drop: dpkgLogMarker}
	f.Write([]byte("==> Installing updates\nLog started: 2026-09-30  23:22:32\nSetting up curl ...\nLog ended: 2026-09-30  23:22:33\n\nAll up"))
	f.Write([]byte("grades done\n"))
	f.flush()
	if want := "==> Installing updates\nSetting up curl ...\n\nAll upgrades done\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
}
