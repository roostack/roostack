package web

import (
	"cmp"
	"net/http"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/usererr"
)

// The Updates page (SPEC.md §7.1, "The Updates page"): every service with
// an update, to choose several and update them now or tonight.

// updateRow is a service with an update, as the Updates page lists it.
type updateRow struct {
	*manager.Update
	Service ServiceView
	// Server is where it runs, shown in a lab of more than one.
	Server string
	// Updating is its update while it's queued or running.
	Updating *manager.ServiceUpdate
	// Tonight is set when someone chose it for tonight.
	Tonight *manager.TonightUpdate
	// Why says why it can't be chosen ("" if it can), and OwnerOnly that
	// only the Owner can update it, from its page.
	Why       string
	OwnerOnly bool
	// Ticked: chosen when the page opens (fix releases and improved
	// packages that can be chosen, without notes).
	Ticked bool
}

// Choosable says whether the row can be ticked.
func (r updateRow) Choosable() bool {
	return r.Why == "" && !r.OwnerOnly && r.Updating == nil && r.Tonight == nil
}

// KindLabel is the update's kind in a few words, for its badge.
func (r updateRow) KindLabel() string {
	switch r.Kind {
	case pkgspec.UpdateRevision:
		return "Improved package"
	case pkgspec.UpdatePatch:
		return "Fix release"
	case pkgspec.UpdateMinor:
		return "Feature release"
	case pkgspec.UpdateMajor:
		return "New major version"
	}
	return "New version"
}

// ByItself says whether Roostack would apply it tonight anyway, and if
// not, why not.
func (r updateRow) ByItself() string {
	switch {
	case r.Auto:
		return "Yes, tonight after the backup"
	case r.Policy == "" || r.Policy == "notify":
		return "No: you chose to be told"
	case r.FailedBefore:
		return "No: it didn't work last time"
	case len(r.Notes) > 0:
		return "No: it has notes to read"
	case len(r.NewRisks) > 0:
		return "No: it waits for the Owner"
	case r.NoBackups:
		return "No: its server isn't backed up"
	case r.Blocked != "":
		return "No"
	case r.Policy == "patch":
		return "No: you chose fix releases only"
	case r.Policy == "minor":
		return "No: you chose fix and feature releases"
	}
	return "No"
}

// Step is what its update is doing now ("" before it starts).
func (r updateRow) Step() string {
	if r.Updating == nil || r.Updating.Waiting != "" {
		return ""
	}
	for _, s := range r.Updating.Progress.Steps {
		if !s.Started.IsZero() && s.Finished.IsZero() {
			return s.Title
		}
	}
	return ""
}

// updateRows lists the services with an update, by title.
func (s *Server) updateRows(r *http.Request, services []ServiceView) []updateRow {
	ctx := r.Context()
	avail := s.Manager.AvailableUpdates(ctx)
	tonight := s.Manager.UpdatesTonight(ctx)
	ms, _ := s.Manager.Servers()
	var rows []updateRow
	for _, sv := range services {
		u, su := avail[sv.Name], s.Manager.Updating(sv.Name)
		if u == nil && su != nil {
			// Its version has switched, but its update is still being
			// checked: it stays listed until it's done.
			u = &manager.Update{Service: sv.Name, Title: sv.Title, From: su.From, To: su.To, Kind: pkgspec.UpdateKind(su.From, su.To)}
		}
		if u == nil {
			continue
		}
		row := updateRow{Update: u, Service: sv, Updating: su}
		if len(ms) > 1 {
			row.Server = sv.Server
		}
		if t, ok := tonight[sv.Name]; ok {
			row.Tonight = &t
		}
		switch {
		case u.Blocked != "":
			row.Why = u.Blocked
		case len(u.NewRisks) > 0:
			row.OwnerOnly = true
		}
		row.Ticked = row.Choosable() && len(u.Notes) == 0 && (u.Kind == pkgspec.UpdatePatch || u.Kind == pkgspec.UpdateRevision)
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b updateRow) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)), strings.Compare(a.Service.Name, b.Service.Name))
	})
	return rows
}

// updatesRunning are the rows whose update is queued or running, the one
// running first.
func updatesRunning(rows []updateRow) []updateRow {
	var out []updateRow
	for _, row := range rows {
		if row.Updating != nil {
			out = append(out, row)
		}
	}
	slices.SortStableFunc(out, func(a, b updateRow) int {
		// The running one (not waiting) first, then by their place.
		return cmp.Or(cmp.Compare(waitRank(a), waitRank(b)), strings.Compare(a.Updating.Waiting, b.Updating.Waiting))
	})
	return out
}

func waitRank(r updateRow) int {
	if r.Updating.Waiting == "" {
		return 0
	}
	return 1
}

func updatingNames(rows []updateRow) string {
	var names []string
	for _, row := range updatesRunning(rows) {
		names = append(names, row.Service.Name)
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

// serviceUpdatesData is the Updates page's data, with msg as its error.
func (s *Server) serviceUpdatesData(w http.ResponseWriter, r *http.Request, msg string) (map[string]any, bool) {
	data, ok := s.pageDataWithServices(w, r)
	if !ok {
		return nil, false
	}
	rows := s.updateRows(r, data["Services"].([]ServiceView))
	data["Rows"], data["Running"], data["RunningNames"] = rows, updatesRunning(rows), updatingNames(rows)
	var tonight []string
	for _, row := range rows {
		if row.Tonight != nil {
			tonight = append(tonight, row.Title)
		}
	}
	data["TonightTitles"] = tonight
	if msg != "" {
		data["Error"] = msg
	}
	return data, true
}

func (s *Server) serviceUpdatesPage(w http.ResponseWriter, r *http.Request) {
	if data, ok := s.serviceUpdatesData(w, r, ""); ok {
		s.render(w, "services-updates", data)
	}
}

// serviceUpdatesLive is the page's notice of updates in progress, polled while
// they run; when one has finished or another started (the list differs
// from ?updating=), the page reloads for its new rows.
func (s *Server) serviceUpdatesLive(w http.ResponseWriter, r *http.Request) {
	data, ok := s.serviceUpdatesData(w, r, "")
	if !ok {
		return
	}
	if data["RunningNames"] != r.URL.Query().Get("updating") {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	s.renderLive(w, r, "services-updates", "updates-running", data)
}

// serviceUpdatesSubmit updates the chosen services now (when=now) or tonight.
func (s *Server) serviceUpdatesSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var choices []manager.UpdateChoice
	for _, name := range r.Form["update"] {
		choices = append(choices, manager.UpdateChoice{Service: name, To: r.Form.Get("to." + name), ReadNotes: r.Form.Get("read."+name) == "1"})
	}
	by := currentUser(r).Username
	tonight := r.Form.Get("when") == "tonight"
	var err error
	if tonight {
		err = s.Manager.UpdateTonight(r.Context(), choices, by)
	} else {
		err = s.Manager.UpdateServices(r.Context(), choices, by)
	}
	if msg, ok := usererr.Message(err); ok {
		if data, ok := s.serviceUpdatesData(w, r, msg); ok {
			w.WriteHeader(http.StatusBadRequest)
			s.render(w, "services-updates", data)
		}
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	var what []string
	for _, c := range choices {
		what = append(what, c.Service+" to "+c.To)
	}
	when := "now"
	if tonight {
		when = "tonight"
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", "update "+strings.Join(what, ", ")+" "+when)
	redirect(w, r, "/updates")
}

// serviceUpdatesCancelSubmit takes a service's update off tonight's.
func (s *Server) serviceUpdatesCancelSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("service")
	if err := s.Manager.CancelTonight(r.Context(), name); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", "cancel tonight's update of "+name)
	redirect(w, r, "/updates")
}
