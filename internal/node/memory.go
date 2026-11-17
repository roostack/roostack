package node

import (
	"bufio"
	"context"
	"errors"
	"io"
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
	// Swapped is program memory the kernel has moved out to swap, on disk
	// only (SwapTotal less SwapFree and SwapCached, which is in memory
	// too): what programs hold beyond what's in memory, which they wait
	// for when they next use it.
	Swapped int64
}

// Memory is a server that can report its memory (Local and Remote), for
// the install summary to compare a new service's needs with (SPEC.md
// §8.3.4), and with how busy its processors are, for Home (usage.go).
type Memory interface {
	MemoryInfo(ctx context.Context) (MemoryInfo, error)
	Usage(ctx context.Context) (Usage, error)
}

// Memory is this server, which reads its own memory.
func (n *Local) Memory() Memory { return n }

var (
	_ Memory = (*Local)(nil)
	_ Memory = (*Remote)(nil)
)

// MemoryInfo reads this server's memory.
func (n *Local) MemoryInfo(context.Context) (MemoryInfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemoryInfo{}, err
	}
	defer f.Close()
	return parseMeminfo(f)
}

// parseMeminfo reads MemTotal, MemAvailable, and swap ("MemTotal:
// 8000000 kB"; the kernel's kB are 1024 bytes).
func parseMeminfo(f io.Reader) (MemoryInfo, error) {
	var m MemoryInfo
	var swapTotal, swapFree, swapCached int64
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
		case "SwapTotal":
			to = &swapTotal
		case "SwapFree":
			to = &swapFree
		case "SwapCached":
			to = &swapCached
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
	m.Swapped = max(0, swapTotal-swapFree-swapCached)
	if m.Total <= 0 || m.Available <= 0 {
		return MemoryInfo{}, errors.New("meminfo: no MemTotal or MemAvailable")
	}
	return m, nil
}
