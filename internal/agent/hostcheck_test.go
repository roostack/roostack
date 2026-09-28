package agent

import (
	"context"
	"os"
	"slices"
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
	}
	got := aptSources(files)
	want := []string{"cloud.sources: mirror+file:///etc/apt/mirrors/other.list",
		"docker.list: https://download.docker.com/linux/debian", "tail.sources: https://pkgs.tailscale.com/stable/debian"}
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
