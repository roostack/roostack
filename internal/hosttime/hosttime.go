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
