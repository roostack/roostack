package web

import (
	"cmp"
	"github.com/mylab/mylab/internal/manager"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/node"
)

// A lab's machines each have their own maintenance, settings and
// activity (SPEC.md §10.7). Pages about a machine other than this one
// live under /settings/servers/<id>/…; this machine keeps its familiar
// addresses.

// machineOf is the machine a request is about: named in the path or query
// ("machine"), the one running the service in the path ("name"), or this
// machine. Nil if there is none (or no such machine).
func (s *Server) machineOf(r *http.Request) ServiceNode {
	if id := cmp.Or(r.PathValue("machine"), r.URL.Query().Get("machine")); id != "" {
		if s.Manager == nil {
			return nil
		}
		if n := s.Manager.NodeByID(id); n != nil {
			return n
		}
		return nil
	}
	if name := r.PathValue("name"); name != "" && s.Manager != nil {
		if n := s.Manager.NodeFor(name); n != nil {
			return n
		}
	}
	if s.Node == nil {
		return nil
	}
	return s.Node
}

// machineView names a machine and where its pages and forms are.
type machineView struct {
	ID, Name string
	// This is the machine serving the UI.
	This bool
}

func (s *Server) machineViewOf(r *http.Request) machineView {
	n := s.machineOf(r)
	if n == nil || s.Node == nil {
		return machineView{This: true}
	}
	// A requested server is named as the lab lists it.
	if id := cmp.Or(r.PathValue("machine"), r.URL.Query().Get("machine")); id != "" && s.Manager != nil {
		if ms, err := s.Manager.Machines(); err == nil {
			for _, m := range ms {
				if m.ID == id {
					return machineView{ID: m.ID, Name: m.Name, This: m.This}
				}
			}
		}
	}
	id := n.Identity()
	return machineView{ID: id.ID, Name: id.Name, This: id.ID == s.Node.Identity().ID}
}

// Label is how pages refer to the machine.
func (m machineView) Label() string {
	if m.This || m.Name == "" {
		return "This server"
	}
	return m.Name
}

func (m machineView) path(this, other string) string {
	if m.This {
		return this
	}
	return "/settings/servers/" + m.ID + other
}

func (m machineView) Updates() string    { return m.path("/settings/updates", "/updates") }
func (m machineView) UpdatesRun() string { return m.path("/settings/updates/run", "/updates/run") }
func (m machineView) Restart() string    { return m.path("/settings/restart", "/restart") }
func (m machineView) RestartPolicy() string {
	return m.path("/settings/restart-policy", "/restart-policy")
}
func (m machineView) TimeZone() string     { return m.path("/settings/time-zone", "/time-zone") }
func (m machineView) TimeZonePost() string { return m.path("/setup/timezone", "/time-zone") }
func (m machineView) RemoteAccess() string {
	return m.path("/settings/remote-access", "/remote-access")
}
func (m machineView) RemoteAccessPost() string {
	return m.path("/setup/ssh", "/remote-access")
}
func (m machineView) Backups() string   { return m.path("/backups", "/backups") }
func (m machineView) BackupRun() string { return m.path("/backup/run", "/backups/run") }

// machineRoutes serves this machine's settings pages for other machines.
func (s *Server) machineRoutes(mux *http.ServeMux) {
	const p = "/settings/servers/{machine}"
	for route, h := range map[string]http.HandlerFunc{
		"GET " + p + "/updates":         s.updatesPage,
		"POST " + p + "/updates/run":    s.updateNowSubmit,
		"POST " + p + "/restart":        s.restartNowSubmit,
		"POST " + p + "/restart-policy": s.restartPolicySubmit,
		"GET " + p + "/time-zone":       s.timezonePage,
		"POST " + p + "/time-zone":      s.timezoneSubmit,
		"GET " + p + "/remote-access":   s.sshPage,
		"POST " + p + "/remote-access":  s.sshSubmit,
		"GET " + p + "/backups":         s.backupsPage,
		"POST " + p + "/backups/run":    s.backupRun,
	} {
		mux.Handle(route, s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
			if s.machineOf(r) == nil {
				http.NotFound(w, r)
				return
			}
			h(w, r)
		}))
	}
}

var _ ServiceNode = node.Node(nil)

// serverView is a server's card on the home page.
type serverView struct {
	manager.Machine
	// Reachable: it answered (the controller always does).
	Reachable  bool
	LastBackup time.Time
	// BackupsSetUp: its backups are configured.
	BackupsSetUp  bool
	RestartNeeded bool
	UpdatesFailed bool
}

// serverViews describes every server for the home page, asking each at
// once.
func (s *Server) serverViews(r *http.Request) []serverView {
	if s.Manager == nil {
		return nil
	}
	machines, err := s.Manager.Machines()
	if err != nil {
		return nil
	}
	out := make([]serverView, len(machines))
	var wg sync.WaitGroup
	for i, mc := range machines {
		wg.Go(func() {
			v := serverView{Machine: mc, Reachable: mc.This || mc.Status.LastErr == nil ||
				!strings.HasPrefix(mc.Status.LastErr.Error(), "MyLab can't reach")}
			n := s.Manager.NodeByID(mc.ID)
			if n != nil && v.Reachable {
				if b := n.Backups(); b != nil {
					if st, err := b.Status(r.Context()); err == nil {
						v.BackupsSetUp = st.Configured
						if st.LastOK != nil {
							v.LastBackup = st.LastOK.Finished
						}
					}
				}
				if u := n.Updates(); u != nil {
					v.RestartNeeded = u.RestartStatus().Needed
					if last, err := u.Last(r.Context()); err == nil && last != nil {
						v.UpdatesFailed = !last.OK
					}
				}
			}
			out[i] = v
		})
	}
	wg.Wait()
	return out
}
