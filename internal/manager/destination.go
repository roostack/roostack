package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/b2"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/store"
)

// Where a server's backups go, and what its own credential can do there
// (SPEC.md §10.10). A reference ("provider:3", "server:n-…", "drive:…")
// is resolved once into a destination, so each place that cares switches
// on a kind rather than on the reference's prefix.

// destKind is a kind of backup destination.
type destKind int

const (
	destUnknown destKind = iota
	destCloud            // Roostack Cloud backup
	destServer           // a server's backup store (rest-server), in this lab
	destDrive            // a drive on the server
	destFolder           // a folder on the server ("folder:" or "local:")
	destSetup            // set up by the server's own setup wizard
	destRemote           // a friend's lab's storage
	destS3               // S3-compatible online storage
)

// destination is a resolved backup reference.
type destination struct {
	kind destKind
	// id follows the reference's colon: a server, drive, folder or
	// provider.
	id string
	// provider and its configuration, for destRemote and destS3.
	provider store.BackupProvider
	remote   RemoteStorage
	s3       S3Provider
}

// destinationOf resolves a backup reference.
func (m *Manager) destinationOf(ctx context.Context, ref string) (destination, error) {
	kind, id, _ := strings.Cut(ref, ":")
	d := destination{id: id}
	switch {
	case kind == CloudChoice:
		d.kind = destCloud
	case kind == "server":
		d.kind = destServer
	case kind == "drive":
		d.kind = destDrive
	case kind == "folder" || kind == "local":
		d.kind = destFolder
	case strings.HasPrefix(ref, "setup"):
		d.kind = destSetup
	case kind == "provider":
		p, err := m.provider(ctx, id)
		if err != nil {
			return d, err
		}
		d.provider = p
		if p.Kind == "remote" {
			d.kind = destRemote
			return d, json.Unmarshal([]byte(p.Config), &d.remote)
		}
		d.kind = destS3
		return d, json.Unmarshal([]byte(p.Config), &d.s3)
	default:
		return d, fmt.Errorf("unknown backup destination %q", ref)
	}
	return d, nil
}

// isB2 reports whether the destination is Backblaze B2.
func (d destination) isB2() bool { return d.kind == destS3 && b2.IsB2(d.s3.Endpoint) }

// Access is what a server's own backup credential can do to its backups at
// a destination (SPEC.md §10.10): whether they survive the server being
// taken over (by ransomware, say), and what it keeps if it leaves the lab.
type Access struct {
	// B2: the destination is Backblaze B2, where a key that can make keys
	// gives each server one that can't erase (SPEC.md §10.10).
	B2 bool
	// CanDelete: the server can delete or overwrite its backups there:
	// they're on its own disks, or it holds the account's key.
	CanDelete bool
	// HidesOnly: deleting there only hides files (Backblaze, with a key
	// Roostack made; or a bucket that keeps old versions, with a key that
	// can't erase them: S3Check). KeepsHidden is how many days the bucket
	// keeps hidden files before erasing them: 0 for good, -1 not known.
	HidesOnly   bool
	KeepsHidden int
	// Versioned: the storage keeps old versions of what's overwritten or
	// deleted (Backblaze always), so the controller can roll the server's
	// folder back to an earlier time (RollBack).
	Versioned bool
}

// MinKeepHidden is how many days a bucket must keep hidden files for a
// backup there to count as safe from ransomware: time to notice, and to
// bring them back with the account's key.
const MinKeepHidden = 30

// Safe reports whether the backups would survive the server being taken
// over: it can't delete them, or what it hides is kept long enough.
func (a Access) Safe() bool {
	if a.CanDelete {
		return false
	}
	return !a.HidesOnly || a.KeepsHidden == 0 || a.KeepsHidden >= MinKeepHidden
}

// BackupAccess says what a server's credential can do at one of its
// backup destinations (its main backup or a copy).
func (m *Manager) BackupAccess(ctx context.Context, serverID, ref string) Access {
	d, err := m.destinationOf(ctx, ref)
	if err != nil {
		return Access{CanDelete: true}
	}
	t, _ := m.targetAt(ctx, serverID, ref)
	return m.access(ctx, serverID, d, t)
}

// access is BackupAccess for a resolved destination and its record.
func (m *Manager) access(ctx context.Context, serverID string, d destination, t store.BackupTarget) Access {
	switch d.kind {
	case destCloud:
		// Keys for the server's own folder, which can only add (Cloud's
		// DESIGN §6(b)).
		return Access{}
	case destServer:
		// rest-server with --append-only and --private-repos; but on the
		// server's own disk it can delete the files directly.
		return Access{CanDelete: d.id == serverID}
	case destRemote:
		// The friend's lab's rest-server is append-only too.
		return Access{}
	case destS3:
		if d.isB2() && t.ScopedKey != "" {
			labID, _ := m.labID(ctx)
			return Access{B2: true, HidesOnly: true, Versioned: true,
				KeepsHidden: m.keepsHidden(ctx, d.s3, backup.ServerFolder(d.s3.Prefix, labID, serverID)+"/")}
		}
		if d.isB2() {
			return Access{B2: true, CanDelete: true, Versioned: true}
		}
		// Other providers: every server has the account's key.
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		c := m.s3CheckOf(ctx, d.s3)
		if c.HidesOnly() {
			return Access{HidesOnly: true, KeepsHidden: c.KeepsOld, Versioned: true}
		}
		return Access{CanDelete: true, Versioned: c.Err == "" && c.Versioned}
	}
	// Drives, folders, and what the server set up itself.
	return Access{CanDelete: true}
}

// targetAt is a server's main backup or copy at a reference.
func (m *Manager) targetAt(ctx context.Context, serverID, ref string) (store.BackupTarget, bool) {
	if m.BackupStore == nil {
		return store.BackupTarget{}, false
	}
	targets, _ := m.BackupStore.BackupTargets(ctx)
	copies, _ := m.BackupStore.BackupCopies(ctx)
	for _, t := range append(targets, copies...) {
		if t.ServerID == serverID && t.Provider == ref {
			return t, true
		}
	}
	return store.BackupTarget{}, false
}

// hiddenCache remembers Backblaze buckets' lifecycle rules for a while, so
// the Backups page doesn't ask Backblaze each time.
var hiddenCache = struct {
	sync.Mutex
	days map[string]hiddenDays
}{days: map[string]hiddenDays{}}

type hiddenDays struct {
	days    int
	expires time.Time
}

// keepsHidden is how long a Backblaze bucket keeps hidden files in a
// folder (b2.Client.KeepsHidden), -1 if Backblaze couldn't be asked.
func (m *Manager) keepsHidden(ctx context.Context, s3 S3Provider, folder string) int {
	key := strings.Join([]string{s3.Endpoint, s3.AccessKey, s3.Bucket, folder}, " ")
	hiddenCache.Lock()
	c, ok := hiddenCache.days[key]
	hiddenCache.Unlock()
	if ok && time.Now().Before(c.expires) {
		return c.days
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	days, err := m.b2(s3).KeepsHidden(ctx, s3.Bucket, folder)
	c = hiddenDays{days: days, expires: time.Now().Add(time.Hour)}
	if err != nil {
		m.Log.Warn("reading a Backblaze bucket's lifecycle rules", "bucket", s3.Bucket, "err", err)
		c = hiddenDays{days: -1, expires: time.Now().Add(5 * time.Minute)}
	}
	hiddenCache.Lock()
	hiddenCache.days[key] = c
	hiddenCache.Unlock()
	return c.days
}

// SharedCredential is a credential a server holds for its backups that
// others use too, so it keeps it after it leaves the lab (SPEC.md §10.10).
type SharedCredential struct {
	// What it is, for a sentence: "the account key for Wasabi".
	What string
	// Anywhere: it works from anywhere on the internet. Otherwise it's
	// only reached through the lab's network, which a removed server
	// leaves.
	Anywhere bool
}

// SharedCredentials lists the shared credentials a server holds for its
// backups and copies: an online storage account's key (when Roostack
// couldn't make it one of its own), a friend's lab's storage password.
func (m *Manager) SharedCredentials(ctx context.Context, serverID string) []SharedCredential {
	if m.BackupStore == nil {
		return nil
	}
	targets, _ := m.BackupStore.BackupTargets(ctx)
	copies, _ := m.BackupStore.BackupCopies(ctx)
	var out []SharedCredential
	for _, t := range append(targets, copies...) {
		if t.ServerID != serverID {
			continue
		}
		d, err := m.destinationOf(ctx, t.Provider)
		if err != nil {
			continue
		}
		var c SharedCredential
		switch {
		case d.kind == destRemote:
			c = SharedCredential{What: "the password for " + d.remote.HostName + "'s storage"}
		case d.kind == destS3 && t.ScopedKey == "":
			c = SharedCredential{What: "the account key for " + d.provider.Name, Anywhere: true}
		default:
			continue
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}
