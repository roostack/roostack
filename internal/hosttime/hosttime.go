// Package hosttime reads the host's time zone settings. Changing them is
// the agent's job (roostack-agent runs timedatectl); this package only reads,
// so it works inside the roostack service's sandbox.
package hosttime

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Files read, overridable in tests.
var (
	LocaltimeFile = "/etc/localtime"
	TzdataFile    = "/usr/share/zoneinfo/tzdata.zi"
	// ZoneTabFiles list the zones of each country by their current names.
	ZoneTabFiles = []string{"/usr/share/zoneinfo/zone1970.tab", "/usr/share/zoneinfo/zone.tab"}
)

// Zone returns the host's configured time zone name, such as
// "Europe/Berlin", falling back to "UTC".
//
// It reads the /etc/localtime link rather than using time.Local, which is
// fixed when the process starts.
func Zone() string {
	target, err := os.Readlink(LocaltimeFile)
	if err != nil {
		return "UTC"
	}
	if _, name, ok := strings.Cut(filepath.ToSlash(target), "zoneinfo/"); ok && name != "" {
		return name
	}
	return "UTC"
}

// Zones lists the time zone names the host knows, sorted: the zones and
// links in tzdata.zi, which is also what timedatectl accepts.
func Zones() ([]string, error) {
	f, err := os.Open(TzdataFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var zones []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		switch {
		case len(fields) >= 2 && fields[0] == "Z": // Z <name> ...
			zones = append(zones, fields[1])
		case len(fields) == 3 && fields[0] == "L": // L <target> <name>
			zones = append(zones, fields[2])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	slices.Sort(zones)
	return slices.Compact(zones), nil
}

// utcNames are the other names for UTC, and for GMT, which is the same all
// year. Apps' own lists of zones may not have them (PHP's has only "UTC"),
// but every list has "UTC".
var utcNames = []string{"Etc/UTC", "Etc/UCT", "Etc/Universal", "Etc/Zulu", "UCT", "Universal", "Zulu",
	"Etc/GMT", "Etc/GMT0", "Etc/GMT+0", "Etc/GMT-0", "Etc/Greenwich", "GMT", "GMT0", "GMT+0", "GMT-0", "Greenwich"}

// ForApps returns the name of a zone that apps are most likely to accept,
// for passing to an app that checks names against its own list, as PHP's
// apps do (SPEC.md §8.1, "The time zone apps are given"). Any other name for
// UTC is "UTC", an old name of a zone is its current one ("Asia/Calcutta" is
// "Asia/Kolkata"), and anything else, or anything when the host's lists
// can't be read, is unchanged. An empty name is "UTC".
//
// Debian's own default is "Etc/UTC", which IANA counts as the zone's main
// name but PHP's list of zones leaves out.
func ForApps(name string) string {
	if name == "" || name == "UTC" || slices.Contains(utcNames, name) {
		return "UTC"
	}
	current := currentNames()
	if current[name] {
		return name
	}
	if target := linkTarget(name); target != "" && (current[target] || slices.Contains(utcNames, target)) {
		return ForApps(target)
	}
	return name
}

// currentNames are the zones in ZoneTabFiles.
func currentNames() map[string]bool {
	names := map[string]bool{}
	for _, file := range ZoneTabFiles {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(b)) {
			// country<TAB>coordinates<TAB>zone[<TAB>comment]
			if fields := strings.Split(strings.TrimRight(line, "\n"), "\t"); len(fields) >= 3 && !strings.HasPrefix(line, "#") {
				names[fields[2]] = true
			}
		}
	}
	return names
}

// linkTarget returns the zone that tzdata.zi links name to, or "".
func linkTarget(name string) string {
	f, err := os.Open(TzdataFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 3 && fields[0] == "L" && fields[2] == name {
			return fields[1]
		}
	}
	return ""
}

// Clock is the host's time as the UI shows it.
type Clock struct {
	Zone string
	// Offset is minutes east of UTC now, to compare with the browser's.
	Offset int
	Time   string // "15:04"
}

// Now returns the host's clock. It uses time.Local, which is what every
// time Roostack shows is in, so it stays right until Roostack restarts after a
// time zone change.
func Now() Clock {
	now := time.Now()
	_, off := now.Zone()
	return Clock{Zone: Zone(), Offset: off / 60, Time: now.Format("15:04")}
}
