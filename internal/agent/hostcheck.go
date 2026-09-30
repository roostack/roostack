package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// HostChanges are changes to the host that Roostack didn't make and that can
// get in the way of managing it (SPEC.md §3.2): software from outside
// Debian, packages held back from updates, and another firewall.
type HostChanges struct {
	// AptSources are package sources other than Debian's ("<file>: <URI>").
	AptSources []string `json:"apt_sources,omitempty"`
	// Held are packages held back from updates (apt-mark hold).
	Held []string `json:"held,omitempty"`
	// Firewalls are firewall rules Roostack didn't make: other nftables tables
	// ("nftables: ip filter"), or ufw or firewalld running.
	Firewalls []string `json:"firewalls,omitempty"`
	// FirewallMissing: Roostack's own firewall isn't loaded, though it set
	// one up.
	FirewallMissing bool `json:"firewall_missing,omitempty"`
}

// Any reports whether there is anything to tell.
func (c HostChanges) Any() bool {
	return len(c.AptSources)+len(c.Held)+len(c.Firewalls) > 0 || c.FirewallMissing
}

// Fingerprint identifies this set of changes, so an Owner who made them on
// purpose can say so until something else changes.
func (c HostChanges) Fingerprint() string {
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// readFile reads a mirror list (tests replace it); aptLists is where apt
// keeps the release files it downloaded.
var (
	readFile = os.ReadFile
	aptLists = "/var/lib/apt/lists"
)

// debianSource reports whether a package source is Debian's: on a Debian
// host name, or a mirror of Debian (a cloud provider's, such as
// mirrors.linode.com) whose release files, which apt checked against
// Debian's signing keys, say "Origin: Debian".
func debianSource(uri string) bool {
	return debianHost(uri) || debianMirror(uri)
}

// debianMirror looks at the release files apt downloaded from uri. apt
// names them after the address: mirrors.linode.com/debian's are
// mirrors.linode.com_debian_dists_<suite>_InRelease.
func debianMirror(uri string) bool {
	rest, ok := strings.CutPrefix(uri, "https://")
	if !ok {
		if rest, ok = strings.CutPrefix(uri, "http://"); !ok {
			return false
		}
	}
	prefix := strings.ReplaceAll(strings.TrimSuffix(rest, "/"), "/", "_") + "_dists_"
	if strings.ContainsAny(prefix, "*?[") {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(aptLists, prefix+"*Release"))
	for _, f := range files {
		data, err := readFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "Origin: Debian" {
				return true
			}
		}
	}
	return false
}

// debianHost reports whether a package source is Debian's own. A mirror
// list (mirror+file://, as Debian's cloud images use) is if every mirror
// in it is.
func debianHost(uri string) bool {
	if path, ok := strings.CutPrefix(uri, "mirror+file://"); ok {
		data, err := readFile(path)
		if err != nil {
			return false
		}
		n := 0
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) == 0 || strings.HasPrefix(f[0], "#") {
				continue
			}
			if n++; !debianHost(f[0]) {
				return false
			}
		}
		return n > 0
	}
	uri = strings.TrimPrefix(strings.TrimPrefix(uri, "https://"), "http://")
	host, _, _ := strings.Cut(uri, "/")
	return host == "debian.org" || strings.HasSuffix(host, ".debian.org")
}

// aptSources lists the non-Debian sources in apt's configuration files
// (one-line .list files and deb822 .sources files), by file name.
func aptSources(files map[string]string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(files)) {
		// A deb822 stanza's URIs count once it's known whether it's enabled.
		var stanza []string
		enabled := true
		flush := func() {
			for _, u := range stanza {
				if enabled && !debianSource(u) {
					out = append(out, filepath.Base(name)+": "+u)
				}
			}
			stanza, enabled = nil, true
		}
		sc := bufio.NewScanner(strings.NewReader(files[name]))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch {
			case line == "":
				flush()
			case strings.HasPrefix(line, "#"):
			case strings.HasPrefix(line, "deb ") || strings.HasPrefix(line, "deb-src "):
				for _, w := range strings.Fields(line)[1:] { // one-line format
					if strings.Contains(w, "://") {
						if !debianSource(w) {
							out = append(out, filepath.Base(name)+": "+w)
						}
						break
					}
				}
			default:
				k, v, _ := strings.Cut(line, ":")
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "enabled":
					enabled = strings.TrimSpace(v) != "no"
				case "uris":
					stanza = append(stanza, strings.Fields(v)...)
				}
			}
		}
		flush()
	}
	return out
}

// foreignTables lists nftables tables other than Roostack's, from
// `nft list tables`.
func foreignTables(list string) []string {
	var out []string
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "table" {
			continue
		}
		if f[1] == "inet" && f[2] == "roostack" {
			continue
		}
		out = append(out, "nftables: "+f[1]+" "+f[2])
	}
	return out
}

// HostChanges checks the host for changes Roostack didn't make.
func (h LinuxHost) HostChanges() (HostChanges, error) {
	var c HostChanges
	files := map[string]string{}
	paths, _ := filepath.Glob("/etc/apt/sources.list.d/*")
	for _, p := range append(paths, "/etc/apt/sources.list") {
		if !strings.HasSuffix(p, ".list") && !strings.HasSuffix(p, ".sources") {
			continue
		}
		if data, err := os.ReadFile(p); err == nil {
			files[p] = string(data)
		}
	}
	c.AptSources = aptSources(files)
	if out, err := exec.Command("apt-mark", "showhold").Output(); err == nil {
		c.Held = strings.Fields(string(out))
	}
	out, err := exec.Command("nft", "list", "tables").Output()
	if err != nil {
		return c, errors.New("nft list tables: " + err.Error())
	}
	c.Firewalls = foreignTables(string(out))
	for _, unit := range []string{"ufw", "firewalld"} {
		if exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil {
			c.Firewalls = append(c.Firewalls, unit+" is running")
		}
	}
	if _, err := os.Stat(h.FirewallFile); err == nil && !strings.Contains(string(out), "table inet roostack") {
		c.FirewallMissing = true
	}
	return c, nil
}
