package manager

import (
	"context"
	"slices"
	"time"

	"roostack.cloud/roostack/cloudapi"
)

// CloudBackups are the lab's servers that back up to Roostack Cloud (their
// main backup, or a copy), each with when its last backup there finished,
// for the lab's report to Cloud (SPEC.md §6.6): the lab's page in Cloud
// shows them. Server is the server's ID, as its folder in Cloud is named
// (StorageRequest.Server). A server that can't be asked is listed with
// no time.
func (m *Manager) CloudBackups(ctx context.Context) ([]cloudapi.ServerBackup, error) {
	out := []cloudapi.ServerBackup{}
	if m.BackupStore == nil {
		return out, nil
	}
	targets, err := m.BackupStore.BackupTargets(ctx)
	if err != nil {
		return nil, err
	}
	copies, err := m.BackupStore.BackupCopies(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range m.allNodes() {
		id := n.Identity().ID
		main := false
		for _, t := range targets {
			main = main || t.ServerID == id && t.Provider == CloudChoice
		}
		var repos []string // its copies in Cloud
		for _, c := range copies {
			if c.ServerID == id && c.Provider == CloudChoice {
				repos = append(repos, c.Repository)
			}
		}
		if !main && len(repos) == 0 {
			continue
		}
		b := cloudapi.ServerBackup{Server: id, Name: n.Identity().Name}
		if bk := n.Backups(); bk != nil {
			nctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			st, err := bk.Status(nctx)
			cancel()
			if err != nil {
				m.Log.Warn("Roostack Cloud's report: asking a server about its backups", "server", n.Identity().Name, "err", err)
			} else {
				if main && st.LastOK != nil {
					b.Last = st.LastOK.Finished
				}
				for _, c := range st.Copies {
					if slices.Contains(repos, c.Repository) && c.LastOK != nil && c.LastOK.Finished.After(b.Last) {
						b.Last = c.LastOK.Finished
					}
				}
			}
		}
		out = append(out, b)
	}
	return out, nil
}
