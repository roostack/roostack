package manager

import (
	"context"
	"encoding/json"
	"strings"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
)

// Extra copies of a server's backups (SPEC.md §6.9, 3-2-1): somewhere
// other than its main destination, each with its own repository (created
// by the controller, like the main one) and its own nightly backup.

// BackupCopy is an extra copy, for the UI.
type BackupCopy struct {
	Ref, Title string
	Repository string
}

// BackupCopiesOf lists a server's extra copies.
func (m *Manager) BackupCopiesOf(ctx context.Context, serverID string) []BackupCopy {
	if m.BackupStore == nil {
		return nil
	}
	copies, err := m.BackupStore.BackupCopies(ctx)
	if err != nil {
		return nil
	}
	choices, _ := m.BackupChoices(ctx)
	var out []BackupCopy
	for _, c := range copies {
		if c.ServerID != serverID {
			continue
		}
		v := BackupCopy{Ref: c.Provider, Title: m.destinationTitle(ctx, serverID, c.Provider, c.Repository, choices), Repository: c.Repository}
		out = append(out, v)
	}
	return out
}

// AddBackupCopy adds an extra copy of a server's backups at a choice other
// than its main destination. The repository is created in the background
// (activity.BackupSetupFor); the server then restarts Roostack.
func (m *Manager) AddBackupCopy(ctx context.Context, serverID, ref string) error {
	if m.BackupStore == nil || m.OpenRepo == nil {
		return ErrNotFound
	}
	n := m.node(serverID)
	if n == nil {
		return ErrNotFound
	}
	if n.Settings() == nil {
		return formErrorf("Roostack can't reach %s right now.", n.Identity().Name)
	}
	main, _, ok := m.BackupTargetOf(ctx, serverID)
	if !ok {
		return formErrorf("Choose where %s's backups go first; a copy goes somewhere else.", n.Identity().Name)
	}
	choice, err := m.choiceFor(ctx, serverID, ref)
	if err != nil {
		return err
	}
	switch {
	case choice == nil:
		return formErrorf("Choose where the copy goes.")
	case ref == main.Provider:
		return formErrorf("That's where %s's backups already go. A copy protects you only somewhere else.", n.Identity().Name)
	case strings.HasPrefix(ref, "server:") && strings.TrimPrefix(ref, "server:") == main.StoreHost:
		return formErrorf("That's where %s's backups already go.", n.Identity().Name)
	}
	for _, c := range m.BackupCopiesOf(ctx, serverID) {
		if c.Ref == ref {
			return formErrorf("%s's backups are already copied there.", n.Identity().Name)
		}
	}
	name := n.Identity().Name
	act := m.Activity.Start(activity.BackupSetupFor(serverID), "Adding a copy of "+name+"'s backups")
	go func() {
		err := m.setUpCopy(context.Background(), act, serverID, n, *choice)
		if err != nil {
			m.Log.Error("adding a backup copy", "server", name, "err", err)
		}
		act.Finish(err)
	}()
	return nil
}

func (m *Manager) setUpCopy(ctx context.Context, act *activity.Log, serverID string, n node.Node, choice BackupChoice) (err error) {
	// Recorded early like the main one; forgotten again if setting up fails.
	defer func() {
		if err == nil {
			return
		}
		if undo := m.BackupStore.RemoveBackupCopy(ctx, serverID, choice.Ref); undo != nil {
			m.Log.Error("forgetting a copy that failed", "server", serverID, "err", undo)
		}
		m.Apply()
	}()
	target, dest, err := m.createRepository(ctx, act, serverID, choice, m.BackupStore.SetBackupCopy)
	if err != nil {
		return err
	}
	if err := m.BackupStore.SetBackupCopy(ctx, target); err != nil {
		return err
	}
	act.Step("Telling %s about the copy (it restarts Roostack)", m.nameOf(serverID))
	c := node.BackupCopy{Name: choice.Ref, Title: choice.Title, Repository: dest.Repository, Env: dest.Env, Cloud: dest.Cloud, Key: dest.Key, Drive: dest.Drive}
	if err := n.Settings().SetBackupCopies(ctx, m.copiesFor(ctx, serverID, &c)); err != nil {
		return err
	}
	act.Step("%s's backups are also copied to %s from the next backup", m.nameOf(serverID), choice.Title)
	return nil
}

// copiesFor is what a server is told about its copies: each one it has,
// with added's key (the others keep theirs).
func (m *Manager) copiesFor(ctx context.Context, serverID string, added *node.BackupCopy) []node.BackupCopy {
	var out []node.BackupCopy
	for _, c := range m.BackupCopiesOf(ctx, serverID) {
		if added != nil && c.Ref == added.Name {
			out = append(out, *added)
			continue
		}
		// The server keeps the credentials and key it has for this one.
		out = append(out, node.BackupCopy{Name: c.Ref, Title: c.Title, Repository: c.Repository})
	}
	return out
}

func (m *Manager) copyTarget(ctx context.Context, serverID, ref string) (store.BackupTarget, bool) {
	copies, err := m.BackupStore.BackupCopies(ctx)
	if err != nil {
		return store.BackupTarget{}, false
	}
	for _, c := range copies {
		if c.ServerID == serverID && c.Provider == ref {
			return c, true
		}
	}
	return store.BackupTarget{}, false
}

// PromoteBackupCopy makes a copy a server's backup 1 (its main backup,
// which restores read first), and backup 1 a copy. Both repositories stay.
func (m *Manager) PromoteBackupCopy(ctx context.Context, serverID, ref string) error {
	if m.BackupStore == nil {
		return ErrNotFound
	}
	n := m.node(serverID)
	if n == nil {
		return ErrNotFound
	}
	if n.Settings() == nil {
		return formErrorf("Roostack can't reach %s right now.", n.Identity().Name)
	}
	c, ok := m.copyTarget(ctx, serverID, ref)
	if !ok {
		return ErrNotFound
	}
	main, mainTitle, ok := m.BackupTargetOf(ctx, serverID)
	if !ok {
		return ErrNotFound
	}
	if err := n.Settings().PromoteBackupCopy(ctx, ref, node.BackupCopy{Name: main.Provider, Title: mainTitle}); err != nil {
		return err
	}
	promoted, demoted := c, main
	promoted.ServerID, demoted.ServerID = serverID, serverID
	if err := m.BackupStore.RemoveBackupCopy(ctx, serverID, ref); err != nil {
		return err
	}
	if err := m.BackupStore.SetBackupTarget(ctx, promoted); err != nil {
		return err
	}
	return m.BackupStore.SetBackupCopy(ctx, demoted)
}

// RemoveBackupCopy stops copying a server's backups somewhere. The copies
// made so far stay (the recovery key reads them).
func (m *Manager) RemoveBackupCopy(ctx context.Context, serverID, ref string) error {
	if m.BackupStore == nil {
		return ErrNotFound
	}
	n := m.node(serverID)
	if n == nil {
		return ErrNotFound
	}
	if n.Settings() == nil {
		return formErrorf("Roostack can't reach %s right now.", n.Identity().Name)
	}
	t, ok := m.copyTarget(ctx, serverID, ref)
	if !ok {
		return ErrNotFound
	}
	if err := m.BackupStore.RemoveBackupCopy(ctx, serverID, ref); err != nil {
		return err
	}
	if t.ScopedKey != "" {
		if _, id, _ := strings.Cut(ref, ":"); id != "" {
			if p, err := m.provider(ctx, id); err == nil {
				var s3 S3Provider
				if json.Unmarshal([]byte(p.Config), &s3) == nil {
					m.b2(s3).DeleteKey(ctx, t.ScopedKey)
				}
			}
		}
	}
	if t.StoreHost != "" {
		m.Apply() // the store forgets the server's user
	}
	return n.Settings().SetBackupCopies(ctx, m.copiesFor(ctx, serverID, nil))
}
