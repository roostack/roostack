package manager

import (
	"cmp"
	"context"
	"encoding/json"
	"strings"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
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
		return usererr.New("Roostack can't reach %s right now.", n.Identity().Name)
	}
	main, _, ok := m.BackupTargetOf(ctx, serverID)
	if !ok {
		return usererr.New("Choose where %s backups go first; a copy goes somewhere else.", m.backupsOf(serverID))
	}
	choice, err := m.choiceFor(ctx, serverID, ref)
	if err != nil {
		return err
	}
	switch {
	case choice == nil:
		return usererr.New("Choose where the copy goes.")
	case ref == main.Provider:
		return usererr.New("That's where %s backups already go. A copy protects you only somewhere else.", m.backupsOf(serverID))
	case strings.HasPrefix(ref, "server:") && strings.TrimPrefix(ref, "server:") == main.StoreHost:
		return usererr.New("That's where %s backups already go.", m.backupsOf(serverID))
	}
	for _, c := range m.BackupCopiesOf(ctx, serverID) {
		if c.Ref == ref {
			return usererr.New("%s backups are already copied there.", capital(m.backupsOf(serverID)))
		}
	}
	name := n.Identity().Name
	act := m.Activity.Start(activity.BackupSetupFor(serverID), "Adding a copy of "+m.backupsOf(serverID)+" backups")
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
	if m.alone() {
		act.Step("Saving the copy (Roostack restarts to use it)")
	} else {
		act.Step("Telling %s about the copy (it restarts Roostack)", m.nameOf(serverID))
	}
	c := node.BackupCopy{Name: choice.Ref, Title: choice.Title, Repository: dest.Repository, Env: dest.Env, Cloud: dest.Cloud, Key: dest.Key, KeyHint: dest.KeyHint, Drive: dest.Drive}
	if err := n.Settings().SetBackupCopies(ctx, m.copiesFor(ctx, serverID, &c)); err != nil {
		return err
	}
	act.Step("%s backups are also copied to %s from the next backup", capital(m.backupsOf(serverID)), inSentence(choice.Title))
	return nil
}

// backupsOf is whose backups they are, before "backups": a server's
// ("nas's"), or "the" in a lab of one.
func (m *Manager) backupsOf(serverID string) string {
	if serverID == m.Lab.NodeID && m.alone() {
		return "the"
	}
	return m.nameOf(serverID) + "'s"
}

// capital starts a sentence with s: "this server" and "the" take a
// capital, a server's own name stays as it is ("nas", not "Nas").
func capital(s string) string {
	if s == "the" || strings.HasPrefix(s, "the ") || strings.HasPrefix(s, "this ") {
		return strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}

// copiesFor is what a server is told about its copies: each one it has,
// with added's key or credentials (the others keep theirs). added keeps
// its title when it comes without one.
func (m *Manager) copiesFor(ctx context.Context, serverID string, added *node.BackupCopy) []node.BackupCopy {
	var out []node.BackupCopy
	for _, c := range m.BackupCopiesOf(ctx, serverID) {
		if added != nil && c.Ref == added.Name {
			a := *added
			a.Title = cmp.Or(a.Title, c.Title)
			out = append(out, a)
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
		return usererr.New("Roostack can't reach %s right now.", n.Identity().Name)
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
		return usererr.New("Roostack can't reach %s right now.", n.Identity().Name)
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
