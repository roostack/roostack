package node

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// MemoryInfo is a server's memory, in bytes, as /proc/meminfo has it.
type MemoryInfo struct {
	Total int64
	// Available is what can be used now without swapping: free memory,
	// plus caches the kernel gives back (MemAvailable). It already
	// counts what's running.
	Available int64
}

// Memory is a server that can report its memory (Local and Remote), for
// the install summary to compare a new service's needs with (SPEC.md
// §8.3.4).
type Memory interface {
	Memory(ctx context.Context) (MemoryInfo, error)
}

var (
	_ Memory = (*Local)(nil)
	_ Memory = (*Remote)(nil)
)

// Memory reads this server's memory.
func (n *Local) Memory(context.Context) (MemoryInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemoryInfo{}, err
	}
	defer f.Close()
	return parseMeminfo(f)
}

// parseMeminfo reads MemTotal and MemAvailable ("MemTotal: 8000000 kB";
// the kernel's kB are 1024 bytes).
func parseMeminfo(f io.Reader) (MemoryInfo, error) {
	var m MemoryInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		var to *int64
		switch key {
		case "MemTotal":
			to = &m.Total
		case "MemAvailable":
			to = &m.Available
		default:
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		if err != nil {
			return MemoryInfo{}, err
		}
		*to = kb * 1024
	}
	if err := sc.Err(); err != nil {
		return MemoryInfo{}, err
	}
	if m.Total <= 0 || m.Available <= 0 {
		return MemoryInfo{}, errors.New("meminfo: no MemTotal or MemAvailable")
	}
	return m, nil
}

// Memory asks the server.
func (r *Remote) Memory(ctx context.Context) (MemoryInfo, error) {
	var out MemoryInfo
	err := r.call(ctx, http.MethodGet, "/v1/memory", nil, &out)
	return out, err
}
