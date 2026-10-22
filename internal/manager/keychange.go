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
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
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
func (m *Manager) keyChangeFile() string {
	return filepath.Join(m.Lab.Paths.StateDir, "backup-key.change.json")
}
func (m *Manager) newKeyFile() string { return backup.KeyFile(m.Lab.Paths) + ".new" }
func (m *Manager) oldKeyFile() string { return backup.KeyFile(m.Lab.Paths) + ".old" }

// KeyChangeStatus returns the change in progress, or nil.
func (m *Manager) KeyChangeStatus() (*KeyChange, error) {
	data, err := os.ReadFile(m.keyChangeFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var k KeyChange
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("%s: %w", m.keyChangeFile(), err)
	}
	return &k, nil
}

func (m *Manager) saveKeyChange(k *KeyChange) error {
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(m.keyChangeFile(), data)
}

// StartKeyChange makes a new recovery key, to be shown and confirmed.
func (m *Manager) StartKeyChange(ctx context.Context) error {
	m.keyMu.Lock()
	defer m.keyMu.Unlock()
	if k, err := m.KeyChangeStatus(); err != nil || k != nil {
		return cmpErr(err, usererr.New("A recovery key change is already under way."))
	}
	if _, err := os.Stat(backup.KeyFile(m.Lab.Paths)); err != nil {
		return usererr.New("The lab has no recovery key yet: it's made with the first backup.")
	}
	key := backup.NewRecoveryKey()
	if err := fsutil.WritePrivate(m.newKeyFile(), []byte(key+"\n")); err != nil {
		return err
	}
	return m.saveKeyChange(&KeyChange{Phase: KeyConfirm, Started: time.Now()})
}

// NewRecoveryKey is the new key, while it waits to be confirmed.
func (m *Manager) NewRecoveryKey() (string, error) {
	k, err := m.KeyChangeStatus()
	if err != nil || k == nil || k.Phase != KeyConfirm {
		return "", cmpErr(err, ErrNotFound)
	}
	data, err := os.ReadFile(m.newKeyFile())
	return strings.TrimSpace(string(data)), err
}

// CancelKeyChange drops a new key that wasn't confirmed yet.
func (m *Manager) CancelKeyChange() error {
	m.keyMu.Lock()
	defer m.keyMu.Unlock()
	k, err := m.KeyChangeStatus()
	if err != nil || k == nil || k.Phase != KeyConfirm {
		return cmpErr(err, usererr.New("Only a change that hasn't started can be cancelled."))
	}
	os.Remove(m.newKeyFile())
	return os.Remove(m.keyChangeFile())
}

// ConfirmKeyChange starts changing the key, once the Owner saved the new
// one: the job runs through the maintenance coordinator, one job at a
// time with backups. The backups to change are listed now, so progress
// shows straight away.
func (m *Manager) ConfirmKeyChange(ctx context.Context) error {
	m.keyMu.Lock()
	k, err := m.KeyChangeStatus()
	if err == nil && (k == nil || k.Phase != KeyConfirm) {
		err = usererr.New("There's no new recovery key waiting to be confirmed.")
	}
	var repos []keyRepo
	if err == nil {
		repos, err = m.keyRepos(ctx)
	}
	if err == nil {
		k.Phase = KeyAdding
		for _, r := range repos {
			k.Repos = append(k.Repos, KeyRepo{Ref: r.Ref, ServerID: r.ServerID, Repository: r.Repository})
		}
		err = m.saveKeyChange(k)
	}
	m.keyMu.Unlock()
	if err != nil {
		return err
	}
	m.submitKeyChange()
	return nil
}

// ResumeKeyChange queues the job again if a change is waiting for a
// repository (hourly, and at start).
func (m *Manager) ResumeKeyChange() {
	if k, err := m.KeyChangeStatus(); err == nil && k != nil && k.Phase != KeyConfirm {
		m.submitKeyChange()
	}
}

func (m *Manager) submitKeyChange() {
	if m.Submit != nil {
		m.Submit(m.KeyChangeJob())
	}
}

// KeyChangeJob adds the new key everywhere, makes it the lab's key, then
// removes the old one everywhere.
func (m *Manager) KeyChangeJob() maintenance.Job {
	return maintenance.Job{Name: "changing the recovery key", Run: func(ctx context.Context, _ *maintenance.Handle) (err error) {
		act := m.Activity.Start(activity.Backup, "Changing the recovery key")
		defer func() { act.Finish(err) }()
		m.keyMu.Lock()
		defer m.keyMu.Unlock()
		k, err := m.KeyChangeStatus()
		if err != nil || k == nil || k.Phase == KeyConfirm {
			return err
		}
		repos, err := m.keyRepos(ctx)
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
			m.addNewKey(ctx, act, k, repos)
			if k.Done() < len(k.Repos) {
				return m.keyWaiting(k)
			}
			if err := m.swapKeys(); err != nil {
				return err
			}
			k.Phase = KeyRemoving
			act.Step("The new recovery key is now the lab's key")
			// Restoring from Roostack Cloud then needs the new key, not
			// the old (SPEC.md §6.9); if Cloud can't be reached, the next
			// check-in tells it.
			if m.CloudBackup != nil {
				if err := m.CloudBackup.RegisterIdentity(ctx); err != nil {
					act.Printf("Roostack Cloud couldn't be told about the new key yet (%v); Roostack tries again at its next daily check.", err)
				}
			}
			if err := m.saveKeyChange(k); err != nil {
				return err
			}
		}
		m.removeOldKey(ctx, act, k, repos)
		if k.Done() < len(k.Repos) {
			return m.keyWaiting(k)
		}
		act.Step("The old recovery key no longer opens any backup")
		os.Remove(m.oldKeyFile())
		return os.Remove(m.keyChangeFile())
	}}
}

// keyWaiting saves progress, and says what's left.
func (m *Manager) keyWaiting(k *KeyChange) error {
	if err := m.saveKeyChange(k); err != nil {
		return err
	}
	return fmt.Errorf("%d of %d backups done; the rest are tried again every hour", k.Done(), len(k.Repos))
}

// addNewKey adds the new key to each repository that hasn't got it,
// noting the old key's ID there, and checks the new key opens it.
func (m *Manager) addNewKey(ctx context.Context, act *activity.Log, k *KeyChange, repos []keyRepo) {
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
		act.Step("Adding the new key to %s's backup", m.serverName(kr.ServerID))
		err := func() error {
			old, err := r.open(backup.KeyFile(m.Lab.Paths))
			if err != nil {
				return err
			}
			if kr.OldKey, err = old.CurrentKey(ctx); err != nil {
				return err
			}
			next, err := r.open(m.newKeyFile())
			if err != nil {
				return err
			}
			// Already there (a run interrupted after adding it)?
			if _, err := next.Keys(ctx); err == nil {
				return nil
			}
			key, err := os.ReadFile(m.newKeyFile())
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
			kr.Error = firstLine(err.Error())
			act.Printf("%s", m.aboutServer(kr.ServerID, kr.Error))
			continue
		}
		kr.Added = true
		m.saveKeyChange(k)
	}
}

// removeOldKey removes the old key from each repository, opening it with
// the new one (now the lab's key).
func (m *Manager) removeOldKey(ctx context.Context, act *activity.Log, k *KeyChange, repos []keyRepo) {
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
		act.Step("Removing the old key from %s's backup", m.serverName(kr.ServerID))
		err := func() error {
			repo, err := r.open(backup.KeyFile(m.Lab.Paths))
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
			kr.Error = firstLine(err.Error())
			act.Printf("%s", m.aboutServer(kr.ServerID, kr.Error))
			continue
		}
		kr.Removed = true
		m.saveKeyChange(k)
	}
}

// swapKeys makes the new key the lab's, keeping the old one until it's
// removed everywhere. Safe to run again after an interruption.
func (m *Manager) swapKeys() error {
	key := backup.KeyFile(m.Lab.Paths)
	if _, err := os.Stat(m.oldKeyFile()); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(key, m.oldKeyFile()); err != nil {
			return err
		}
	}
	if _, err := os.Stat(m.newKeyFile()); err == nil {
		return os.Rename(m.newKeyFile(), key)
	}
	return nil
}

// keyRepo is a repository the recovery key opens, and how to open it
// with a given key file.
type keyRepo struct {
	Ref, ServerID, Repository string
	open                      func(keyFile string) (*backup.Repo, error)
}

func findRepo(repos []keyRepo, ref string) (keyRepo, bool) {
	i := slices.IndexFunc(repos, func(r keyRepo) bool { return r.Ref == ref })
	if i < 0 {
		return keyRepo{}, false
	}
	return repos[i], true
}

// keyRepos lists every repository the recovery key opens: each server's
// backup and extra copies, through the controller's admin access (as for
// pruning), and this server's own main backup.
func (m *Manager) keyRepos(ctx context.Context) ([]keyRepo, error) {
	if m.BackupStore == nil {
		return nil, nil
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		return nil, err
	}
	copies, err := m.BackupStore.BackupCopies(ctx)
	if err != nil {
		return nil, err
	}
	var out []keyRepo
	add := func(ref string, t store.BackupTarget) {
		out = append(out, keyRepo{Ref: ref, ServerID: t.ServerID, Repository: t.Repository,
			open: func(keyFile string) (*backup.Repo, error) {
				var r *backup.Repo
				if strings.HasPrefix(t.Provider, "setup") {
					// Set up by this server's wizard: its own main backup.
					if t.ServerID != m.Lab.NodeID {
						return nil, fmt.Errorf("%s's backup was set up on that server", m.nameOf(t.ServerID))
					}
					r = m.OpenRepo(t.Repository, m.Lab.Backup.Env)
				} else {
					var err error
					if r, err = m.adminRepo(ctx, t); err != nil {
						return nil, err
					}
				}
				withKey := *r
				withKey.PasswordFile = keyFile
				return &withKey, nil
			}})
	}
	for _, t := range targets {
		add("backup:"+t.ServerID, t)
	}
	for _, t := range copies {
		add("copy:"+t.ServerID+":"+t.Provider, t)
	}
	return out, nil
}

// cmpErr returns err if set, else fallback.
func cmpErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
