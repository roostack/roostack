package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/destination"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/units"
)

// Moving a service to another server by copying its data directly
// (SPEC.md §10.10): copy while it runs, stop it, copy what changed, bring
// its secrets, then switch where it runs. The old server's copy is kept.

// dataDirs is how many data folders a service has on every server
// (node.Local.DataDirs: its services folder and its media folder).
const dataDirs = 2

// mtimeMargin covers files changed in the same second as the first copy.
const mtimeMargin = 2 * time.Second

// MoveInfo describes a possible move, for the move page.
type MoveInfo struct {
	From Server
	// Size is the service's data in bytes (-1 if unknown).
	Size int64
	// ViaBackup: moving through a backup is possible (the old server's
	// backups are set up through Roostack).
	ViaBackup bool
	// Suggested is the way the move page suggests.
	Suggested string
}

// largeData is the size above which moving through a backup is suggested:
// the backup is resumable and only uploads what changed since the last one.
const largeData = 10 * units.GB

// MoveInfo returns where a service runs and how much data it has.
func (m *Manager) MoveInfo(ctx context.Context, name string) (MoveInfo, error) {
	_, in, err := m.installedService(name)
	if err != nil {
		return MoveInfo{}, err
	}
	info := MoveInfo{Size: -1}
	servers, err := m.Servers()
	if err != nil {
		return info, err
	}
	host := in.HostOr(m.Lab.NodeID)
	for _, mc := range servers {
		if mc.ID == host {
			info.From = mc
		}
	}
	if mv := node.Capability(m.node(host), node.Node.Mover); mv != nil {
		if size, err := mv.DataSize(ctx, name); err == nil {
			info.Size = size
		}
	}
	if t, _, ok := m.BackupTargetOf(ctx, host); ok && t.Provider != destination.Setup {
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

// MoveService moves a service to another server in the background
// (activity.Move), one of the ways above.
func (m *Manager) MoveService(ctx context.Context, name, to, way string) error {
	p, in, err := m.installedService(name)
	if err != nil {
		return err
	}
	from := in.HostOr(m.Lab.NodeID)
	if to == from {
		return usererr.New("%s already runs there.", p.Title)
	}
	if err := m.checkArch(ctx, p, cmp.Or(in.Title, p.Title), to); err != nil {
		return err
	}
	src, dst := node.Capability(m.node(from), node.Node.Mover), node.Capability(m.node(to), node.Node.Mover)
	if src == nil || dst == nil {
		return usererr.New("Choose a server in this lab.")
	}
	for _, id := range []string{from, to} {
		if r, ok := m.node(id).(RemoteServer); ok && r.Settings() == nil {
			return usererr.New("Roostack can't reach %s right now.", r.Identity().Name)
		}
	}
	var target store.BackupTarget
	switch way {
	case MoveCopy:
	case MoveBackup:
		t, _, ok := m.BackupTargetOf(ctx, from)
		if !ok || t.Provider == destination.Setup || m.node(from).Backups() == nil {
			return usererr.New("%s's backups aren't set up through Roostack yet, so this way isn't available. Copy it directly instead.",
				m.nameOf(from))
		}
		target = t
	default:
		return usererr.New("Choose how to move it.")
	}
	m.forgetFailedMove(ctx, name)
	act := m.Activity.Start(activity.Move, fmt.Sprintf("Moving %s to %s", p.Title, m.nameOf(to)))
	j := &transferJob{m: m, act: act, name: name, title: p.Title, from: from, to: to, src: src, dst: dst}
	go func() {
		ctx := context.Background()
		var err error
		if way == MoveBackup {
			err = j.moveViaBackup(ctx, target)
		} else {
			err = j.moveDirectly(ctx)
		}
		if err != nil {
			m.Log.Error("moving a service", "service", name, "err", err)
		}
		act.Finish(err)
	}()
	return nil
}

// moveDirectly copies the service's data while it runs, stops it, copies
// what changed since, and finishes the move.
func (j *transferJob) moveDirectly(ctx context.Context) (err error) {
	defer func() { j.startAgainIfFailed(ctx, err) }()
	first := time.Now().Add(-mtimeMargin)
	j.act.Step("Copying %s's data to %s while it keeps running", j.title, j.m.nameOf(j.to))
	for dir := range dataDirs {
		if err := copyData(ctx, j.src, j.dst, j.name, dir, time.Time{}); err != nil {
			return err
		}
	}
	if err := j.stop(ctx); err != nil {
		return err
	}
	j.act.Step("Copying what changed since")
	for dir := range dataDirs {
		if err := copyData(ctx, j.src, j.dst, j.name, dir, first); err != nil {
			return err
		}
		keep, err := j.src.ListData(ctx, j.name, dir)
		if err != nil {
			return err
		}
		if err := j.dst.PruneData(ctx, j.name, dir, keep); err != nil {
			return err
		}
	}
	return j.finishMove(ctx)
}

// ownSecrets keeps only the secrets the service's generated files use. The
// names come from the server it ran on, or from that server's backup, so
// a compromised server could otherwise plant another service's password,
// or one of Roostack's own, wherever the service lands (security review
// of 2026-10-16, M20). Anything else is left out, and said.
func (m *Manager) ownSecrets(act *activity.Log, name string, secrets map[string][]byte) (map[string][]byte, error) {
	p, in, err := m.installedService(name)
	if err != nil {
		return nil, err
	}
	out, err := generate.Service(m.Lab, p, in)
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, s := range out.Secrets {
		own[s.Name] = true
	}
	kept := map[string][]byte{}
	for n, v := range secrets {
		if !own[n] {
			m.Log.Warn("left out a secret that isn't the service's own", "service", name, "secret", n)
			act.Printf("Left out the secret %q: it isn't %s's", n, p.Title)
			continue
		}
		kept[n] = v
	}
	return kept, nil
}

// finishMove brings the service's secrets, puts the copied data in place,
// switches where it runs and sets the old copy aside. After a backup, it
// loads its databases from their dumps on the new server, and goes back
// if they don't load.
func (j *transferJob) finishMove(ctx context.Context) error {
	if err := j.bringSecrets(ctx); err != nil {
		return err
	}
	if err := j.commit(ctx); err != nil {
		return err
	}
	j.act.Step("Starting %s on %s", j.title, j.m.nameOf(j.to))
	switched, err := j.switchTo()
	if err != nil {
		return err
	}
	// It runs on the new server now; its old copy stays put.
	if j.repo != nil {
		j.act.Step("Loading its databases from the backup")
		if err := waitApplied(ctx, j.m.node(j.to), switched); err != nil {
			return err
		}
		if err := j.dst.LoadDumps(ctx, j.name); err != nil {
			// Its old copy is still whole: it goes back there (SPEC.md
			// §10.10), or is held stopped here if it can't.
			return j.moveBack(ctx, err)
		}
	}
	// Moved again after a move that couldn't go back: no longer held.
	if _, in, err := j.m.installedService(j.name); err == nil && in.Held != nil && in.Held.MovedFrom != "" {
		if _, err := j.m.releaseHold(j.name, "Starting "+j.title+" on "+j.m.nameOf(j.to)+": it's moved"); err != nil {
			j.act.Printf("%v", err)
		}
	}
	j.setAsideOld(ctx)
	j.act.Step("%s now runs on %s", j.title, j.m.nameOf(j.to))
	return nil
}

// waitApplied waits until a server has applied changes made after t.
func waitApplied(ctx context.Context, n node.Node, t time.Time) error {
	deadline := time.Now().Add(applyWait)
	for time.Now().Before(deadline) {
		if st := n.Status(); !st.Running && st.LastRun.After(t) {
			return st.LastErr
		}
		if !sleep.For(ctx, 2*time.Second) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s didn't finish applying in time", n.Identity().Name)
}

// applyWait bounds waiting for a server to start a moved service (its
// first start may download images).
var applyWait = 20 * time.Minute

// moveViaBackup moves a service through a fresh backup: the old server
// backs up, the service stops, and the controller streams its folders
// from that snapshot to the new server, which loads its databases from
// their dumps. For large data or slow links: the backup is incremental,
// and the controller reads the repository itself (with the recovery key),
// so no keys are shared between servers.
func (j *transferJob) moveViaBackup(ctx context.Context, target store.BackupTarget) (err error) {
	defer func() { j.startAgainIfFailed(ctx, err) }()
	b := j.m.node(j.from).Backups()
	started := time.Now()
	j.act.Step("Backing up %s on %s (only what changed since the last backup is uploaded)", j.title, j.m.nameOf(j.from))
	if err := b.RunNow(ctx); err != nil && !errors.Is(err, backup.ErrAlreadyQueued) {
		return err
	}
	run, err := waitBackup(ctx, b, started)
	if err != nil {
		return err
	}
	if j.repo, err = j.m.adminRepo(ctx, target); err != nil {
		return err
	}
	// The service's own snapshot from that backup.
	snap, err := j.repo.ServiceSnapshot(ctx, j.name, "")
	if err != nil {
		return err
	}
	if snap.Time.Before(started.Add(-time.Minute)) {
		return fmt.Errorf("the backup at %s didn't include %s", run.Finished.Format(time.Kitchen), j.title)
	}
	j.snap = *snap
	j.act.Printf("Backup %s", snap.Short)
	if err := j.stop(ctx); err != nil {
		return err
	}
	j.act.Step("Copying %s's data from the backup to %s", j.title, j.m.nameOf(j.to))
	if err := j.copyFromBackup(ctx); err != nil {
		return err
	}
	return j.finishMove(ctx)
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
		if !sleep.For(ctx, 5*time.Second) {
			return nil, ctx.Err()
		}
	}
}

// pipe connects a writer function to a reader function. It succeeds only
// if both do, and read took everything write wrote.
func pipe(ctx context.Context, write func(io.Writer) error, read func(io.Reader) error) error {
	pr, pw := io.Pipe()
	sent := make(chan error, 1)
	go func() {
		err := write(pw)
		pw.CloseWithError(err)
		sent <- err
	}()
	err := read(pr)
	// Closed even when read succeeded: one that stopped before the end
	// would otherwise leave write waiting for ever. write then fails, and
	// so does the copy, as some of the data wasn't taken.
	pr.CloseWithError(err)
	werr := <-sent
	if errors.Is(werr, backup.ErrNotInSnapshot) {
		return werr
	}
	if err == nil && errors.Is(werr, io.ErrClosedPipe) {
		werr = fmt.Errorf("the receiving side stopped before the end of the data: %w", werr)
	}
	return errors.Join(werr, err)
}

// copyData streams one data folder from one server to the other.
func copyData(ctx context.Context, src, dst node.Mover, name string, dir int, since time.Time) error {
	return pipe(ctx, func(w io.Writer) error { return src.ExportData(ctx, name, dir, since, w) },
		func(r io.Reader) error { return dst.ImportData(ctx, name, dir, r, 0) })
}

// switchHost places the service on another server: its routes get ports
// there, and the servers apply the change.
func (m *Manager) switchHost(name, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	file := state.InstanceFile(m.Lab, name)
	in, err := instance.Load(file)
	if err != nil {
		return err
	}
	in.Host = to
	in.Waiting = false // restored, it runs there
	// Ports are per server; the new one gives them again.
	for route, r := range in.Routes {
		r.Port = 0
		in.Routes[route] = r
	}
	if err := in.Save(file); err != nil {
		return err
	}
	m.applyReason = "Moving " + name + " to another server"
	m.startApplyLocked()
	return m.assignErr
}

// KeptData lists folders moves left on a server.
func (m *Manager) KeptData(ctx context.Context, serverID string) ([]node.KeptFolder, error) {
	mv := node.Capability(m.node(serverID), node.Node.Mover)
	if mv == nil {
		return nil, ErrNotFound
	}
	return mv.KeptData(ctx)
}

// RemoveKeptData deletes a folder a move left on a server, for good.
// PutBackKeptData puts a folder kept on a server back in place of its
// service's data (node.Local.PutBackKeptData).
func (m *Manager) PutBackKeptData(ctx context.Context, serverID, path string) error {
	mv := node.Capability(m.node(serverID), node.Node.Mover)
	if mv == nil {
		return ErrNotFound
	}
	return mv.PutBackKeptData(ctx, path)
}

func (m *Manager) RemoveKeptData(ctx context.Context, serverID, path string) error {
	mv := node.Capability(m.node(serverID), node.Node.Mover)
	if mv == nil {
		return ErrNotFound
	}
	if err := mv.RemoveKeptData(ctx, path); err != nil {
		return err
	}
	// What a failed move copied is gone: so is its note.
	if service, _, ok := strings.Cut(filepath.Base(path), node.FailedMoveSuffix); ok {
		m.forgetFailedMove(ctx, service)
	}
	return nil
}
