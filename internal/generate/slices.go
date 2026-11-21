package generate

import (
	"fmt"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/quadlet"
	"roostack.cloud/roostack/units"
)

// Memory for Roostack and the services (SPEC.md §6.1, "Memory"): the
// roostack user's systemd manager runs Caddy in a slice of its own, which
// the kernel takes memory from last, and every service in a slice of its
// own under one for all the services, which together may use the server's
// memory less what's kept for the system and Roostack. Past it, or past a
// service's own limit, the kernel slows them down and moves their memory
// to swap: nothing is killed (MemoryHigh, never MemoryMax).
//
// systemd nests slices by their names' hyphens, so roostack-services-
// outline.slice is inside roostack-services.slice, inside roostack.slice.
const (
	RoostackSlice = "roostack.slice"
	CoreSlice     = "roostack-core.slice"
	ServicesSlice = "roostack-services.slice"
	// MemoryKept is what the system and Roostack keep, whatever the
	// services do: the maintainer chose 400 MB on any server (2026-11-19).
	MemoryKept = 400_000_000
	// memoryFloor is the least a service's automatic limit leaves above
	// its figure, for a small service whose figure doubled is still small.
	memoryFloor = 256_000_000
)

// ServiceSlice is the slice a service's containers run in. A service's
// name may have hyphens, which systemd would read as more slices, and
// never "_", so each "-" is written "_".
func ServiceSlice(name string) string {
	return "roostack-services-" + strings.ReplaceAll(name, "-", "_") + ".slice"
}

// ServicesMemory is what all the services together may use on a server
// with this much memory before the kernel slows them down: all of it less
// MemoryKept, and never less than half. 0 (no cap) when the server's
// memory isn't known.
func ServicesMemory(total int64) int64 {
	if total <= 0 {
		return 0
	}
	return max(total-MemoryKept, total/2)
}

// sliceFile is a slice's unit file.
func sliceFile(name, description string, lines ...string) File {
	var b strings.Builder
	b.WriteString(quadlet.Header)
	fmt.Fprintf(&b, "\n[Unit]\nDescription=%s\n\n[Slice]\n", description)
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return File{Name: name, Content: b.String()}
}

// systemSlices are the slices for Caddy and for the services together, on
// a server with this much memory (0: not known, so no cap).
func systemSlices(total int64) []File {
	services := []string{"CPUWeight=100"}
	if cap := ServicesMemory(total); cap > 0 {
		services = append(services, fmt.Sprintf("MemoryHigh=%d", cap))
	}
	return []File{
		// Every slice above Caddy's keeps as much as it does, as the kernel
		// honours a slice's MemoryLow only as far as its parents do; the
		// package does the same above the roostack user's manager.
		sliceFile(RoostackSlice, "Roostack: Caddy and the services", "MemoryLow=96M"),
		// Caddy uses about 50 MB of its own.
		sliceFile(CoreSlice, "Roostack: Caddy, kept in memory ahead of the services",
			"MemoryLow=96M", "CPUWeight=200"),
		sliceFile(ServicesSlice, "Roostack: the services, which together may use all but "+units.Bytes(MemoryKept)+" of the server's memory", services...),
	}
}

// ServiceSliceFile is a service's slice, with its memory limit (0: none).
func ServiceSliceFile(name string, limit int64) File {
	if limit <= 0 {
		return sliceFile(ServiceSlice(name), "Roostack: "+name)
	}
	return sliceFile(ServiceSlice(name), "Roostack: "+name+", slowed down past "+units.Bytes(limit), fmt.Sprintf("MemoryHigh=%d", limit))
}

// MemoryLimit is a service's own memory limit on a server, in bytes (0:
// none), and why, in words for its page:
//   - the Owner's choice (instance.MemoryLimit), a figure or none, always;
//   - otherwise automatic: twice the memory its package expects, and at
//     least 256 MB above it, only while tight (the services on the server
//     together expect more than they may use, ServicesMemory), and only
//     when every part of it says what it expects.
//
// On a server with room to spare, a service isn't held to its figure, so
// one whose needs vary a lot (Immich's machine learning, transcoding in
// Jellyfin or Plex) can use what it needs (SPEC.md §6.1, "Memory").
func MemoryLimit(lab config.Lab, p *pkgspec.Package, in *instance.Instance, tight bool) (int64, error) {
	switch in.MemoryLimit {
	case instance.MemoryNoLimit:
		return 0, nil
	case "":
	default:
		return pkgspec.ParseMemory(in.MemoryLimit)
	}
	if !tight {
		return 0, nil
	}
	figure, known, err := MemoryFigure(lab, p, in)
	if err != nil || !known {
		return 0, err
	}
	return max(2*figure, figure+memoryFloor), nil
}

// MemoryFigure is what a service is expected to use with its settings,
// and whether every part of it says (a part that doesn't counts as 0).
func MemoryFigure(lab config.Lab, p *pkgspec.Package, in *instance.Instance) (total int64, known bool, err error) {
	parts, err := memoryParts(lab, p, in)
	if err != nil {
		return 0, false, err
	}
	known = len(parts) > 0
	for _, part := range parts {
		total += part.Memory
		if part.Memory == 0 {
			known = false
		}
	}
	return total, known, nil
}
