package web

import (
	"cmp"
	"net/http"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
)

// A lab's servers each have their own maintenance, settings and
// activity (SPEC.md §10.7). Pages about a server other than this one
// live under /settings/servers/<id>/…; this server keeps its familiar
// addresses.

// serverOf is the server a request is about: named in the path
// ("server"), the one running the service in the path ("name"), named in
// the query ("server", for live parts of a server's pages), or this server.
// A service's server always comes from the service, never the query. Nil
// if there is none (or no such server).
func (s *Server) serverOf(r *http.Request) ServiceNode {
	if id := r.PathValue("server"); id != "" {
		return s.serverByID(id)
	}
	if name := r.PathValue("name"); name != "" {
		if n := s.Manager.NodeFor(name); n != nil {
			return n
		}
	} else if id := r.URL.Query().Get("server"); id != "" {
		return s.serverByID(id)
	}
	if s.Node == nil {
		return nil
	}
	return s.Node
}

// serverByID is one of the lab's servers, or nil.
func (s *Server) serverByID(id string) ServiceNode {
	if n := s.Manager.NodeByID(id); n != nil {
		return n
	}
	return nil
}

// serverRef names a server and where its pages and forms are.
type serverRef struct {
	ID, Name string
	// This is the server serving the UI.
	This bool
}

func (s *Server) serverRefOf(r *http.Request) serverRef {
	n := s.serverOf(r)
	if n == nil || s.Node == nil {
		return serverRef{This: true}
	}
	// A requested server is named as the lab lists it.
	if id := cmp.Or(r.PathValue("server"), r.URL.Query().Get("server")); id != "" {
		if ms, err := s.Manager.Servers(); err == nil {
			for _, m := range ms {
				if m.ID == id {
					return serverRef{ID: m.ID, Name: m.Name, This: m.This}
				}
			}
		}
	}
	id := n.Identity()
	return serverRef{ID: id.ID, Name: id.Name, This: id.ID == s.Node.Identity().ID}
}

// Label is how pages refer to the server.
func (m serverRef) Label() string {
	if m.This || m.Name == "" {
		return "This server"
	}
	return m.Name
}

// InSentence is how a sentence refers to the server after its first
// word: "this server", or its name ("added a backup for this server").
// Label's capital belongs only at the start (found 2026-10-18).
func (m serverRef) InSentence() string {
	if m.This || m.Name == "" {
		return "this server"
	}
	return m.Name
}

func (m serverRef) path(this, other string) string {
	if m.This {
		return this
	}
	return "/settings/servers/" + m.ID + other
}

func (m serverRef) Updates() string    { return m.path("/settings/updates", "/updates") }
func (m serverRef) UpdatesRun() string { return m.path("/settings/updates/run", "/updates/run") }
func (m serverRef) SeriesAccept() string {
	return m.path("/settings/updates/series", "/updates/series")
}
func (m serverRef) HostChangesKnown() string {
	return m.path("/settings/host-changes/known", "/host-changes/known")
}
func (m serverRef) Restart() string { return m.path("/settings/restart", "/restart") }
func (m serverRef) RestartPolicy() string {
	return m.path("/settings/restart-policy", "/restart-policy")
}
func (m serverRef) TimeZone() string     { return m.path("/settings/time-zone", "/time-zone") }
func (m serverRef) TimeZonePost() string { return m.path("/setup/timezone", "/time-zone") }
func (m serverRef) RemoteAccess() string {
	return m.path("/settings/remote-access", "/remote-access")
}
func (m serverRef) RemoteAccessPost() string {
	return m.path("/setup/ssh", "/remote-access")
}
func (m serverRef) Backups() string   { return m.path("/backups", "/backups") }
func (m serverRef) BackupRun() string { return m.path("/backup/run", "/backups/run") }

// serverRoutes serves this server's settings pages for other servers.
func (s *Server) serverRoutes(mux *http.ServeMux) {
	const p = "/settings/servers/{server}"
	for route, h := range map[string]http.HandlerFunc{
		"GET " + p + "/updates":               s.updatesPage,
		"POST " + p + "/updates/run":          s.updateNowSubmit,
		"POST " + p + "/updates/series":       s.seriesAcceptSubmit,
		"POST " + p + "/host-changes/known":   s.hostChangesKnownSubmit,
		"POST " + p + "/restart":              s.restartNowSubmit,
		"POST " + p + "/restart-policy":       s.restartPolicySubmit,
		"GET " + p + "/time-zone":             s.timezonePage,
		"POST " + p + "/time-zone":            s.timezoneSubmit,
		"GET " + p + "/remote-access":         s.sshPage,
		"POST " + p + "/remote-access":        s.sshSubmit,
		"GET " + p + "/backups":               s.backupsPage,
		"POST " + p + "/backups/run":          s.backupRun,
		"POST " + p + "/backups/plan":         s.planAddSubmit(false),
		"POST " + p + "/backups/plan/promote": s.planPromoteSubmit,
		"POST " + p + "/backups/plan/remove":  s.planRemoveSubmit,
		"POST " + p + "/backups/plan/key":     s.planKeySubmit,
		// Rolling a backup back (SPEC.md §10.10).
		"POST " + p + "/backups/plan/rollback": s.planRollbackSubmit,
	} {
		mux.Handle(route, s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
			n := s.serverOf(r)
			if n == nil {
				http.NotFound(w, r)
				return
			}
			// Another server that doesn't answer (often: restarting to apply
			// a setting just changed): say so, and come back by itself. Its
			// Backups page has its own way of saying it.
			if m := s.serverRefOf(r); !m.This && n.Settings() == nil && !strings.HasSuffix(route, p+"/backups") {
				s.renderUnreachable(w, r, m)
				return
			}
			h(w, r)
		}))
	}
	// Polled by that page: answers once the server does.
	mux.Handle("GET "+p+"/reachable", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		n := s.serverOf(r)
		if n == nil {
			http.NotFound(w, r)
			return
		}
		if n.Settings() == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("HX-Refresh", "true")
	}))
}

// renderUnreachable is a server's settings page while it doesn't answer.
func (s *Server) renderUnreachable(w http.ResponseWriter, r *http.Request, m serverRef) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Server"] = m
	w.WriteHeader(http.StatusServiceUnavailable)
	s.render(w, "settings-server-unreachable", data)
}

var _ ServiceNode = node.Node(nil)

// serverView is a server's card on the home page.
type serverView struct {
	manager.Server
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
	servers, err := s.Manager.Servers()
	if err != nil {
		return nil
	}
	out := make([]serverView, len(servers))
	var wg sync.WaitGroup
	for i, mc := range servers {
		wg.Go(func() {
			v := serverView{Server: mc, Reachable: mc.This || mc.Status.LastErr == nil ||
				!strings.HasPrefix(mc.Status.LastErr.Error(), "Roostack can't reach")}
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
					if rs, err := u.RestartStatus(r.Context()); err == nil {
						v.RestartNeeded = rs.Needed
					}
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
