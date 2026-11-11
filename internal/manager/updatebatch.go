package manager

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// Choosing several updates at once, on the Updates page (SPEC.md §7.1,
// "The Updates page"): now, one at a time, each server backing up once
// before its first; or tonight, after the nightly backup, as automatic
// updates do.

// tonightKey, in the controller's settings: the updates people chose for
// tonight (map of service name to TonightUpdate, as JSON).
const tonightKey = "service-updates-tonight"

// UpdateChoice is an update someone chose on the Updates page.
type UpdateChoice struct {
	Service string
	// To is the version they saw, so a newer offer meanwhile isn't
	// applied unseen.
	To string
	// ReadNotes: they said they've read its upgrade notes.
	ReadNotes bool
}

// TonightUpdate is an update someone chose for tonight.
type TonightUpdate struct {
	To string    `json:"to"`
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// chosen checks the chosen updates against what's on offer now, and
// returns them in the order they'll run: by server, then by title. It
// fails, with a sentence, on the first that can't be chosen.
func (m *Manager) chosen(ctx context.Context, choices []UpdateChoice) ([]*Update, error) {
	if len(choices) == 0 {
		return nil, usererr.New("Choose at least one update.")
	}
	avail := m.AvailableUpdates(ctx)
	var out []*Update
	seen := map[string]bool{}
	for _, c := range choices {
		if seen[c.Service] {
			continue
		}
		seen[c.Service] = true
		u := avail[c.Service]
		switch {
		case u == nil:
			return nil, usererr.New("There's no update for %s now: have a look at the list again.", c.Service)
		case u.To != c.To:
			return nil, usererr.New("The update on offer for %s has changed to %s: have a look, then choose again.", u.Title, u.ToApp())
		case u.Blocked != "":
			return nil, usererr.New("%s", u.Blocked)
		case len(u.NewRisks) > 0:
			return nil, usererr.New("%s %s asks for more than the version it runs, so only the Owner can update it, from its own page.", u.Title, u.ToApp())
		case len(u.Notes) > 0 && !c.ReadNotes:
			return nil, usererr.New("Read the notes for %s %s before choosing it.", u.Title, u.ToApp())
		case m.Updating(c.Service) != nil:
			return nil, usererr.New("%s is already updating.", u.Title)
		}
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b *Update) int {
		return cmp.Or(strings.Compare(m.nameOf(a.Host), m.nameOf(b.Host)), strings.Compare(a.Title, b.Title))
	})
	return out, nil
}

// UpdateServices starts the chosen updates now, in the background, one at
// a time: by server, each server backing up before its first (and again
// before the next, if that one stopped before its backup), so the others
// on it don't each wait for one. One that doesn't work goes back to its
// previous version, as any update does, and the rest carry on. by names
// who chose them.
func (m *Manager) UpdateServices(ctx context.Context, choices []UpdateChoice, by string) error {
	us, err := m.chosen(ctx, choices)
	if err != nil {
		return err
	}
	m.upd.mu.Lock()
	m.initUpdatesLocked()
	for _, u := range us {
		if m.upd.active[u.Service] != nil {
			m.upd.mu.Unlock()
			return usererr.New("%s is already updating.", u.Title)
		}
	}
	for i, u := range us {
		m.upd.active[u.Service] = queuedUpdate(u, !u.NoBackups, fmt.Sprintf("Waiting its turn: %d of %d", i+1, len(us)))
	}
	m.upd.mu.Unlock()
	why := "Update now, on the Updates page, by " + by
	switch len(us) {
	case 1:
	case 2:
		why = "Update now, chosen with one other on the Updates page by " + by
	default:
		why = fmt.Sprintf("Update now, chosen with %d others on the Updates page by %s", len(us)-1, by)
	}
	go func() {
		m.upd.run.Lock()
		defer m.upd.run.Unlock()
		backedUp := map[string]string{} // server ID: the service it backed up for
		for _, u := range us {
			r := updateRun{why: why, by: by, backup: !u.NoBackups && backedUp[u.Host] == ""}
			if !u.NoBackups && !r.backup {
				r.noBackup = fmt.Sprintf("%s backed up for %s's update just before", m.serverName(u.Host), backedUp[u.Host])
				m.progress(u.Service, func(su *ServiceUpdate) {
					su.Progress.Steps = slices.DeleteFunc(su.Progress.Steps, func(s node.InstallStep) bool { return s.Key == "backup" })
				})
			}
			if rec := m.runQueued(u, r); !rec.Backup.IsZero() {
				backedUp[u.Host] = u.Title
			}
		}
	}()
	return nil
}

// UpdatesTonight are the updates people chose for tonight that are still
// on offer, by service name.
func (m *Manager) UpdatesTonight(ctx context.Context) map[string]TonightUpdate {
	m.upd.tonight.Lock()
	defer m.upd.tonight.Unlock()
	return m.tonightLocked(ctx)
}

// tonightLocked reads the updates chosen for tonight, leaving out those
// no longer on offer (a newer version came, or the service updated or
// went). Callers hold upd.tonight.
func (m *Manager) tonightLocked(ctx context.Context) map[string]TonightUpdate {
	if m.Settings == nil {
		return nil
	}
	saved, err := store.GetJSON[map[string]TonightUpdate](ctx, m.Settings, tonightKey)
	if err != nil || len(saved) == 0 {
		return nil
	}
	avail := m.AvailableUpdates(ctx)
	maps.DeleteFunc(saved, func(name string, t TonightUpdate) bool { return avail[name] == nil || avail[name].To != t.To })
	return saved
}

func (m *Manager) saveTonightLocked(ctx context.Context, t map[string]TonightUpdate) error {
	if m.Settings == nil {
		return fmt.Errorf("no settings to keep tonight's updates in")
	}
	return store.PutJSON(ctx, m.Settings, tonightKey, t)
}

// UpdateTonight chooses updates for tonight: after each server's nightly
// backup, Roostack will start them as it does automatic updates, whatever
// each service's update policy says. A server with no backups set up has
// no nightly backup to wait for, so its services can only update now.
func (m *Manager) UpdateTonight(ctx context.Context, choices []UpdateChoice, by string) error {
	us, err := m.chosen(ctx, choices)
	if err != nil {
		return err
	}
	for _, u := range us {
		if u.NoBackups {
			return usererr.New("%s's server has no backups set up, so there's no nightly backup to wait for: update it now instead.", u.Title)
		}
	}
	m.upd.tonight.Lock()
	defer m.upd.tonight.Unlock()
	t := m.tonightLocked(ctx)
	if t == nil {
		t = map[string]TonightUpdate{}
	}
	now := time.Now()
	for _, u := range us {
		t[u.Service] = TonightUpdate{To: u.To, By: by, At: now}
	}
	return m.saveTonightLocked(ctx, t)
}

// CancelTonight takes a service's update off tonight's.
func (m *Manager) CancelTonight(ctx context.Context, name string) error {
	m.upd.tonight.Lock()
	defer m.upd.tonight.Unlock()
	t := m.tonightLocked(ctx)
	if _, ok := t[name]; !ok {
		return nil
	}
	delete(t, name)
	return m.saveTonightLocked(ctx, t)
}

// takeTonight takes a service's update off tonight's as it starts, if
// it's the version chosen.
func (m *Manager) takeTonight(ctx context.Context, name, to string) {
	m.upd.tonight.Lock()
	defer m.upd.tonight.Unlock()
	t := m.tonightLocked(ctx)
	if t[name].To != to {
		return
	}
	delete(t, name)
	if err := m.saveTonightLocked(ctx, t); err != nil {
		m.Log.Error("taking an update off tonight's", "service", name, "err", err)
	}
}
