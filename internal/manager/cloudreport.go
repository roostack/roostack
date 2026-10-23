package manager

import (
	"context"
	"slices"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/destination"
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
			main = main || t.ServerID == id && t.Provider == destination.Cloud
		}
		var repos []string // its copies in Cloud
		for _, c := range copies {
			if c.ServerID == id && c.Provider == destination.Cloud {
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

// CloudPaused is how much of the lab's public access is paused, for the
// lab's report to Cloud (SPEC.md §6.16): how many public services, and
// since the earliest of their pauses.
func (m *Manager) CloudPaused() (*cloudapi.PublicPaused, error) {
	pp, err := m.PublicPauses()
	if err != nil {
		return nil, err
	}
	out := &cloudapi.PublicPaused{}
	earliest := func(t time.Time) {
		if !t.IsZero() && (out.Since.IsZero() || t.Before(out.Since)) {
			out.Since = t
		}
	}
	for _, s := range pp.PausedServices() {
		out.Services++
		if s.Own != nil {
			earliest(s.Own.Since)
		}
		if s.ByAll {
			earliest(pp.All.Since)
		}
		for _, b := range s.ByBuiltWith {
			earliest(pp.BuiltWith[b].Since)
		}
		if s.Advisory != nil {
			earliest(s.Advisory.Since)
		}
	}
	return out, nil
}
