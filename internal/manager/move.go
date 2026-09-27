package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
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
}

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
	return info, nil
}

// MoveService moves a service to another machine in the background
// (activity.Move).
func (m *Manager) MoveService(ctx context.Context, name, to string) error {
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
	act := m.Activity.Start(activity.Move, fmt.Sprintf("Moving %s to %s", p.Title, m.nameOf(to)))
	go func() {
		err := m.move(context.Background(), act, name, p.Title, from, to, src, dst)
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
	act.Step("Copying its secrets (e.g. database passwords)")
	secrets, err := src.ExportSecrets(ctx, name)
	if err != nil {
		return err
	}
	if err := dst.ImportSecrets(ctx, secrets); err != nil {
		return err
	}
	act.Step("Putting the data in place on %s", m.nameOf(to))
	kept, err := dst.CommitData(ctx, name)
	if err != nil {
		return err
	}
	for _, k := range kept {
		act.Printf("Data that was already there is kept at %s", k)
	}

	act.Step("Starting %s on %s", title, m.nameOf(to))
	if err := m.switchHost(name, to); err != nil {
		return err
	}
	stopped = false // it runs on the new machine now
	aside, err := src.SetAsideData(ctx, name, "to-"+m.nameOf(to))
	if err != nil {
		act.Printf("Couldn't set the old copy aside on %s: %v", m.nameOf(from), err)
	}
	for _, a := range aside {
		act.Printf("The old copy stays on %s at %s until you delete it", m.nameOf(from), a)
	}
	act.Step("%s now runs on %s", title, m.nameOf(to))
	return nil
}

// copyData streams one data folder from one machine to the other.
func copyData(ctx context.Context, src, dst node.Mover, name string, dir int, since time.Time) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	sent := make(chan error, 1)
	go func() {
		err := src.ExportData(ctx, name, dir, since, pw)
		pw.CloseWithError(err)
		sent <- err
	}()
	err := dst.ImportData(ctx, name, dir, pr)
	if err != nil {
		cancel()
		pr.CloseWithError(err)
	}
	return errors.Join(<-sent, err)
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
