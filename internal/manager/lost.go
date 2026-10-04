package manager

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
)

// A server that's gone (SPEC.md §10.10, "Restoring across the lab"): the
// Owner says so for a server that can't be reached, chooses which of its
// services come back onto a server now, and the rest wait to be restored
// (instance.Waiting). Once none of its services are waiting, it leaves
// the lab; its backups stay.

// LostService is a service of a server that's gone, for the plan.
type LostService struct {
	Name, Title string
	// Backup is its newest backup that can be read (nil if none), read
	// from its first place.
	Backup *BackupMoment
	// Size is how much data that backup holds (-1 if unknown).
	Size int64
	// Waiting: it already waits to be restored.
	Waiting bool
}

// LostPlan is what restoring a gone server's services involves.
type LostPlan struct {
	Server   Server
	Services []LostService
	// Targets are the servers its services can go onto (those that can be
	// reached), with Free: each one's free space for services' data (-1 if
	// unknown).
	Targets []Server
	Free    map[string]int64
}

// LostServerPlan describes restoring a server's services: its services
// (placed or already waiting) and their newest readable backups, and the
// servers they can go onto. The server must be one that can't be reached.
func (m *Manager) LostServerPlan(ctx context.Context, id string) (LostPlan, error) {
	srv, err := m.lostServer(id)
	if err != nil {
		return LostPlan{}, err
	}
	plan := LostPlan{Server: srv, Free: map[string]int64{}}
	all, err := state.LoadServices(m.Lab)
	if err != nil {
		return plan, err
	}
	for _, s := range all {
		if s.Instance.Host != id {
			continue
		}
		ls := LostService{Name: s.Package.Name, Title: s.Package.Title, Size: -1, Waiting: s.Instance.Waiting}
		if moments, _, err := m.ServiceBackups(ctx, ls.Name); err == nil && len(moments) > 0 {
			ls.Backup = &moments[0]
			ls.Size = moments[0].Places[0].Snapshot.Size()
		}
		plan.Services = append(plan.Services, ls)
	}
	servers, err := m.Servers()
	if err != nil {
		return plan, err
	}
	for _, s := range servers {
		if s.ID == id || !s.Reachable {
			continue
		}
		plan.Targets = append(plan.Targets, s)
		plan.Free[s.ID] = m.freeForServices(ctx, s.ID)
	}
	return plan, nil
}

// lostServer is a server the Owner may say is gone: another server, which
// can't be reached.
func (m *Manager) lostServer(id string) (Server, error) {
	if id == m.Lab.NodeID {
		return Server{}, ErrNotFound
	}
	servers, err := m.Servers()
	if err != nil {
		return Server{}, err
	}
	for _, s := range servers {
		if s.ID != id {
			continue
		}
		if s.Reachable {
			return s, formErrorf("%s is connected: its services are running there.", s.Name)
		}
		return s, nil
	}
	return Server{}, ErrNotFound
}

// reachable reports whether a server answers (this one always does).
func (m *Manager) reachable(id string) bool {
	if id == m.Lab.NodeID {
		return true
	}
	r, ok := m.node(id).(RemoteServer)
	return ok && r.Settings() != nil
}

// freeForServices is a server's free space for services' data: its data
// disk's (not one only for the backup store), else its system disk's.
// -1 if unknown.
func (m *Manager) freeForServices(ctx context.Context, id string) int64 {
	d, ok := m.node(id).(node.Disks)
	if !ok {
		return -1
	}
	disks, err := d.DiskSpaces(ctx)
	if err != nil {
		return -1
	}
	free := int64(-1)
	for _, disk := range disks {
		switch {
		case disk.Kind == node.DiskData && !disk.Store:
			return disk.Free
		case disk.Kind == node.DiskSystem:
			free = disk.Free
		}
	}
	return free
}

// RestoreLostServer says a server is gone: every service it ran waits to
// be restored, and those named are restored now onto another server, one
// after another, each from its newest backup that can be read (in the
// background, activity.Restore). When none of its services are left
// waiting, the server leaves the lab.
func (m *Manager) RestoreLostServer(ctx context.Context, id string, names []string, to string) error {
	srv, err := m.lostServer(id)
	if err != nil {
		return err
	}
	if len(names) > 0 {
		if to == id || !m.reachable(to) {
			return formErrorf("Choose a server that's connected to restore onto.")
		}
		if _, ok := m.node(to).(node.Mover); !ok {
			return formErrorf("Choose a server in this lab.")
		}
	}
	all, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	var titles []string
	for _, name := range names {
		i := slices.IndexFunc(all, func(s state.Service) bool { return s.Package.Name == name && s.Instance.Host == id })
		if i < 0 {
			return formErrorf("%s didn't run on %s.", name, srv.Name)
		}
		titles = append(titles, all[i].Package.Title)
	}
	// Every one of its services waits, until restored: none runs there
	// any more, even if it comes back.
	for _, s := range all {
		if s.Instance.Host == id && !s.Instance.Waiting {
			if err := m.setWaiting(s.Package.Name, true); err != nil {
				return err
			}
		}
	}
	m.ApplyBecause(srv.Name + " is gone: its services wait to be restored")
	if len(names) == 0 {
		m.leaveIfDone(context.WithoutCancel(ctx), id)
		return nil
	}
	m.mu.Lock()
	if m.restoring == nil {
		m.restoring = map[string]bool{}
	}
	for _, name := range names {
		if m.restoring[name] {
			m.mu.Unlock()
			return backup.ErrRestoreQueued
		}
	}
	for _, name := range names {
		m.restoring[name] = true
	}
	m.mu.Unlock()
	act := m.Activity.Start(activity.Restore, fmt.Sprintf("Restoring %s's services", srv.Name))
	go func() {
		ctx := context.Background()
		var errs []error
		for i, name := range names {
			act.Step("%s (%d of %d)", titles[i], i+1, len(names))
			if err := m.restoreNewest(ctx, act, name, titles[i], id, to); err != nil {
				act.Printf("%s wasn't restored: %v. It's still waiting; try again from its page.", titles[i], err)
				errs = append(errs, fmt.Errorf("%s: %w", titles[i], err))
			}
			m.mu.Lock()
			delete(m.restoring, name)
			m.mu.Unlock()
		}
		m.leaveIfDone(ctx, id)
		err := errors.Join(errs...)
		if err == nil {
			act.Step("%d of %s's services restored onto %s", len(names), srv.Name, m.nameOf(to))
		} else {
			m.Log.Error("restoring a gone server's services", "server", srv.Name, "err", err)
		}
		act.Finish(err)
	}()
	return nil
}

// restoreNewest restores a service from its newest backup that can be
// read, onto a server.
func (m *Manager) restoreNewest(ctx context.Context, act *activity.Log, name, title, host, to string) error {
	moments, _, err := m.ServiceBackups(ctx, name)
	if err != nil {
		return err
	}
	if len(moments) == 0 {
		return errors.New("no backup of it can be read")
	}
	place := moments[0].Places[0]
	repos, err := m.keyRepos(ctx)
	if err != nil {
		return err
	}
	kr, ok := findRepo(repos, place.Ref)
	if !ok {
		return errors.New("its backup isn't there any more")
	}
	repo, err := kr.open(backup.KeyFile(m.Lab.Paths))
	if err != nil {
		return err
	}
	dst := m.node(to).(node.Mover)
	return m.restoreFrom(ctx, act, restoreJob{name: name, title: title, host: host, to: to, repo: repo,
		snap: place.Snapshot, from: fmt.Sprintf("%s's backup at %s", place.ServerName, place.Where)}, dst)
}

// setWaiting marks a service as waiting to be restored, or not.
func (m *Manager) setWaiting(name string, waiting bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	file := state.InstanceFile(m.Lab, name)
	in, err := instance.Load(file)
	if err != nil {
		return err
	}
	in.Waiting = waiting
	return in.Save(file)
}

// leaveIfDone takes a gone server out of the lab once none of its
// services are left (all restored elsewhere or removed). Its backups stay:
// the recovery key still reads them.
func (m *Manager) leaveIfDone(ctx context.Context, id string) {
	all, err := state.LoadServices(m.Lab)
	if err != nil {
		return
	}
	for _, s := range all {
		if s.Instance.Host == id {
			return
		}
	}
	if m.reachable(id) {
		return // it came back meanwhile: the Owner decides
	}
	if err := m.RemoveServer(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
		m.Log.Warn("removing a gone server", "server", m.nameOf(id), "err", err)
	}
}

// RemoveWaiting removes a service that waits to be restored, and deletes
// its backups everywhere (in the background, activity.Restore): only this
// deletes them (SPEC.md §10.10).
func (m *Manager) RemoveWaiting(ctx context.Context, name string) error {
	p, in, err := m.installedService(name)
	if err != nil {
		return err
	}
	if !in.Waiting {
		return formErrorf("%s isn't waiting to be restored.", p.Title)
	}
	host := in.Host
	act := m.Activity.Start(activity.Restore, "Removing "+p.Title+" and its backups")
	go func() {
		ctx := context.Background()
		err := m.forgetServiceBackups(ctx, act, name)
		if err == nil {
			act.Step("Removing %s from the lab", p.Title)
			err = m.Remove(ctx, name)
		}
		if err == nil {
			m.leaveIfDone(ctx, host)
			act.Step("%s and its backups are gone", p.Title)
		}
		act.Finish(err)
	}()
	return nil
}

// forgetServiceBackups deletes every backup of a service, in every
// repository the controller opens. Each is listed first: if one can't be
// read, nothing is deleted (its backups there would stay unseen).
func (m *Manager) forgetServiceBackups(ctx context.Context, act *activity.Log, name string) error {
	repos, err := m.keyRepos(ctx)
	if err != nil {
		return err
	}
	type found struct {
		repo  *backup.Repo
		where string
		ids   []string
	}
	var todo []found
	var errs []error
	act.Step("Finding its backups")
	for _, kr := range repos {
		where, _ := m.repoTitle(ctx, kr)
		where = fmt.Sprintf("%s's backup at %s", m.nameOf(kr.ServerID), where)
		repo, err := kr.open(backup.KeyFile(m.Lab.Paths))
		var snaps []backup.Snapshot
		if err == nil {
			snaps, err = repo.Snapshots(ctx, backup.TagRoostack, backup.ServiceTag(name))
		}
		if err != nil {
			act.Printf("%s: %v", where, err)
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			continue
		}
		f := found{repo: repo, where: where}
		for _, s := range snaps {
			f.ids = append(f.ids, s.ID)
		}
		act.Printf("%s: %d backups", where, len(f.ids))
		todo = append(todo, f)
	}
	if len(errs) > 0 {
		return fmt.Errorf("some backups couldn't be read, so nothing was deleted and it's kept: %w", errors.Join(errs...))
	}
	for _, f := range todo {
		if len(f.ids) == 0 {
			continue
		}
		act.Step("Deleting its %d backups in %s", len(f.ids), f.where)
		if err := f.repo.ForgetSnapshots(ctx, f.ids...); err != nil {
			return fmt.Errorf("%s: %w", f.where, err)
		}
	}
	return nil
}
