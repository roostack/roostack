package web

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/manager"
)

// Storage for other labs (SPEC.md §10.10): this lab keeps other labs'
// encrypted backups (Settings → Storage for others), and stores its own
// at other labs (the Backups page).

// guestsEndpointKey remembers the address other labs reach this one at.
const guestsEndpointKey = "guests.endpoint"

func (s *Server) storagePage(w http.ResponseWriter, r *http.Request) {
	s.renderStorage(w, r, nil)
}

func (s *Server) renderStorage(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	guests, err := s.Manager.StorageGuests(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	left, _ := s.Manager.LeftoverGuests(r.Context())
	endpoint, _ := s.Store.Setting(r.Context(), guestsEndpointKey)
	if endpoint == "" {
		if host, _, err := net.SplitHostPort(defaultEndpoint()); err == nil {
			endpoint = net.JoinHostPort(host, strconv.Itoa(agent.GuestsPort))
		}
	}
	data["Guests"], data["Leftover"], data["Endpoint"] = guests, left, endpoint
	data["Offering"] = s.Manager.OfferStorage(r.Context())
	if host, _, err := net.SplitHostPort(defaultEndpoint()); err == nil {
		data["LANAddr"] = host
	}
	data["Now"] = time.Now()
	for k, v := range extra {
		data[k] = v
	}
	if extra["Error"] != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "settings-storage", withArea(data, "storage"))
}

// gigabytes reads a whole number of GB from a form.
func gigabytes(v string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n << 30
}

func (s *Server) inviteGuestSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name, endpoint := r.FormValue("name"), strings.TrimSpace(r.FormValue("endpoint"))
	inv, err := s.Manager.InviteGuest(r.Context(), name, gigabytes(r.FormValue("quota")), endpoint)
	if msg, ok := userMessage(err); ok {
		s.renderStorage(w, r, map[string]any{"Error": msg, "Endpoint": endpoint, "Name": name, "Quota": r.FormValue("quota")})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.SetSetting(r.Context(), guestsEndpointKey, endpoint)
	s.Store.Audit(r.Context(), currentUser(r), "storage", "invited "+strings.TrimSpace(name))
	s.renderStorage(w, r, map[string]any{"Invite": inv.String(), "InviteName": strings.TrimSpace(name),
		"InviteExpires": time.Now().Add(labnet.InviteLifetime), "Endpoint": endpoint})
}

func (s *Server) guestID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Server) removeGuestSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Manager.RemoveGuest(r.Context(), s.guestID(r)); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "storage", "removed guest "+r.PathValue("id"))
	redirect(w, r, "/settings/storage")
}

func (s *Server) guestQuotaSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	err := s.Manager.SetGuestQuota(r.Context(), s.guestID(r), gigabytes(r.FormValue("quota")))
	if msg, ok := userMessage(err); ok {
		s.renderStorage(w, r, map[string]any{"Error": msg})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "storage", "changed guest "+r.PathValue("id")+"'s space")
	redirect(w, r, "/settings/storage")
}

func (s *Server) deleteLeftoverSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	err := s.Manager.DeleteLeftoverGuest(r.Context(), r.PathValue("user"))
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "storage", "deleted a removed guest's data ("+r.PathValue("user")+")")
	redirect(w, r, "/settings/storage")
}

func (s *Server) addRemoteStorageSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	err := s.Manager.AddRemoteStorage(r.Context(), r.FormValue("invite"))
	if msg, ok := userMessage(err); ok {
		data, derr := s.pageData(r)
		if derr != nil {
			s.serverError(w, derr)
			return
		}
		data["RemoteError"], data["Error"] = msg, msg
		s.renderBackups(w, r, data)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "storage", "added storage at another lab")
	redirect(w, r, "/backups#destination")
}

// remoteView is this lab's storage at another lab, for the Backups page.
type remoteView struct {
	manager.RemoteStorage
	Usage labnet.GuestUsage
	// Reachable: the other lab answered just now.
	Reachable bool
}

func (s *Server) remoteViews(r *http.Request) []remoteView {
	var out []remoteView
	for _, rs := range s.Manager.RemoteStorages(r.Context()) {
		v := remoteView{RemoteStorage: rs}
		if u, err := s.Manager.RemoteUsage(r.Context(), rs); err == nil {
			v.Usage, v.Reachable = u, true
		}
		out = append(out, v)
	}
	return out
}
