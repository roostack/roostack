package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// Changing the recovery key (SPEC.md §6.9): a new key is made and shown,
// and once the Owner confirms saving it, it's added to every repository
// the lab has, checked, and only then made the lab's key; the old key is
// then removed from each repository. A repository that can't be reached
// (a drive unplugged, a friend's lab offline) keeps the change waiting,
// with the old key still working there, and is tried again hourly. At no
// point is there a repository only a key nobody saved opens.
//
// This replaces the key; it doesn't change the repositories' master key,
// so it protects against a leaked key, not against someone who also
// copied the backups (SPEC.md §6.9 says why that's left, for now).

// KeyChanges is changing the recovery key (Manager.KeyChanges).
type KeyChanges struct {
	core keyCore
	// mu makes changes one at a time.
	mu sync.Mutex
}

// keyCore is what key changes use of the Manager: besides the core, the
// activity logs and maintenance jobs, Roostack Cloud backup, every
// repository the recovery key opens, and servers' names in sentences.
type keyCore interface {
	core
	activities() *activity.Registry
	submit(j maintenance.Job)
	cloudBackup() CloudBackup
	keyRepos(ctx context.Context) ([]keyRepo, error)
	serverName(id string) string
	aboutServer(id, line string) string
}

func (m *Manager) activities() *activity.Registry { return m.Activity }
func (m *Manager) cloudBackup() CloudBackup       { return m.CloudBackup }

// submit queues a job with the maintenance coordinator, if there is one.
func (m *Manager) submit(j maintenance.Job) {
	if m.Submit != nil {
		m.Submit(j)
	}
}

// Key change phases.
const (
	KeyConfirm  = "confirm"  // the new key is shown, waiting for "I've saved it"
	KeyAdding   = "adding"   // being added to every repository
	KeyRemoving = "removing" // the new key is the lab's; the old one is being removed
)

// KeyChange is a recovery key change in progress, saved so it survives
// restarts.
type KeyChange struct {
	Phase   string    `json:"phase"`
	Started time.Time `json:"started"`
	Repos   []KeyRepo `json:"repos"`
}

// KeyRepo is one repository's part in the change.
type KeyRepo struct {
	Ref        string `json:"ref"`
	ServerID   string `json:"server"`
	Repository string `json:"repository"`
	// OldKey is the ID of the old key there, to remove.
	OldKey  string `json:"old_key,omitempty"`
	Added   bool   `json:"added,omitempty"`
	Removed bool   `json:"removed,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Done counts the repositories finished in the current phase.
func (k *KeyChange) Done() int {
	n := 0
	for _, r := range k.Repos {
		if k.Phase == KeyRemoving && r.Removed || k.Phase != KeyRemoving && r.Added {
			n++
		}
	}
	return n
}

// Files: the change's state, and the new and old keys next to the
// recovery key (backup.KeyFile), all 0600.
func (x *KeyChanges) keyChangeFile() string {
	return filepath.Join(x.core.labConfig().Paths.StateDir, "backup-key.change.json")
}
func (x *KeyChanges) newKeyFile() string { return backup.KeyFile(x.core.labConfig().Paths) + ".new" }
func (x *KeyChanges) oldKeyFile() string { return backup.KeyFile(x.core.labConfig().Paths) + ".old" }

// KeyChangeStatus returns the change in progress, or nil.
func (x *KeyChanges) KeyChangeStatus() (*KeyChange, error) {
	data, err := os.ReadFile(x.keyChangeFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var k KeyChange
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("%s: %w", x.keyChangeFile(), err)
	}
	return &k, nil
}

func (x *KeyChanges) saveKeyChange(k *KeyChange) error {
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(x.keyChangeFile(), data)
}

// StartKeyChange makes a new recovery key, to be shown and confirmed.
func (x *KeyChanges) StartKeyChange(ctx context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if k, err := x.KeyChangeStatus(); err != nil || k != nil {
		return cmpErr(err, usererr.New("A recovery key change is already under way."))
	}
	if _, err := os.Stat(backup.KeyFile(x.core.labConfig().Paths)); err != nil {
		return usererr.New("The lab has no recovery key yet: Roostack will make one with the first backup.")
	}
	key := backup.NewRecoveryKey()
	if err := fsutil.WritePrivate(x.newKeyFile(), []byte(key+"\n")); err != nil {
		return err
	}
	return x.saveKeyChange(&KeyChange{Phase: KeyConfirm, Started: time.Now()})
}

// NewRecoveryKey is the new key, while it waits to be confirmed.
func (x *KeyChanges) NewRecoveryKey() (string, error) {
	k, err := x.KeyChangeStatus()
	if err != nil || k == nil || k.Phase != KeyConfirm {
		return "", cmpErr(err, ErrNotFound)
	}
	data, err := os.ReadFile(x.newKeyFile())
	return strings.TrimSpace(string(data)), err
}

// CancelKeyChange drops a new key that wasn't confirmed yet.
func (x *KeyChanges) CancelKeyChange() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	k, err := x.KeyChangeStatus()
	if err != nil || k == nil || k.Phase != KeyConfirm {
		return cmpErr(err, usererr.New("Only a change that hasn't started can be cancelled."))
	}
	os.Remove(x.newKeyFile())
	return os.Remove(x.keyChangeFile())
}

// ConfirmKeyChange starts changing the key, once the Owner saved the new
// one: the job runs through the maintenance coordinator, one job at a
// time with backups. The backups to change are listed now, so progress
// shows straight away.
func (x *KeyChanges) ConfirmKeyChange(ctx context.Context) error {
	x.mu.Lock()
	k, err := x.KeyChangeStatus()
	if err == nil && (k == nil || k.Phase != KeyConfirm) {
		err = usererr.New("There's no new recovery key waiting to be confirmed.")
	}
	var repos []keyRepo
	if err == nil {
		repos, err = x.core.keyRepos(ctx)
	}
	if err == nil {
		k.Phase = KeyAdding
		for _, r := range repos {
			k.Repos = append(k.Repos, KeyRepo{Ref: r.Ref, ServerID: r.ServerID, Repository: r.Repository})
		}
		err = x.saveKeyChange(k)
	}
	x.mu.Unlock()
	if err != nil {
		return err
	}
	x.submitKeyChange()
	return nil
}

// ResumeKeyChange queues the job again if a change is waiting for a
// repository (hourly, and at start).
func (x *KeyChanges) ResumeKeyChange() {
	if k, err := x.KeyChangeStatus(); err == nil && k != nil && k.Phase != KeyConfirm {
		x.submitKeyChange()
	}
}

func (x *KeyChanges) submitKeyChange() { x.core.submit(x.KeyChangeJob()) }

// KeyChangeJob adds the new key everywhere, makes it the lab's key, then
// removes the old one everywhere.
func (x *KeyChanges) KeyChangeJob() maintenance.Job {
	return maintenance.Job{Name: "changing the recovery key", Run: func(ctx context.Context, _ *maintenance.Handle) (err error) {
		act := x.core.activities().Start(activity.Backup, "Changing the recovery key")
		defer func() { act.Finish(err) }()
		x.mu.Lock()
		defer x.mu.Unlock()
		k, err := x.KeyChangeStatus()
		if err != nil || k == nil || k.Phase == KeyConfirm {
			return err
		}
		repos, err := x.core.keyRepos(ctx)
		if err != nil {
			return err
		}
		if k.Phase == KeyAdding {
			// Backups added since the change started get the key too.
			for _, r := range repos {
				if !slices.ContainsFunc(k.Repos, func(x KeyRepo) bool { return x.Ref == r.Ref }) {
					k.Repos = append(k.Repos, KeyRepo{Ref: r.Ref, ServerID: r.ServerID, Repository: r.Repository})
				}
			}
			x.addNewKey(ctx, act, k, repos)
			if k.Done() < len(k.Repos) {
				return x.keyWaiting(k)
			}
			if err := x.swapKeys(); err != nil {
				return err
			}
			k.Phase = KeyRemoving
			act.Step("The new recovery key is now the lab's key")
			// Restoring from Roostack Cloud then needs the new key, not
			// the old (SPEC.md §6.9); if Cloud can't be reached, the next
			// check-in tells it.
			if cb := x.core.cloudBackup(); cb != nil {
				if err := cb.RegisterIdentity(ctx); err != nil {
					act.Printf("Roostack couldn't tell Roostack Cloud about the new key yet (%v), so it will try again at its next daily check.", err)
				}
			}
			if err := x.saveKeyChange(k); err != nil {
				return err
			}
		}
		x.removeOldKey(ctx, act, k, repos)
		if k.Done() < len(k.Repos) {
			return x.keyWaiting(k)
		}
		act.Step("The old recovery key no longer opens any backup")
		os.Remove(x.oldKeyFile())
		return os.Remove(x.keyChangeFile())
	}}
}

// keyWaiting saves progress, and says what's left.
func (x *KeyChanges) keyWaiting(k *KeyChange) error {
	if err := x.saveKeyChange(k); err != nil {
		return err
	}
	return fmt.Errorf("%d of %d backups done; the rest are tried again every hour", k.Done(), len(k.Repos))
}

// addNewKey adds the new key to each repository that hasn't got it,
// noting the old key's ID there, and checks the new key opens it.
func (x *KeyChanges) addNewKey(ctx context.Context, act *activity.Log, k *KeyChange, repos []keyRepo) {
	for i := range k.Repos {
		kr := &k.Repos[i]
		if kr.Added {
			continue
		}
		kr.Error = ""
		r, ok := findRepo(repos, kr.Ref)
		if !ok {
			kr.Added, kr.Removed = true, true // the backup was removed since
			continue
		}
		act.Step("Adding the new key to %s's backup", x.core.serverName(kr.ServerID))
		err := func() error {
			old, err := r.open(backup.KeyFile(x.core.labConfig().Paths))
			if err != nil {
				return err
			}
			if kr.OldKey, err = old.CurrentKey(ctx); err != nil {
				return err
			}
			next, err := r.open(x.newKeyFile())
			if err != nil {
				return err
			}
			// Already there (a run interrupted after adding it)?
			if _, err := next.Keys(ctx); err == nil {
				return nil
			}
			key, err := os.ReadFile(x.newKeyFile())
			if err != nil {
				return err
			}
			// AddKey records the new key's ID for the key hint, which the
			// controller then uses with the new key (ASSUMPTIONS O6).
			if _, err := old.AddKey(ctx, strings.TrimSpace(string(key)), "recovery"); err != nil {
				return err
			}
			_, err = next.Keys(ctx)
			return err
		}()
		if err != nil {
			kr.Error = words.FirstLine(strings.TrimSpace(err.Error()))
			act.Printf("%s", x.core.aboutServer(kr.ServerID, kr.Error))
			continue
		}
		kr.Added = true
		x.saveKeyChange(k)
	}
}

// removeOldKey removes the old key from each repository, opening it with
// the new one (now the lab's key).
func (x *KeyChanges) removeOldKey(ctx context.Context, act *activity.Log, k *KeyChange, repos []keyRepo) {
	for i := range k.Repos {
		kr := &k.Repos[i]
		if kr.Removed {
			continue
		}
		kr.Error = ""
		r, ok := findRepo(repos, kr.Ref)
		if !ok {
			kr.Removed = true
			continue
		}
		act.Step("Removing the old key from %s's backup", x.core.serverName(kr.ServerID))
		err := func() error {
			repo, err := r.open(backup.KeyFile(x.core.labConfig().Paths))
			if err != nil {
				return err
			}
			keys, err := repo.Keys(ctx)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(keys, func(x backup.Key) bool { return x.ID == kr.OldKey }) {
				return nil // already gone
			}
			return repo.RemoveKey(ctx, kr.OldKey)
		}()
		if err != nil {
			kr.Error = words.FirstLine(strings.TrimSpace(err.Error()))
			act.Printf("%s", x.core.aboutServer(kr.ServerID, kr.Error))
			continue
		}
		kr.Removed = true
		x.saveKeyChange(k)
	}
}

// swapKeys makes the new key the lab's, keeping the old one until it's
// removed everywhere. Safe to run again after an interruption.
func (x *KeyChanges) swapKeys() error {
	key := backup.KeyFile(x.core.labConfig().Paths)
	if _, err := os.Stat(x.oldKeyFile()); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(key, x.oldKeyFile()); err != nil {
			return err
		}
	}
	if _, err := os.Stat(x.newKeyFile()); err == nil {
		return os.Rename(x.newKeyFile(), key)
	}
	return nil
}

// cmpErr returns err if set, else fallback.
func cmpErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
