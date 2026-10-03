package web

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/units"
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
	hostID := s.Manager.GuestServer(r.Context())
	hostsHere := s.Node == nil || hostID == s.Node.Identity().ID
	if servers, err := s.Manager.Servers(); err == nil {
		for _, sv := range servers {
			if sv.ID == hostID {
				data["HostServer"] = sv
			}
		}
		if len(servers) > 1 {
			data["ServerChoices"] = servers
		}
	}
	if endpoint == "" && hostsHere {
		endpoint = s.publicEndpoint()
	}
	var active, removed []manager.GuestView
	for _, g := range guests {
		if g.Removed.IsZero() {
			active = append(active, g)
		} else {
			removed = append(removed, g)
		}
	}
	data["Guests"], data["Removed"], data["Leftover"], data["Endpoint"] = active, removed, left, endpoint
	data["Offering"] = s.Manager.OfferStorage(r.Context())
	data["Internet"] = hostsHere && s.onInternet()
	data["Quota"] = "50"
	if sp, ok := s.Manager.GuestSpaceLeft(r.Context()); ok {
		data["Space"] = sp
		// Never offer more than there is by default.
		quota := max(min(50, sp.Left/units.GB), 1)
		data["Quota"] = strconv.FormatInt(quota, 10)
		// Free space left for the lab's own use after offering it (shown
		// before the form's script updates it as the number changes).
		data["SpaceAfter"] = max(sp.Free-sp.Promised-quota*units.GB, 0)
	}
	if host, _, err := net.SplitHostPort(defaultEndpoint()); err == nil && hostsHere {
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

// publicEndpoint is the address other labs reach this server's guests
// link at, if Roostack knows it: on a lab on the internet its public
// address, at home the home's public address, when DNS records know it
// (SPEC.md §10.10). Otherwise "" and the Owner types it.
func (s *Server) publicEndpoint() string {
	if s.DNS == nil {
		return ""
	}
	st := s.DNS.Status()
	ip := st.PublicIP
	if st.Internet {
		ip = st.IP
	}
	if addr := net.ParseIP(ip); addr == nil || addr.IsPrivate() || addr.IsLoopback() {
		return ""
	}
	return net.JoinHostPort(ip, strconv.Itoa(agent.GuestsPort))
}

// gigabytes reads a whole number of GB from a form.
func gigabytes(v string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n * units.GB
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

func (s *Server) guestServerSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	err := s.Manager.SetGuestServer(r.Context(), r.FormValue("server"))
	if msg, ok := userMessage(err); ok {
		s.renderStorage(w, r, map[string]any{"Error": msg})
		return
	}
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "storage", "storage for others offered by "+r.FormValue("server"))
	redirect(w, r, "/settings/storage")
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

func (s *Server) deleteRemovedGuestSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	err := s.Manager.DeleteRemovedGuest(r.Context(), s.guestID(r))
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "storage", "deleted removed guest "+r.PathValue("id")+"'s backups")
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

// remoteView is this lab's storage at another lab, for the Backups page.
type remoteView struct {
	manager.RemoteStorage
	Usage labnet.GuestUsage
	// Reachable: the other lab answered just now.
	Reachable bool
	// UsedBy names the servers backing up there.
	UsedBy []string
}

func (s *Server) remoteViews(r *http.Request) []remoteView {
	servers, _ := s.Manager.Servers()
	var out []remoteView
	for _, rs := range s.Manager.RemoteStorages(r.Context()) {
		v := remoteView{RemoteStorage: rs}
		for _, sv := range servers {
			uses := false
			if t, _, ok := s.Manager.BackupTargetOf(r.Context(), sv.ID); ok && t.Provider == rs.Ref {
				uses = true
			}
			for _, c := range s.Manager.BackupCopiesOf(r.Context(), sv.ID) {
				uses = uses || c.Ref == rs.Ref
			}
			if uses {
				v.UsedBy = append(v.UsedBy, sv.Name)
			}
		}
		if u, err := s.Manager.RemoteUsage(r.Context(), rs); err == nil {
			v.Usage, v.Reachable = u, true
		}
		out = append(out, v)
	}
	return out
}
