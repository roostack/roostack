package manager

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// A server's backup plan (SPEC.md §6.9): backup 1 (its main backup, which
// restores read first) and its copies, each on some kind of storage, at
// home or away from it. The 3-2-1 checks and the suggestions for empty
// slots come from these.

// Backup kinds.
const (
	KindUSB     = "usb"     // a USB drive on the server
	KindDisk    = "disk"    // a second disk
	KindNetwork = "network" // a network share (a NAS)
	KindSystem  = "system"  // the server's own disk
	KindFolder  = "folder"  // a folder on the server
	KindOnline  = "online"  // S3-compatible online storage
	KindFriend  = "friend"  // storage at another lab
	KindServer  = "server"  // another server's disk, in this lab
	KindOther   = "other"
)

// BackupKind says what kind of storage a destination is.
func (m *Manager) BackupKind(ctx context.Context, serverID, ref string) string {
	kind, id, _ := strings.Cut(ref, ":")
	switch kind {
	case "drive":
		switch {
		case id == "system":
			return KindSystem
		case strings.HasPrefix(id, "mount:"):
			return KindNetwork
		}
		if m.Drives != nil {
			if drives, err := m.Drives.Drives(ctx); err == nil {
				for _, d := range drives {
					if d.ID == id && d.Kind == "usb" {
						return KindUSB
					}
				}
			}
		}
		return KindDisk
	case "folder", "local":
		// A folder in Roostack's own data (the default on the server's
		// disk) is the server's disk; every server has the same layout.
		for _, dir := range []string{m.Lab.Paths.BackupStoreDir(), m.Lab.Paths.StateDir} {
			if dir != "" && dir != "." && strings.HasPrefix(filepath.Clean(id)+"/", filepath.Clean(dir)+"/") {
				return KindSystem
			}
		}
		return KindFolder
	case "server":
		if id == serverID {
			return KindSystem
		}
		return KindServer
	case "provider":
		p, err := m.provider(ctx, id)
		if err != nil {
			return KindOther
		}
		if p.Kind == "remote" {
			return KindFriend
		}
		var s3 S3Provider
		if json.Unmarshal([]byte(p.Config), &s3) == nil {
			return KindOnline
		}
	}
	return KindOther
}

// AtHome reports whether a kind of storage is physically with the server
// (quick to restore from, but lost with it in a fire or theft); Away
// whether it's somewhere else. Another server may be either.
func AtHome(kind string) bool {
	switch kind {
	case KindUSB, KindDisk, KindNetwork, KindSystem, KindFolder:
		return true
	}
	return false
}

// Away reports whether a kind of storage is away from the server.
func Away(kind string) bool { return kind == KindOnline || kind == KindFriend }

// StorageFamily groups kinds into "different kinds of storage" for 3-2-1:
// drives and folders at home count as one.
func StorageFamily(kind string) string {
	if kind == KindUSB || kind == KindDisk || kind == KindFolder || kind == KindSystem {
		return "drive"
	}
	return kind
}
