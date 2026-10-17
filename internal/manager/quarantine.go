package manager

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/units"
)

// Junk in backups (SPEC.md §10.10, "Junk in a repository"): a server
// that's taken over can't delete its backups on an append-only store, but
// it can add files restic can't read, which stop backups, prunes and
// restores there. The controller looks for them with the recovery key
// weekly, before the prune, and soon after a server's backup fails that
// way. It removes them where it has its own access (a server's backup
// store, storage at a friend's lab, a folder on the controller) without
// asking (the Owner's choice, 2026-10-17: they're of no use to restic),
// and says so under Needs attention until the Owner has seen it.

// JunkReport is what the controller found in one repository.
type JunkReport struct {
	Found time.Time `json:"found"`
	// ServerID is whose backup it is, and Server its name then.
	ServerID string `json:"server_id"`
	Server   string `json:"server"`
	// Provider is the backup's place (its reference), Where names it, and
	// Repository is where it is (the files are inside it).
	Provider   string `json:"provider"`
	Where      string `json:"where"`
	Repository string `json:"repository"`
	// Copy: an extra copy rather than the server's main backup.
	Copy bool `json:"copy,omitempty"`
	// Writers name who could have written there.
	Writers []string      `json:"writers"`
	Files   []backup.Junk `json:"files"`
	// Err is why some weren't removed: Roostack has no access of its own
	// there (online storage), or removing failed.
	Err string `json:"err,omitempty"`
}

// Removed reports whether every file found was removed.
func (r JunkReport) Removed() bool {
	return !slices.ContainsFunc(r.Files, func(j backup.Junk) bool { return !j.Removed })
}

// junkKey is the controller's setting holding the reports the Owner
// hasn't dismissed yet.
const junkKey = "backups.junk"

// junkState is the controller's memory of repositories checked.
type junkState struct {
	mu sync.Mutex
	// good holds the files read fine in each repository (by pruneKey):
	// they never change, so they're read once per run of Roostack.
	good map[string]map[string]bool
	// checked is when each repository was last looked at after a failed
	// backup, so a backup failing for another reason isn't checked hourly.
	checked map[string]time.Time
}

// junkRecheckAfter is how soon a repository is looked at again after a
// failed backup.
const junkRecheckAfter = 6 * time.Hour

// JunkJob looks for junk in the repositories the controller made (all,
// or those in only, by pruneKey) and removes what it can. Problems with
// one repository go in its log and reports, so the prune after it runs.
func (m *Manager) JunkJob(only map[string]bool) maintenance.Job {
	return maintenance.Job{
		Name: "checking backups for files Roostack can't read",
		Run: func(ctx context.Context, h *maintenance.Handle) error {
			if m.BackupStore == nil {
				return nil
			}
			targets, err := m.BackupStore.BackupTargets(ctx)
			if err != nil {
				return err
			}
			copies, err := m.BackupStore.BackupCopies(ctx)
			if err != nil {
				return err
			}
			var act *activity.Log
			for i, t := range append(targets, copies...) {
				if strings.HasPrefix(t.Provider, "setup") || (only != nil && !only[pruneKey(t)]) {
					continue // that server prunes it itself; or not asked for
				}
				if act == nil && m.Activity != nil {
					act = m.Activity.Start(activity.Backup, "Checking backups for files Roostack can't read")
				}
				m.checkJunk(ctx, act, t, i >= len(targets))
			}
			if act != nil {
				act.Finish(nil)
			}
			return nil
		},
	}
}

// checkJunk looks in one repository, and records what it found.
func (m *Manager) checkJunk(ctx context.Context, act *activity.Log, t store.BackupTarget, isCopy bool) {
	say := func(format string, args ...any) {
		if act != nil {
			act.Printf(format, args...)
		}
	}
	d, err := m.destinationOf(ctx, t.Provider)
	if err != nil {
		say("%s: %v", m.nameOf(t.ServerID), err)
		return
	}
	repo, err := m.adminRepo(ctx, t)
	if err != nil {
		say("%s: %v", m.nameOf(t.ServerID), err)
		return
	}
	if act != nil {
		act.Step("%s: %s", m.nameOf(t.ServerID), m.junkWhere(ctx, t))
	}
	m.junk.mu.Lock()
	if m.junk.good == nil {
		m.junk.good = map[string]map[string]bool{}
	}
	good := m.junk.good[pruneKey(t)]
	if good == nil {
		good = map[string]bool{}
		m.junk.good[pruneKey(t)] = good
	}
	m.junk.mu.Unlock()
	// One check at a time per repository holds good; JunkJob runs under
	// the maintenance coordinator, one job at a time.
	files := backup.FilesOf(repo)
	junk, err := backup.CleanJunk(ctx, repo, files, good)
	if len(junk) == 0 {
		if err != nil {
			say("Couldn't look: %v", err)
		}
		return
	}
	r := JunkReport{Found: time.Now(), ServerID: t.ServerID, Server: m.nameOf(t.ServerID), Provider: t.Provider,
		Where: m.junkWhere(ctx, t), Repository: t.Repository, Copy: isCopy, Writers: m.junkWriters(t, d), Files: junk}
	switch {
	case err != nil:
		r.Err = err.Error()
	case files == nil:
		r.Err = "Roostack has no access of its own there to remove them."
	}
	for _, j := range junk {
		state := "removed"
		if !j.Removed {
			state = "not removed"
		}
		say("%s (%s, %s): %s", j.Path(), sizeText(j.Size), j.Why, state)
		m.Log.Warn("junk in a backup", "server", t.ServerID, "where", t.Provider, "file", j.Path(), "size", j.Size,
			"why", j.Why, "removed", j.Removed)
	}
	if r.Err != "" {
		say("%s", r.Err)
	}
	if err := m.addJunkReport(ctx, r); err != nil {
		m.Log.Error("recording junk in a backup", "err", err)
	}
}

// junkWhere names a repository for people, in the middle of a sentence
// ("… backups on this server's own disk").
func (m *Manager) junkWhere(ctx context.Context, t store.BackupTarget) string {
	choices, _ := m.BackupChoices(ctx)
	title := m.destinationTitle(ctx, t.ServerID, t.Provider, t.Repository, choices)
	for _, w := range []string{"This ", "The ", "A "} {
		if rest, ok := strings.CutPrefix(title, w); ok {
			return strings.ToLower(w) + rest
		}
	}
	return title
}

// junkWriters are who could have written to a server's repository: the
// server itself, and whoever has the files.
func (m *Manager) junkWriters(t store.BackupTarget, d destination) []string {
	out := []string{m.nameOf(t.ServerID)}
	switch d.kind {
	case destServer:
		out = append(out, m.nameOf(d.id))
	case destRemote:
		if d.remote.Servers[t.ServerID] == "" {
			// Set up before servers had their own users there.
			out = append(out, "any of this lab's servers")
		}
		out = append(out, d.remote.HostName)
	case destS3:
		out = append(out, "anyone with "+d.provider.Name+"'s key")
	case destCloud:
		out = append(out, "Roostack Cloud")
	}
	return slices.Compact(out)
}

func sizeText(n int64) string {
	if n < 0 {
		return "size unknown"
	}
	return units.Bytes(n)
}

// JunkReports are the reports the Owner hasn't dismissed.
func (m *Manager) JunkReports(ctx context.Context) []JunkReport {
	if m.Settings == nil {
		return nil
	}
	v, err := m.Settings.Setting(ctx, junkKey)
	if err != nil || v == "" {
		return nil
	}
	var out []JunkReport
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		m.Log.Warn("reading junk reports", "err", err)
	}
	return out
}

// maxJunkReports is how many reports are kept (the newest).
const maxJunkReports = 20

func (m *Manager) addJunkReport(ctx context.Context, r JunkReport) error {
	if m.Settings == nil {
		return errors.New("no settings store")
	}
	m.junk.mu.Lock()
	defer m.junk.mu.Unlock()
	reports := append(m.JunkReports(ctx), r)
	if len(reports) > maxJunkReports {
		reports = reports[len(reports)-maxJunkReports:]
	}
	data, err := json.Marshal(reports)
	if err != nil {
		return err
	}
	return m.Settings.SetSetting(ctx, junkKey, string(data))
}

// DismissJunk forgets the reports: the Owner has seen them.
func (m *Manager) DismissJunk(ctx context.Context) error {
	if m.Settings == nil {
		return nil
	}
	m.junk.mu.Lock()
	defer m.junk.mu.Unlock()
	return m.Settings.SetSetting(ctx, junkKey, "")
}

// CheckForJunk looks at every server's latest backups (hourly): one that
// failed in a way junk causes has its repository checked soon, rather
// than waiting for the weekly check.
func (m *Manager) CheckForJunk(ctx context.Context) {
	if m.BackupStore == nil || m.Submit == nil {
		return
	}
	only := map[string]bool{}
	now := time.Now()
	m.junk.mu.Lock()
	if m.junk.checked == nil {
		m.junk.checked = map[string]time.Time{}
	}
	m.junk.mu.Unlock()
	suspect := func(serverID, provider string, last *store.BackupRun) {
		if last == nil || !backup.LooksLikeJunk(last.Error) {
			return
		}
		key := serverID + " " + provider // pruneKey
		m.junk.mu.Lock()
		defer m.junk.mu.Unlock()
		if now.Sub(m.junk.checked[key]) < junkRecheckAfter {
			return
		}
		m.junk.checked[key] = now
		only[key] = true
	}
	ids := []string{m.Lab.NodeID}
	for _, r := range m.others() {
		ids = append(ids, r.Identity().ID)
	}
	for _, id := range ids {
		n := m.node(id)
		if n == nil || n.Backups() == nil {
			continue
		}
		st, err := n.Backups().Status(ctx)
		if err != nil {
			continue
		}
		if t, _, ok := m.BackupTargetOf(ctx, id); ok {
			suspect(id, t.Provider, st.Last)
		}
		for _, c := range st.Copies {
			suspect(id, c.Name, c.Last)
		}
	}
	if len(only) > 0 {
		m.Log.Info("a backup failed as junk in its repository would make it fail: checking", "repositories", len(only))
		m.Submit(m.JunkJob(only))
	}
}
