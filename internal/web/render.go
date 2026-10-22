package web

import (
	"bytes"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/store"
)

// Sending pages: a whole page (render), a part of one for htmx
// (renderPart, renderLive), a redirect, and an error.

func (s *Server) render(w http.ResponseWriter, page string, data map[string]any) {
	if u, ok := data["User"].(*store.User); ok && u != nil {
		n := navFor(u, page)
		n.Monitoring = s.Monitor != nil
		// A page shared by two places says which it's in now (adding a
		// location for migrating photos is the media library form, under
		// Services).
		if tab, ok := data["NavActive"].(string); ok {
			n.Active = tab
		}
		data["Nav"] = n
		// Every page's footer shows Roostack's version.
		if _, set := data["Version"]; !set {
			data["Version"] = s.Version
		}
		if _, set := data["LabName"]; !set {
			data["LabName"] = s.labName()
		}
		// For the banner shown when the browser's clock differs from the
		// host's (every time Roostack shows is host time).
		// Not while changing it, nor while restarting to use a new one (the
		// old zone is still in effect), nor on the wizard's step before
		// the one that asks it.
		if page != "setup-timezone" && page != "restarting" && !(page == "twofactor" && data["Wizard"] == true) {
			data["Clock"] = clockView{hosttime.Now(), auth.Can(u.Role, auth.ManageHost)}
		}
	}
	// The setup wizard's steps show only themselves, and how far along
	// they are (wizardSteps).
	if data["Wizard"] == true {
		data["Step"], data["Steps"] = wizardSteps[page], wizardStepCount
	}
	// While you're in Settings, the sidebar lists every settings page
	// under it (without Roostack Cloud while it isn't in use); an area
	// page marks its own.
	if n, ok := data["Nav"].(nav); ok && n.Settings {
		inSettings := n.Active == "settings"
		if inSettings {
			for _, g := range s.settingsGroups() {
				for _, a := range g.Areas {
					if a.ID != "servers" && a.ID != "backups" { // main areas
						n.SettingsAreas = append(n.SettingsAreas, a)
					}
				}
			}
		}
		if an, ok := data["AreaNav"].(areaNav); ok && an.Label == "Settings" && inSettings {
			n.Area = an.Active
		}
		if servers, err := s.Manager.Servers(); err == nil {
			for _, mc := range servers {
				if mc.Status.LastErr != nil {
					n.ServerIssues++
				}
			}
		}
		// What Home found lately counts too, while it's fresh.
		s.issues.mu.Lock()
		if time.Since(s.issues.at) < serverIssuesFresh {
			n.ServerIssues = max(n.ServerIssues, s.issues.n)
		}
		s.issues.mu.Unlock()
		data["Nav"] = n
	}
	// Under Services, the sidebar lists them, for those who can open them.
	if n, ok := data["Nav"].(nav); ok && n.Active == "services" && n.Backups {
		current, _ := data["Service"].(ServiceView)
		n.InService = current.Name != ""
		services, ok := data["Services"].([]ServiceView)
		if p, lazy := data[pageServicesKey].(*pageServices); !ok && lazy {
			// A page in Services that doesn't list them itself.
			services, _ = p.get()
		}
		for _, sv := range services {
			n.Services = append(n.Services, navLink{Href: "/services/" + sv.Name, Title: sv.Title,
				Logo: sv.IconURL, Icon: sv.DrawnIcon, Current: sv.Name == current.Name})
		}
		data["Nav"] = n
	}
	// Render into a buffer first, so a template error becomes an error
	// page rather than half a page sent with 200 OK.
	var buf bytes.Buffer
	if err := s.pages[page].Execute(&buf, data); err != nil {
		s.Log.Error("render", "page", page, "err", err)
		http.Error(w, "Something went wrong showing this page. Details are in the Roostack log.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// redirect sends the browser elsewhere. For htmx background requests (like
// the dashboard's live refresh) a normal redirect would be followed
// silently and the other page's HTML swapped into the current page, so htmx
// is told to navigate the whole page instead. On the setup listener, the
// redirect after setup's last step is the page saying where Roostack is
// from now on (finishedOnSetupListener).
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if finishedOnSetupListener(w, r) {
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	http.Error(w, "Something went wrong. Details are in the Roostack log.", http.StatusInternalServerError)
}

// renderPart renders one named template from a page (htmx partials). Like
// render, it renders into a buffer first, so a template error becomes an
// error rather than half a part sent with 200 OK.
func (s *Server) renderPart(w http.ResponseWriter, page, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, name, data); err != nil {
		s.Log.Error("render partial", "page", page, "part", name, "err", strings.TrimSpace(err.Error()))
		http.Error(w, "Something went wrong showing this part of the page; the details are in the Roostack log.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// Parts of pages that refresh themselves send a fingerprint of what they
// show (static/app.js); when it's still the same, the answer is 204 No
// Content and htmx leaves the page alone: nothing to download or redraw,
// and open panels, selections and scroll positions are untouched.
const (
	haveHeader        = "X-Roostack-Have"
	fingerprintHeader = "X-Roostack-Fingerprint"
)

// renderLive renders a self-refreshing part, or 204 if the page already
// shows exactly this.
func (s *Server) renderLive(w http.ResponseWriter, r *http.Request, page, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, name, data); err != nil {
		s.Log.Error("render partial", "page", page, "part", name, "err", strings.TrimSpace(err.Error()))
		return
	}
	h := fnv.New64a()
	h.Write(buf.Bytes())
	fp := strconv.FormatUint(h.Sum64(), 36)
	w.Header().Set(fingerprintHeader, fp)
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(haveHeader) == fp {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}
