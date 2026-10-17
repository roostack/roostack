package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
)

// A new key for an online storage account (SPEC.md §10.10): after a
// server that held the account's key leaves the lab, or to give Backblaze
// a key that can make keys, so each server gets one of its own.

// accountUse is a server's main backup or copy at an account.
type accountUse struct {
	store.BackupTarget
	Main bool
}

// ChangeS3Key gives Roostack a new key for an online storage account (ref
// is "provider:<id>"). The key is checked first: it must reach the bucket
// and the backups already there, which the controller prunes with it.
// Then, in the background (activity.BackupKeyFor), each server backing up
// there gets new credentials: on Backblaze, when the key can make keys, a
// key of its own limited to its folder (a server that has one keeps it);
// otherwise the new key itself.
func (m *Manager) ChangeS3Key(ctx context.Context, ref, accessKey, secretKey string) error {
	if m.BackupStore == nil || m.OpenRepo == nil {
		return ErrNotFound
	}
	d, err := m.destinationOf(ctx, ref)
	if err != nil || d.kind != destS3 {
		return ErrNotFound
	}
	s3 := d.s3
	s3.AccessKey, s3.SecretKey = accessKey, secretKey
	s3 = s3.Clean()
	if s3.AccessKey == "" || s3.SecretKey == "" {
		return formErrorf("Enter the new key's ID and the key itself.")
	}
	using, err := m.accountUses(ctx, ref)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	scoped := false
	if d.isB2() {
		if scoped, err = m.b2(s3).CanMakeKeys(cctx, s3.Bucket); err != nil {
			return formErrorf("Backblaze didn't accept this key: %s", firstLine(err.Error()))
		}
	}
	if len(using) > 0 {
		if _, err := m.OpenRepo(using[0].Repository, s3.Env()).Keys(cctx); err != nil {
			return formErrorf("Roostack couldn't reach the backups in %s with this key: %s", s3.Bucket, firstLine(err.Error()))
		}
	}
	p := d.provider
	cfg, _ := json.Marshal(s3)
	p.Config = string(cfg)
	if err := m.BackupStore.UpdateBackupProvider(ctx, p); err != nil {
		return err
	}
	act := m.Activity.Start(activity.BackupKeyFor(d.id), "Changing the key for "+p.Name)
	act.Step("Checking the new key")
	if d.isB2() && !scoped {
		act.Printf("This key can't make other keys, so every server backing up to %s uses it, and could delete its backups there.", p.Name)
	}
	go func() {
		ctx := context.Background()
		if !d.isB2() {
			// The new key may be allowed more, or less (SPEC.md §10.10).
			act.Step("Checking whether %s keeps what's deleted", p.Name)
			sayS3Check(act, m.checkS3(ctx, s3))
		}
		act.Finish(m.handOutS3Key(ctx, act, p.Name, scoped, s3, using))
	}()
	return nil
}

// accountUses lists the main backups and copies at an account, the
// controller's last: giving it new credentials restarts Roostack.
func (m *Manager) accountUses(ctx context.Context, ref string) ([]accountUse, error) {
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		return nil, err
	}
	copies, err := m.BackupStore.BackupCopies(ctx)
	if err != nil {
		return nil, err
	}
	var out []accountUse
	for _, t := range targets {
		if t.Provider == ref {
			out = append(out, accountUse{t, true})
		}
	}
	for _, t := range copies {
		if t.Provider == ref {
			out = append(out, accountUse{t, false})
		}
	}
	slices.SortStableFunc(out, func(a, b accountUse) int {
		switch {
		case a.ServerID == b.ServerID:
			return 0
		case a.ServerID == m.Lab.NodeID:
			return 1
		case b.ServerID == m.Lab.NodeID:
			return -1
		}
		return 0
	})
	return out, nil
}

// handOutS3Key gives each server backing up to an account its new
// credentials (ChangeS3Key). scoped: make each a Backblaze key of its own.
// A server that can't be reached keeps the old key, and the error says so.
func (m *Manager) handOutS3Key(ctx context.Context, act *activity.Log, name string, scoped bool, s3 S3Provider, using []accountUse) error {
	labID, err := m.labID(ctx)
	if err != nil {
		return err
	}
	var missed []string
	for _, u := range using {
		server := m.nameOf(u.ServerID)
		if u.ScopedKey != "" {
			act.Printf("%s keeps the key of its own it has.", server)
			continue
		}
		n := m.node(u.ServerID)
		if n == nil || n.Settings() == nil {
			act.Printf("Roostack couldn't reach %s: it still has the old key.", server)
			missed = append(missed, server)
			continue
		}
		t := u.BackupTarget
		dest := node.BackupDestination{Repository: t.Repository, Env: s3.Env(), PrunedByController: true}
		if scoped {
			m.scopedB2Key(ctx, act, u.ServerID, labID, s3, &t, &dest)
		}
		act.Step("Giving %s its new key (it restarts Roostack)", server)
		if u.Main {
			err = n.Settings().SetBackupDestination(ctx, dest)
		} else {
			c := node.BackupCopy{Name: t.Provider, Repository: t.Repository, Env: dest.Env}
			err = n.Settings().SetBackupCopies(ctx, m.copiesFor(ctx, u.ServerID, &c))
		}
		if err != nil {
			act.Printf("Roostack couldn't reach %s (%s): it still has the old key.", server, firstLine(err.Error()))
			missed = append(missed, server)
			if t.ScopedKey != "" {
				if err := m.b2(s3).DeleteKey(ctx, t.ScopedKey); err != nil {
					m.Log.Warn("deleting a Backblaze key a server didn't get", "server", u.ServerID, "err", err)
				}
			}
			continue
		}
		if t.ScopedKey == "" {
			continue
		}
		// Recorded, so it's deleted when the server leaves.
		record := m.BackupStore.SetBackupCopy
		if u.Main {
			record = m.BackupStore.SetBackupTarget
		}
		if err := record(ctx, t); err != nil {
			m.Log.Error("recording a server's Backblaze key", "server", u.ServerID, "err", err)
		}
	}
	switch {
	case len(missed) == 1:
		return fmt.Errorf("Roostack couldn't reach %s, which still has the old key: give the key again once it's back, before you delete the old one", missed[0])
	case len(missed) > 1:
		return fmt.Errorf("Roostack couldn't reach %s, which still have the old key: give the key again once they're back, before you delete the old one",
			joinNames(missed))
	case len(using) == 0:
		act.Step("No server backs up to %s yet. You can delete the old key at %s now.", name, name)
	default:
		act.Step("Every server backing up to %s has the new key. You can delete the old one at %s now.", name, name)
	}
	return nil
}

// joinNames lists names for a sentence: "a, b and c".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
