package manager

import (
	"context"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/store"
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
	KindCloud   = "cloud"   // Roostack Cloud backup
	KindOther   = "other"
)

// BackupKind says what kind of storage a destination is.
func (m *Manager) BackupKind(ctx context.Context, serverID, ref string) string {
	d, err := m.destinationOf(ctx, ref)
	if err != nil {
		return KindOther
	}
	id := d.id
	switch d.kind {
	case destDrive:
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
	case destFolder:
		// A folder in Roostack's own data (the default on the server's
		// disk) is the server's disk; every server has the same layout.
		for _, dir := range []string{m.Lab.Paths.BackupStoreDir(), m.Lab.Paths.StateDir} {
			if dir != "" && dir != "." && fsutil.Within(id, dir) {
				return KindSystem
			}
		}
		return KindFolder
	case destServer:
		if id == serverID {
			return KindSystem
		}
		return KindServer
	case destCloud:
		return KindCloud
	case destRemote:
		return KindFriend
	case destS3:
		return KindOnline
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
func Away(kind string) bool { return kind == KindOnline || kind == KindFriend || kind == KindCloud }

// StorageFamily groups kinds into "different kinds of storage" for 3-2-1:
// disks count as one, whether in this server, plugged into it, or in
// another server of the lab.
func StorageFamily(kind string) string {
	if kind == KindUSB || kind == KindDisk || kind == KindFolder || kind == KindSystem || kind == KindServer {
		return "drive"
	}
	return kind
}

// PlanCopies is how many copies the 3-2-1 rule counts: the data on the
// server itself and two backups. The plan numbers its backups up to it.
const PlanCopies = 3

// backupStaleAfter is how long without a finished backup before Roostack
// says backups aren't happening.
const backupStaleAfter = 48 * time.Hour

// BackupStale reports whether a backup that last finished at t is too old.
func BackupStale(t time.Time) bool { return time.Since(t) > backupStaleAfter }

// PlanBackup is one of a server's backups, as the 3-2-1 checks see it.
type PlanBackup struct {
	Title string
	// Kind is what kind of storage it's on (Kind*); AtHome and Away are
	// where it is (AtHome and Away for most kinds; another server is
	// where the Owner says it is, ServerPlace).
	Kind         string
	AtHome, Away bool
	Access       Access
	// LastOK is its last backup that finished, if any.
	LastOK *store.BackupRun
}

// PlanCheck is how a server's backups stand against the 3-2-1 rule: three
// copies (the data on the server and two backups), two kinds of storage
// (the data is on a disk), one away from home; and Safe, one the server
// can't delete, so ransomware on it can't take every backup (SPEC.md
// §6.9). BackedUp: the main backup finished lately.
type PlanCheck struct {
	Copies, TwoKinds, Away, Safe, BackedUp bool
	// Missing is what the plan lacks first, and Suggest where to find it
	// ("" when the 3-2-1 rule is met).
	Missing, Suggest string
	// Hint suggests a change to the plan (an at-home backup as the main
	// one), or is "".
	Hint string
}

// PlanChecks checks a server's backups, the main one first. internet: the
// server is the lab on the internet (a cloud server, SPEC.md §6.12), so
// "away" means away from it and its provider, and it has no home.
func PlanChecks(backups []PlanBackup, internet bool) PlanCheck {
	var c PlanCheck
	c.Copies = len(backups) >= PlanCopies-1
	for _, b := range backups {
		c.TwoKinds = c.TwoKinds || StorageFamily(b.Kind) != StorageFamily(KindSystem)
		c.Away = c.Away || b.Away
		c.Safe = c.Safe || b.Access.Safe()
	}
	if len(backups) > 0 && backups[0].LastOK != nil && !BackupStale(backups[0].LastOK.Finished) {
		c.BackedUp = true
	}
	// The main backup is best at home: restores from it are quick. (A cloud
	// server has no home to suggest.)
	if len(backups) > 1 && !backups[0].AtHome && !internet {
		for _, b := range backups[1:] {
			if b.AtHome {
				c.Hint = "Restores use the main backup first, so it's best at home, where restoring is quick: consider making " +
					b.Title + " the main backup."
				break
			}
		}
	}
	c.Missing, c.Suggest = c.missing(backups, internet)
	return c
}

// missing is what the plan lacks first, with a suggestion.
func (c PlanCheck) missing(backups []PlanBackup, internet bool) (title, suggest string) {
	switch {
	case !c.Away && internet:
		return "A backup away from this server", "Suggested: online storage with another company, or space at your own or a friend's lab. " +
			"It survives losing this server, or your account with its provider."
	case !c.Away:
		return "A backup away from home", "Suggested: online storage, or space at a friend's lab. It survives a fire or theft at home."
	case !c.TwoKinds:
		return "A different kind of storage", "Suggested: online storage, or a friend's lab: not a disk like the one your data is on."
	case !c.Safe:
		// Backblaze already, with the account's key: a better key is the
		// quickest way there.
		for _, b := range backups {
			if b.Access.B2 && b.Access.CanDelete {
				return "A backup safe from ransomware", "Suggested: give Roostack a Backblaze key that can make keys (see " + b.Title +
					"), or use another server in your lab, a friend's lab or Roostack Cloud. This server can add backups there but not delete them."
			}
		}
		return "A backup safe from ransomware", "Suggested: another server in your lab, a friend's lab, Roostack Cloud or Backblaze B2. " +
			"This server can add backups there but not delete them."
	}
	return "", ""
}
