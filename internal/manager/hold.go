package manager

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
)

// A restore that didn't finish (SPEC.md §6.9): a restore, or a move through
// a backup, puts the service's files in place and then loads its databases
// from the dumps among them. When that fails, the service would start on
// files without the databases that go with them, so it's held stopped in
// its desired state (instance.Hold: only its databases run) until the Owner
// chooses to restore it again, put back what was there, or start it
// anyway. A move that fails there goes back to where it ran instead.

// holdService holds a service stopped on the server in its instance file
// (instance.Hold), and applies, so every apply after, and a reboot, leaves
// its app's containers out.
func (m *Manager) holdService(name string, h instance.Hold) error {
	return m.holdOn(name, "", h)
}

// holdOn holds a service stopped, placing it on server to as well unless
// to is "" (a move that couldn't go back).
func (m *Manager) holdOn(name, to string, h instance.Hold) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	file := state.InstanceFile(m.Lab, name)
	in, err := instance.Load(file)
	if err != nil {
		return err
	}
	if to != "" && in.Host != to {
		in.Host = to
		for route, r := range in.Routes { // ports are per server
			r.Port = 0
			in.Routes[route] = r
		}
	}
	h.At = time.Now()
	in.Held = &h
	if err := in.Save(file); err != nil {
		return err
	}
	m.Log.Warn("holding a service stopped: its databases didn't load", "service", name, "err", h.Error)
	m.applyReason = "Keeping " + name + " stopped: its databases didn't load"
	m.startApplyLocked()
	return m.assignErr
}

// releaseHold ends a service's hold, if it has one, and applies, so it
// starts. It reports whether there was one.
func (m *Manager) releaseHold(name, reason string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	file := state.InstanceFile(m.Lab, name)
	in, err := instance.Load(file)
	if err != nil || in.Held == nil {
		return false, err
	}
	in.Held = nil
	if err := in.Save(file); err != nil {
		return false, err
	}
	m.applyReason = reason
	m.startApplyLocked()
	return true, nil
}

// audit records something Roostack did by itself in the audit log.
func (m *Manager) audit(ctx context.Context, action, detail string) {
	if m.Audit != nil {
		m.Audit(ctx, action, detail)
	}
}

// heldService is a held service's package title and hold.
func (m *Manager) heldService(name string) (string, *instance.Hold, error) {
	p, in, err := m.installedService(name)
	if err != nil {
		return "", nil, err
	}
	if in.Held == nil {
		return "", nil, formErrorf("%s isn't stopped any more, so there's nothing to choose: reload the page to see how it's doing.", cmp.Or(in.Title, p.Title))
	}
	return cmp.Or(in.Title, p.Title), in.Held, nil
}

// StartHeld starts a held service anyway, on whatever is in its
// databases now (the Owner's choice).
func (m *Manager) StartHeld(ctx context.Context, name string) error {
	title, _, err := m.heldService(name)
	if err != nil {
		return err
	}
	_, err = m.releaseHold(name, "Starting "+title+" anyway, at the Owner's word, though its databases didn't load")
	return err
}

// PutBackHeld puts back a held service's data from before its restore,
// then ends the hold, so it starts as it was. The files from the backup
// are kept in turn.
func (m *Manager) PutBackHeld(ctx context.Context, name string) error {
	title, h, err := m.heldService(name)
	if err != nil {
		return err
	}
	if h.Kept == "" {
		return formErrorf("There's no data from before the restore to put back, so restore it again or start it anyway instead.")
	}
	_, in, err := m.installedService(name)
	if err != nil {
		return err
	}
	host := cmp.Or(in.Host, m.Lab.NodeID)
	if !h.KeptByRestore {
		mv, ok := m.node(host).(node.Mover)
		if !ok {
			return ErrNotFound
		}
		if err := mv.PutBackKeptData(ctx, h.Kept); err != nil {
			return err
		}
		_, err := m.releaseHold(name, "Starting "+title+" again: its data from before its restore is back")
		return err
	}
	// The server's own restore kept it: putting it back is a piece of
	// maintenance there, in the background.
	x := m.node(host).Restores()
	if x == nil {
		return formErrorf("Roostack can't reach %s right now, so it can't put the data back yet: try again once %s is back on.",
			m.nameOf(host), m.nameOf(host))
	}
	if err := x.PutBack(ctx, name, h.Kept); err != nil {
		return err
	}
	go m.watchRestore(name, host, "", true)
	return nil
}

// restoreWatch is how often the controller asks a server whether its own
// restore of a service has ended, and restoreWatchFor how long it asks.
var (
	restoreWatch    = 2 * time.Second
	restoreWatchFor = 24 * time.Hour
)

// watchRestore waits for a server's own restore (or put back, putBack) of
// a service to end, then holds the service stopped if its databases didn't
// load, or ends its hold if it worked. backupID is the backup restored,
// for restoring it again.
//
// The server leaves the service stopped meanwhile; if the controller
// restarts before it ends, nothing holds it (SPEC.md §6.9).
func (m *Manager) watchRestore(name, host, backupID string, putBack bool) {
	ctx := context.Background()
	deadline := time.Now().Add(restoreWatchFor)
	for {
		time.Sleep(restoreWatch)
		x := m.node(host).Restores()
		if x == nil {
			if time.Now().After(deadline) {
				return
			}
			continue // it doesn't answer right now
		}
		pending, err := x.Pending(ctx)
		if err == nil && !pending {
			o, err := x.Outcome(ctx, name)
			if err != nil {
				m.Log.Error("reading how a restore ended", "service", name, "err", err)
				return
			}
			m.restoreEnded(ctx, name, backupID, putBack, o.Finished && o.Err == "", o.DumpsFailed, instance.Hold{
				Backup: backupID, Taken: o.Taken, Error: o.Err, Kept: o.Kept, KeptByRestore: true})
			return
		}
		if time.Now().After(deadline) {
			return
		}
	}
}

// restoreEnded holds a service whose databases didn't load (h), or ends
// its hold once a restore or put back worked.
func (m *Manager) restoreEnded(ctx context.Context, name, backupID string, putBack, ok, dumpsFailed bool, h instance.Hold) {
	switch {
	case dumpsFailed:
		if err := m.holdService(name, h); err != nil {
			m.Log.Error("holding a service stopped", "service", name, "err", err)
		}
	case ok:
		title := name
		if p, in, err := m.installedService(name); err == nil {
			title = cmp.Or(in.Title, p.Title)
		}
		reason := "Starting " + title + ": it's restored"
		if putBack {
			reason = "Starting " + title + ": its data from before its restore is back"
		}
		released, err := m.releaseHold(name, reason)
		if err != nil {
			m.Log.Error("ending a service's hold", "service", name, "err", err)
		}
		if released && !putBack {
			m.audit(ctx, AuditHoldEnded, "Restored "+title+", so it's no longer stopped")
		}
	}
}

// AuditHoldEnded is the audit log's action when a restore that worked ends
// a hold.
const AuditHoldEnded = "restore"

// FailedMove is a move through a backup whose databases didn't load on
// the new server, so the service went back (SPEC.md §10.10).
type FailedMove struct {
	To, ToName, FromName string
	At                   time.Time
}

// failedMoveKey is the controller setting that remembers a service's
// failed move, for Needs attention, until it's moved again or what was
// copied is deleted.
func failedMoveKey(name string) string { return "move-failed:" + name }

// FailedMoves are the services whose last move didn't finish and went
// back, by name.
func (m *Manager) FailedMoves(ctx context.Context) map[string]FailedMove {
	if m.Settings == nil {
		return nil
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return nil
	}
	out := map[string]FailedMove{}
	for _, s := range services {
		v, _ := m.Settings.Setting(ctx, failedMoveKey(s.Name()))
		to, at, ok := strings.Cut(v, " ")
		if !ok {
			continue
		}
		unix, _ := strconv.ParseInt(at, 10, 64)
		out[s.Name()] = FailedMove{To: to, ToName: m.nameOf(to), FromName: m.nameOf(cmp.Or(s.Instance.Host, m.Lab.NodeID)),
			At: time.Unix(unix, 0)}
	}
	return out
}

// forgetFailedMove clears a service's failed move.
func (m *Manager) forgetFailedMove(ctx context.Context, name string) {
	if m.Settings != nil {
		if v, _ := m.Settings.Setting(ctx, failedMoveKey(name)); v != "" {
			m.Settings.SetSetting(ctx, failedMoveKey(name), "")
		}
	}
}

// moveBack puts a service back on the server it moved from after its
// databases didn't load on the new one (cause), and sets what was copied
// there aside. If that fails too, it's held stopped on the new server.
// The error is the move log's last line.
func (m *Manager) moveBack(ctx context.Context, act interface {
	Step(string, ...any)
	Printf(string, ...any)
}, name, title, from, to string, dst node.Mover, cause error) error {
	act.Printf("Its database didn't load on %s: %v", m.nameOf(to), cause)
	act.Step("Moving %s back to %s", title, m.nameOf(from))
	switched := time.Now()
	err := m.switchHost(name, from)
	if err == nil {
		err = waitApplied(ctx, m.node(from), switched)
	}
	if err != nil {
		act.Printf("%v", err)
		h := instance.Hold{Error: err.Error(), MovedFrom: from}
		if herr := m.holdOn(name, to, h); herr != nil {
			act.Printf("Roostack couldn't record that %s should stay stopped (%v), so it may start again at the next change on %s.",
				title, herr, m.nameOf(to))
		}
		return fmt.Errorf("%s is stopped on %s: its database didn't load there, and moving it back to %s failed too: %v. "+
			"Roostack will keep it stopped until you choose.", title, m.nameOf(to), m.nameOf(from), err)
	}
	if m.Settings != nil {
		m.Settings.SetSetting(ctx, failedMoveKey(name), to+" "+strconv.FormatInt(time.Now().Unix(), 10))
	}
	where := ""
	aside, aerr := dst.SetAsideData(ctx, name, node.FailedMove)
	if aerr != nil {
		act.Printf("Roostack couldn't set aside what was copied to %s (%v), so it's still in %s's usual folders there.",
			m.nameOf(to), aerr, title)
	} else if len(aside) > 0 {
		where = ", at " + strings.Join(aside, " and ") + ","
	}
	return fmt.Errorf("%s couldn't be moved to %s: its database didn't load there. It's running on %s again, as before. "+
		"What was copied to %s will stay there%s until you delete it.", title, m.nameOf(to), m.nameOf(from), m.nameOf(to), where)
}
