package manager

import (
	"context"
	"errors"
	"fmt"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/store"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/state"
)

// Moving a service to another machine by copying its data directly
// (SPEC.md §10.10): copy while it runs, stop it, copy what changed, bring
// its secrets, then switch where it runs. The old machine's copy is kept.

// dataDirs is how many data folders a service has on every machine
// (node.Local.DataDirs: its services folder and its media folder).
const dataDirs = 2

// mtimeMargin covers files changed in the same second as the first copy.
const mtimeMargin = 2 * time.Second

// MoveInfo describes a possible move, for the move page.
type MoveInfo struct {
	From Machine
	// Size is the service's data in bytes (-1 if unknown).
	Size int64
	// ViaBackup: moving through a backup is possible (the old machine's
	// backups are set up through MyLab).
	ViaBackup bool
	// Suggested is the way the move page suggests.
	Suggested string
}

// largeData is the size above which moving through a backup is suggested:
// the backup is resumable and only uploads what changed since the last one.
const largeData = 10 << 30

// MoveInfo returns where a service runs and how much data it has.
func (m *Manager) MoveInfo(ctx context.Context, name string) (MoveInfo, error) {
	_, in, err := m.installedService(name)
	if err != nil {
		return MoveInfo{}, err
	}
	info := MoveInfo{Size: -1}
	machines, err := m.Machines()
	if err != nil {
		return info, err
	}
	host := in.Host
	if host == "" {
		host = m.Lab.NodeID
	}
	for _, mc := range machines {
		if mc.ID == host {
			info.From = mc
		}
	}
	if mv, ok := m.node(host).(node.Mover); ok {
		if size, err := mv.DataSize(ctx, name); err == nil {
			info.Size = size
		}
	}
	if t, _, ok := m.BackupTargetOf(ctx, host); ok && t.Provider != "setup" {
		info.ViaBackup = true
	}
	info.Suggested = MoveCopy
	if info.ViaBackup && info.Size > largeData {
		info.Suggested = MoveBackup
	}
	return info, nil
}

// Ways to move a service.
const (
	MoveCopy   = "copy"   // copy its data directly
	MoveBackup = "backup" // through a fresh backup
)

// MoveService moves a service to another machine in the background
// (activity.Move), one of the ways above.
func (m *Manager) MoveService(ctx context.Context, name, to, way string) error {
	p, in, err := m.installedService(name)
	if err != nil {
		return err
	}
	from := in.Host
	if from == "" {
		from = m.Lab.NodeID
	}
	if to == from {
		return formErrorf("%s already runs there.", p.Title)
	}
	src, ok1 := m.node(from).(node.Mover)
	dst, ok2 := m.node(to).(node.Mover)
	if !ok1 || !ok2 {
		return formErrorf("Choose a machine in this lab.")
	}
	for _, id := range []string{from, to} {
		if r, ok := m.node(id).(RemoteMachine); ok && r.Settings() == nil {
			return formErrorf("MyLab can't reach %s right now.", r.Identity().Name)
		}
	}
	var target store.BackupTarget
	switch way {
	case MoveCopy:
	case MoveBackup:
		t, _, ok := m.BackupTargetOf(ctx, from)
		if !ok || t.Provider == "setup" || m.node(from).Backups() == nil {
			return formErrorf("%s's backups aren't set up through MyLab yet, so this way isn't available. Copy it directly instead.",
				m.nameOf(from))
		}
		target = t
	default:
		return formErrorf("Choose how to move it.")
	}
	act := m.Activity.Start(activity.Move, fmt.Sprintf("Moving %s to %s", p.Title, m.nameOf(to)))
	go func() {
		ctx := context.Background()
		var err error
		if way == MoveBackup {
			err = m.moveViaBackup(ctx, act, name, p.Title, from, to, target, src, dst)
		} else {
			err = m.move(ctx, act, name, p.Title, from, to, src, dst)
		}
		if err != nil {
			m.Log.Error("moving a service", "service", name, "err", err)
		}
		act.Finish(err)
	}()
	return nil
}

func (m *Manager) move(ctx context.Context, act *activity.Log, name, title, from, to string, src, dst node.Mover) (err error) {
	stopped := false
	defer func() {
		if err != nil && stopped {
			act.Step("Starting %s again on %s", title, m.nameOf(from))
			if serr := src.StartService(ctx, name); serr != nil {
				act.Printf("%v", serr)
			}
		}
	}()
	first := time.Now().Add(-mtimeMargin)
	act.Step("Copying %s's data to %s while it keeps running", title, m.nameOf(to))
	for dir := range dataDirs {
		if err := copyData(ctx, src, dst, name, dir, time.Time{}); err != nil {
			return err
		}
	}
	act.Step("Stopping %s on %s", title, m.nameOf(from))
	if err := src.StopService(ctx, name); err != nil {
		return err
	}
	stopped = true
	act.Step("Copying what changed since")
	for dir := range dataDirs {
		if err := copyData(ctx, src, dst, name, dir, first); err != nil {
			return err
		}
		keep, err := src.ListData(ctx, name, dir)
		if err != nil {
			return err
		}
		if err := dst.PruneData(ctx, name, dir, keep); err != nil {
			return err
		}
	}
	stopped, err = m.finishMove(ctx, act, name, title, from, to, src, dst, false)
	return err
}

// finishMove brings the service's secrets, puts the copied data in place,
// switches where it runs and sets the old copy aside. loadDumps loads its
// databases from their dumps on the new machine (after a backup).
// It reports whether the service is still stopped on the old machine.
func (m *Manager) finishMove(ctx context.Context, act *activity.Log, name, title, from, to string,
	src, dst node.Mover, loadDumps bool) (stillStopped bool, err error) {
	act.Step("Copying its secrets (e.g. database passwords)")
	secrets, err := src.ExportSecrets(ctx, name)
	if err != nil {
		return true, err
	}
	if err := dst.ImportSecrets(ctx, secrets); err != nil {
		return true, err
	}
	act.Step("Putting the data in place on %s", m.nameOf(to))
	kept, err := dst.CommitData(ctx, name)
	if err != nil {
		return true, err
	}
	for _, k := range kept {
		act.Printf("Data that was already there is kept at %s", k)
	}

	act.Step("Starting %s on %s", title, m.nameOf(to))
	switched := time.Now()
	if err := m.switchHost(name, to); err != nil {
		return true, err
	}
	// It runs on the new machine now; its old copy stays put.
	if loadDumps {
		act.Step("Loading its databases from the backup")
		if err := waitApplied(ctx, m.node(to), switched); err != nil {
			return false, err
		}
		if err := dst.LoadDumps(ctx, name); err != nil {
			return false, err
		}
	}
	aside, err := src.SetAsideData(ctx, name, "to-"+m.nameOf(to))
	if err != nil {
		act.Printf("Couldn't set the old copy aside on %s: %v", m.nameOf(from), err)
	}
	for _, a := range aside {
		act.Printf("The old copy stays on %s at %s until you delete it", m.nameOf(from), a)
	}
	act.Step("%s now runs on %s", title, m.nameOf(to))
	return false, nil
}

// waitApplied waits until a machine has applied changes made after t.
func waitApplied(ctx context.Context, n node.Node, t time.Time) error {
	deadline := time.Now().Add(applyWait)
	for time.Now().Before(deadline) {
		if st := n.Status(); !st.Running && st.LastRun.After(t) {
			return st.LastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("%s didn't finish applying in time", n.Identity().Name)
}

// applyWait bounds waiting for a machine to start a moved service (its
// first start may download images).
var applyWait = 20 * time.Minute

// moveViaBackup moves a service through a fresh backup: the old machine
// backs up, the service stops, and the controller streams its folders
// from that snapshot to the new machine, which loads its databases from
// their dumps. For large data or slow links: the backup is incremental,
// and the controller reads the repository itself (with the recovery key),
// so no keys are shared between machines.
func (m *Manager) moveViaBackup(ctx context.Context, act *activity.Log, name, title, from, to string,
	target store.BackupTarget, src, dst node.Mover) (err error) {
	stopped := false
	defer func() {
		if err != nil && stopped {
			act.Step("Starting %s again on %s", title, m.nameOf(from))
			if serr := src.StartService(ctx, name); serr != nil {
				act.Printf("%v", serr)
			}
		}
	}()
	b := m.node(from).Backups()
	started := time.Now()
	act.Step("Backing up %s on %s (only what changed since the last backup is uploaded)", title, m.nameOf(from))
	if err := b.RunNow(); err != nil && !errors.Is(err, backup.ErrAlreadyQueued) {
		return err
	}
	run, err := waitBackup(ctx, b, started)
	if err != nil {
		return err
	}
	act.Printf("Backup %s", run.SnapshotID)
	act.Step("Stopping %s on %s", title, m.nameOf(from))
	if err := src.StopService(ctx, name); err != nil {
		return err
	}
	stopped = true
	repo, err := m.adminRepo(ctx, target)
	if err != nil {
		return err
	}
	act.Step("Copying %s's data from the backup to %s", title, m.nameOf(to))
	for dir, path := range []string{filepath.Join(m.Lab.Paths.ServicesDir, name), filepath.Join(m.Lab.Paths.MediaDir, name)} {
		strip := len(strings.Split(strings.Trim(path, "/"), "/"))
		err := pipe(ctx, func(w io.Writer) error { return repo.Dump(ctx, run.SnapshotID, path, w) },
			func(r io.Reader) error { return dst.ImportData(ctx, name, dir, r, strip) })
		if errors.Is(err, backup.ErrNotInSnapshot) {
			continue // e.g. no media folder
		}
		if err != nil {
			return err
		}
	}
	stopped, err = m.finishMove(ctx, act, name, title, from, to, src, dst, true)
	return err
}

// waitBackup waits for a backup that started after t, and returns it.
func waitBackup(ctx context.Context, b node.Backups, t time.Time) (*store.BackupRun, error) {
	for {
		st, err := b.Status(ctx)
		if err != nil {
			return nil, err
		}
		if !st.Running && !st.Queued && st.Last != nil && st.Last.Finished.After(t) {
			if !st.Last.OK {
				return nil, fmt.Errorf("the backup didn't finish: %s", st.Last.Error)
			}
			return st.Last, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// pipe connects a writer function to a reader function.
func pipe(ctx context.Context, write func(io.Writer) error, read func(io.Reader) error) error {
	pr, pw := io.Pipe()
	sent := make(chan error, 1)
	go func() {
		err := write(pw)
		pw.CloseWithError(err)
		sent <- err
	}()
	err := read(pr)
	if err != nil {
		pr.CloseWithError(err)
	}
	werr := <-sent
	if errors.Is(werr, backup.ErrNotInSnapshot) {
		return werr
	}
	return errors.Join(werr, err)
}

// copyData streams one data folder from one machine to the other.
func copyData(ctx context.Context, src, dst node.Mover, name string, dir int, since time.Time) error {
	return pipe(ctx, func(w io.Writer) error { return src.ExportData(ctx, name, dir, since, w) },
		func(r io.Reader) error { return dst.ImportData(ctx, name, dir, r, 0) })
}

// switchHost places the service on another machine: its routes get ports
// there, and the machines apply the change.
func (m *Manager) switchHost(name, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	file := state.InstanceFile(m.Lab, name)
	in, err := instance.Load(file)
	if err != nil {
		return err
	}
	in.Host = to
	// Ports are per machine; the new one gives them again.
	for route, r := range in.Routes {
		r.Port = 0
		in.Routes[route] = r
	}
	if err := in.Save(file); err != nil {
		return err
	}
	m.startApplyLocked()
	return m.assignErr
}

// KeptData lists folders moves left on a machine.
func (m *Manager) KeptData(ctx context.Context, machineID string) ([]node.KeptFolder, error) {
	mv, ok := m.node(machineID).(node.Mover)
	if !ok {
		return nil, ErrNotFound
	}
	return mv.KeptData(ctx)
}

// RemoveKeptData deletes a folder a move left on a machine, for good.
func (m *Manager) RemoveKeptData(ctx context.Context, machineID, path string) error {
	mv, ok := m.node(machineID).(node.Mover)
	if !ok {
		return ErrNotFound
	}
	return mv.RemoveKeptData(ctx, path)
}
