package web

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/trust"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// The lab's VPN (SPEC.md §10.11): each person's devices on Your account →
// VPN devices, and the Owner's settings and everyone's devices on Settings
// → VPN. Roostack makes each device's key pair when it's added, shows its
// settings once (a QR code and a file), and keeps only the public key
// (the maintainer, 2026-11-11: my.<domain> allows no scripts, so the
// browser can't make it).

// VPNManager is the lab's VPN (the manager).
type VPNManager interface {
	VPN() config.VPN
	SetVPN(v config.VPN) error
	// VPNChanged applies a change to the devices, or to who may use what.
	VPNChanged(reason string)
	VPNWay(ctx context.Context) manager.VPNWay
}

// vpnOn reports whether the lab's VPN is on.
func (s *Server) vpnOn() bool { return s.VPN != nil && s.VPN.VPN().On }

// vpnChanged applies a change that alters what devices reach: someone's
// role, groups or devices, or who may use a service.
func (s *Server) vpnChanged(reason string) {
	if s.VPN != nil {
		s.VPN.VPNChanged(reason)
	}
}

// vpnStaleAfter is how long a device can go without connecting before
// Settings → VPN points it out.
const vpnStaleAfter = 90 * 24 * time.Hour

// acctDevices is the VPN devices tab.
type acctDevices struct {
	List []deviceRow
	// Limit is each person's limit (0: none), and Left how many more this
	// person can add.
	Limit, Left int
	// Server is the server devices connect to, and Endpoint where.
	Server, Endpoint string
	// Form is the add or change form, Added a device's settings, shown once.
	Form  *deviceForm
	Added *deviceAdded
}

// deviceRow is one device in a list.
type deviceRow struct {
	ID                    int64
	Name, Person, Reaches string
	LastConnected         string
	// Stale: it hasn't connected for vpnStaleAfter; Never: it never has.
	Stale, Never bool
}

// deviceForm adds or changes a device.
type deviceForm struct {
	ID          int64 // 0: adding
	Name, Reach string
	Pages, SSH  bool
	// What this person may choose: Roostack's pages (Owners and Admins),
	// SSH (Owners), and what the Owner allows.
	CanPages, CanSSH, Home, Everything bool
	// NeedCode: they have two-factor, so it's asked for again.
	NeedCode bool
}

// deviceAdded is a device's settings, shown once.
type deviceAdded struct {
	Name, FileName string
	QR             template.HTML
	// File is the settings, and Download a link that saves them.
	File     string
	Download template.URL
}

// reachText says what a device reaches.
func reachText(d store.VPNDevice) string {
	t := map[string]string{store.ReachLab: "The lab only", store.ReachHome: "The lab and the home network",
		store.ReachEverything: "Everything"}[d.Reach]
	var also []string
	if d.Pages {
		also = append(also, "Roostack's pages")
	}
	if d.SSH {
		also = append(also, "SSH")
	}
	if len(also) > 0 {
		t += ", with " + words.And(also)
	}
	return t
}

// deviceRows lists devices with when each last connected.
func deviceRows(list []store.VPNDevice, way manager.VPNWay, now time.Time) []deviceRow {
	var rows []deviceRow
	for _, d := range list {
		r := deviceRow{ID: d.ID, Name: d.Name, Person: d.Username, Reaches: reachText(d)}
		t, ok := way.LastConnected[d.PublicKey]
		switch {
		case !ok:
			r.Never, r.LastConnected = true, "Never"
		default:
			r.LastConnected = words.Capitalise(words.When(t))
		}
		since := d.Created
		if ok {
			since = t
		}
		r.Stale = now.Sub(since) > vpnStaleAfter
		rows = append(rows, r)
	}
	return rows
}

// acctDevicesView is the tab's list.
func (s *Server) acctDevicesView(r *http.Request, a *acct) (acctView, error) {
	list, err := s.Store.UserVPNDevices(r.Context(), a.User.ID)
	if err != nil {
		return acctView{}, err
	}
	way := s.VPN.VPNWay(r.Context())
	d := &acctDevices{List: deviceRows(list, way, time.Now()), Limit: s.VPN.VPN().Limit(), Server: way.Server, Endpoint: way.Endpoint}
	if d.Limit > 0 {
		d.Left = max(0, d.Limit-len(list))
	}
	return acctView{Tab: "devices", Title: "VPN devices", Devices: d}, nil
}

// acctDevicesPage is Your account → VPN devices.
func (s *Server) acctDevicesPage(w http.ResponseWriter, r *http.Request, a *acct) {
	if !s.vpnOn() {
		http.NotFound(w, r)
		return
	}
	v, err := s.acctDevicesView(r, a)
	if err != nil {
		s.serverError(w, err)
		return
	}
	switch r.FormValue("done") {
	case "removed":
		v.Done = "Removed. That device can't connect any more."
	case "changed":
		v.Done = "Saved."
	}
	s.renderAcct(w, r, a, http.StatusOK, v)
}

// newDeviceForm is the form's choices for a.
func (s *Server) newDeviceForm(a *acct) *deviceForm {
	v := s.VPN.VPN()
	return &deviceForm{Reach: store.ReachLab, CanPages: a.User.Role == auth.RoleOwner || a.User.Role == auth.RoleAdmin,
		CanSSH: a.User.Role == auth.RoleOwner, Home: v.HomeNetwork, Everything: v.Everything, NeedCode: a.User.TOTPSecret != ""}
}

// acctDeviceAdd shows the form to add a device.
func (s *Server) acctDeviceAdd(w http.ResponseWriter, r *http.Request, a *acct) {
	if !s.vpnOn() {
		http.NotFound(w, r)
		return
	}
	v, err := s.acctDevicesView(r, a)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if v.Devices.Limit > 0 && v.Devices.Left == 0 {
		v.Error = fmt.Sprintf("You have %d devices, as many as the lab allows each person. Remove one to add another, or ask %s for more.",
			v.Devices.Limit, s.ownerName(r.Context()))
		s.renderAcct(w, r, a, http.StatusOK, v)
		return
	}
	v.Title, v.Devices.Form = "Add a device", s.newDeviceForm(a)
	s.renderAcct(w, r, a, http.StatusOK, v)
}

// readDeviceForm reads and checks the form, keeping what a may choose,
// then (if the rest is fine and ready says nothing's wrong) the two-factor
// code, which is used up once it's right.
func (s *Server) readDeviceForm(r *http.Request, a *acct, f *deviceForm, ready func() string) string {
	f.Name = strings.Join(strings.Fields(r.FormValue("name")), " ")
	f.Reach = r.FormValue("reach")
	f.Pages = f.CanPages && r.FormValue("pages") != ""
	f.SSH = f.CanSSH && r.FormValue("ssh") != ""
	switch {
	case f.Name == "":
		return "Give the device a name, such as \"Alex's phone\"."
	case utf8.RuneCountInString(f.Name) > 60:
		return "The name can be at most 60 characters."
	case f.Reach != store.ReachLab && !(f.Reach == store.ReachHome && f.Home) && !(f.Reach == store.ReachEverything && f.Everything):
		return "Choose what the device should reach."
	}
	if msg := ready(); msg != "" {
		return msg
	}
	if f.NeedCode {
		res, until, err := s.checkCode(r.Context(), a.User.ID, strings.ReplaceAll(r.FormValue("code"), " ", ""))
		switch {
		case err != nil:
			return "Roostack couldn't check the code: " + err.Error()
		case res == codeLocked:
			return lockedMessage(until)
		case res == codeWrong:
			return "That code isn't right. Enter the 6-digit code your authenticator app shows now."
		}
	}
	return ""
}

// acctDeviceSubmit adds a device, and shows its settings once.
func (s *Server) acctDeviceSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	if !s.vpnOn() {
		http.NotFound(w, r)
		return
	}
	f := s.newDeviceForm(a)
	if msg := s.readDeviceForm(r, a, f, func() string { return s.vpnNotReady(r.Context()) }); msg != "" {
		s.renderDeviceForm(w, r, a, "Add a device", f, msg)
		return
	}
	private, public, err := newDeviceKey()
	if err != nil {
		s.serverError(w, err)
		return
	}
	d, err := s.Store.AddVPNDevice(r.Context(), store.VPNDevice{UserID: a.User.ID, Name: f.Name, PublicKey: public, Reach: f.Reach,
		Pages: f.Pages, SSH: f.SSH}, s.VPN.VPN().Limit())
	if errors.Is(err, store.ErrDeviceLimit) {
		s.renderDeviceForm(w, r, a, "Add a device", f, "That's as many devices as the lab allows each person. Remove one to add another.")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	d.Username, d.Role = a.User.Username, a.User.Role
	s.Store.Audit(r.Context(), a.User, attention.AuditVPNDeviceAdded, d.Name+", reaching "+lowerFirst(reachText(d)))
	s.VPN.VPNChanged("A device was added to the lab's VPN")
	s.showDeviceSettings(w, r, a, d, private, d.Name+" is ready to add")
}

// acctDeviceChange shows the form to change a device.
func (s *Server) acctDeviceChange(w http.ResponseWriter, r *http.Request, a *acct) {
	d, ok := s.ownDevice(w, r, a)
	if !ok {
		return
	}
	f := s.newDeviceForm(a)
	f.ID, f.Name, f.Reach, f.Pages, f.SSH = d.ID, d.Name, d.Reach, d.Pages, d.SSH
	s.renderDeviceForm(w, r, a, "Change "+d.Name, f, "")
}

// acctDeviceChangeSubmit saves a device: Roostack's pages and SSH change
// at once; changing what it reaches gives it new settings with a new key
// (SPEC.md §10.11).
func (s *Server) acctDeviceChangeSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	d, ok := s.ownDevice(w, r, a)
	if !ok {
		return
	}
	f := s.newDeviceForm(a)
	f.ID = d.ID
	ready := func() string {
		if f.Reach != d.Reach || r.FormValue("new") != "" {
			return s.vpnNotReady(r.Context())
		}
		return ""
	}
	if msg := s.readDeviceForm(r, a, f, ready); msg != "" {
		s.renderDeviceForm(w, r, a, "Change "+d.Name, f, msg)
		return
	}
	newKey := f.Reach != d.Reach || r.FormValue("new") != ""
	var private string
	if newKey {
		var err error
		if private, d.PublicKey, err = newDeviceKey(); err != nil {
			s.serverError(w, err)
			return
		}
	}
	d.Name, d.Reach, d.Pages, d.SSH = f.Name, f.Reach, f.Pages, f.SSH
	if err := s.Store.SetVPNDevice(r.Context(), d); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), a.User, attention.AuditVPNDeviceChanged, d.Name+", now reaching "+lowerFirst(reachText(d)))
	s.VPN.VPNChanged("A device on the lab's VPN changed")
	if newKey {
		s.showDeviceSettings(w, r, a, d, private, d.Name+"'s new settings")
		return
	}
	redirect(w, r, "/account/devices?done=changed")
}

// acctDeviceRemove removes one of a's devices.
func (s *Server) acctDeviceRemove(w http.ResponseWriter, r *http.Request, a *acct) {
	d, ok := s.ownDevice(w, r, a)
	if !ok {
		return
	}
	if err := s.removeDevice(r.Context(), a.User, d); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/account/devices?done=removed")
}

// removeDevice removes a device, and applies it.
func (s *Server) removeDevice(ctx context.Context, by *store.User, d store.VPNDevice) error {
	if err := s.Store.DeleteVPNDevice(ctx, d.ID); err != nil {
		return err
	}
	detail := d.Name
	if by == nil || by.ID != d.UserID {
		detail += ", " + d.Username + "'s"
	}
	s.Store.Audit(ctx, by, attention.AuditVPNDeviceRemoved, detail)
	s.VPN.VPNChanged("A device was removed from the lab's VPN")
	return nil
}

// ownDevice is the device in the path, if it's a's.
func (s *Server) ownDevice(w http.ResponseWriter, r *http.Request, a *acct) (store.VPNDevice, bool) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := s.Store.VPNDevice(r.Context(), id)
	if !s.vpnOn() || err != nil || d.UserID != a.User.ID {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.serverError(w, err)
		} else {
			http.NotFound(w, r)
		}
		return d, false
	}
	return d, true
}

// renderDeviceForm shows the add or change form, with what went wrong.
func (s *Server) renderDeviceForm(w http.ResponseWriter, r *http.Request, a *acct, title string, f *deviceForm, msg string) {
	v, err := s.acctDevicesView(r, a)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.Title, v.Devices.Form, v.Error = title, f, msg
	status := http.StatusOK
	if msg != "" {
		status = http.StatusBadRequest
	}
	s.renderAcct(w, r, a, status, v)
}

// showDeviceSettings shows a device's settings, once: they hold its
// private key, which Roostack doesn't keep.
func (s *Server) showDeviceSettings(w http.ResponseWriter, r *http.Request, a *acct, d store.VPNDevice, private, title string) {
	v, err := s.acctDevicesView(r, a)
	if err != nil {
		s.serverError(w, err)
		return
	}
	file, err := s.deviceConfig(r.Context(), d, private)
	if err != nil {
		s.serverError(w, err)
		return
	}
	qr, err := qrSVGLabel(file, "QR code with "+d.Name+"'s settings for the WireGuard app")
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.Title = title
	v.Devices.Added = &deviceAdded{Name: d.Name, FileName: deviceFileName(d.Name), QR: qr, File: file,
		Download: template.URL("data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(file)))}
	s.renderAcct(w, r, a, http.StatusOK, v)
}

// deviceConfig is a device's settings, a WireGuard configuration: its
// key and address, and the server it connects to, with what it sends
// through it (AllowedIPs, SPEC.md §10.11). The lab's names point at one
// address, so the lab only is just that address.
func (s *Server) deviceConfig(ctx context.Context, d store.VPNDevice, private string) (string, error) {
	way := s.VPN.VPNWay(ctx)
	lab := s.labAddress(way)
	if way.PublicKey == "" || lab == "" {
		return "", errors.New("the lab's VPN isn't ready")
	}
	allowed := []string{lab + "/32"}
	switch d.Reach {
	case store.ReachHome:
		allowed = append(allowed, way.HomeNetworks...)
	case store.ReachEverything:
		allowed = []string{"0.0.0.0/0"}
	}
	if d.SSH && d.Reach != store.ReachEverything {
		// The lab's servers, at their addresses on Roostack's network.
		allowed = append(allowed, trust.ServersRange.String())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s on %s's VPN, made by Roostack\n", d.Name, s.labName())
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n", private, d.Address)
	fmt.Fprintf(&b, "[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = 25\n",
		way.PublicKey, way.Endpoint, strings.Join(allowed, ", "))
	return b.String(), nil
}

// labAddress is where the lab's names point (its wildcard record, as the
// DNS records manager last said, or the server's home address before it
// has): what a device on the lab only sends through the VPN.
func (s *Server) labAddress(way manager.VPNWay) string {
	if s.DNS != nil {
		if ip := s.DNS.Status().IP; ip != "" {
			return ip
		}
	}
	return way.HomeAddress
}

// vpnNotReady says why a device's settings can't be made yet, or "".
func (s *Server) vpnNotReady(ctx context.Context) string {
	if way := s.VPN.VPNWay(ctx); way.PublicKey == "" || s.labAddress(way) == "" {
		return "The lab's VPN is still starting. Try again in a minute."
	}
	return ""
}

// newDeviceKey makes a device's key pair.
func newDeviceKey() (private, public string, err error) {
	if private, err = agent.NewWireGuardKey(); err != nil {
		return "", "", err
	}
	public, err = agent.WireGuardPublicKey(private)
	return private, public, err
}

// deviceFileName is a device's settings file: WireGuard names the tunnel
// after it, and on Linux that's an interface name, at most 15 characters
// of letters, digits, and hyphens.
func deviceFileName(name string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b.WriteRune(c)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 15 {
		slug = strings.TrimRight(slug[:15], "-")
	}
	return cmp.Or(slug, "lab-vpn") + ".conf"
}

// lowerFirst is s with its first letter in lower case.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// settingsVPN is Settings → VPN: the Owner's settings, and everyone's
// devices.
type settingsVPN struct {
	VPN config.VPN
	Way manager.VPNWay
	// Limit is the form's limit ("" for none).
	Limit   string
	Devices []deviceRow
	People  int
	// Domain: the lab has an address, which devices connect to.
	Domain bool
	Saved  string
	Error  string
}

func (s *Server) settingsVPNData(r *http.Request) (map[string]any, *settingsVPN, error) {
	data := s.pageData(r)
	v := &settingsVPN{VPN: s.VPN.VPN(), Way: s.VPN.VPNWay(r.Context()), Domain: s.Domain != "", Saved: r.FormValue("saved")}
	if l := v.VPN.Limit(); l > 0 {
		v.Limit = strconv.Itoa(l)
	}
	list, err := s.Store.VPNDevices(r.Context())
	if err != nil {
		return nil, nil, err
	}
	v.Devices = deviceRows(list, v.Way, time.Now())
	people := map[int64]bool{}
	for _, d := range list {
		people[d.UserID] = true
	}
	v.People = len(people)
	data["VPN"], data["LabDomain"] = v, s.Domain
	return data, v, nil
}

// settingsVPNPage shows Settings → VPN.
func (s *Server) settingsVPNPage(w http.ResponseWriter, r *http.Request) {
	if s.VPN == nil {
		http.NotFound(w, r)
		return
	}
	data, _, err := s.settingsVPNData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "settings-vpn", withArea(data, "vpn"))
}

// settingsVPNSubmit saves the Owner's settings.
func (s *Server) settingsVPNSubmit(w http.ResponseWriter, r *http.Request) {
	if s.VPN == nil {
		http.NotFound(w, r)
		return
	}
	v := s.VPN.VPN()
	v.On = r.FormValue("on") != ""
	if r.Form.Has("server") {
		v.Server = r.FormValue("server")
	}
	v.HomeNetwork, v.Everything = r.FormValue("home") != "", r.FormValue("everything") != ""
	problem := ""
	switch limit := strings.TrimSpace(r.FormValue("limit")); {
	case limit == "":
		v.NoLimit, v.MaxDevices = true, 0
	default:
		n, err := strconv.Atoi(limit)
		if err != nil || n < 1 || n > 1000 {
			problem = "Enter how many devices each person can add, from 1 to 1,000, or leave it empty for no limit."
		}
		v.NoLimit, v.MaxDevices = false, n
	}
	if v.On && s.Domain == "" {
		problem = "The lab's VPN needs your lab to have an address first: devices connect to it."
	}
	if problem == "" {
		if err := s.VPN.SetVPN(v); err != nil {
			msg, ok := usererr.Message(err)
			if !ok {
				s.serverError(w, err)
				return
			}
			problem = msg
		}
	}
	if problem != "" {
		data, sv, err := s.settingsVPNData(r)
		if err != nil {
			s.serverError(w, err)
			return
		}
		sv.Error = problem
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "settings-vpn", withArea(data, "vpn"))
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", map[bool]string{true: "the lab's VPN is on", false: "the lab's VPN is off"}[v.On])
	if s.DNS != nil {
		// link.<domain>, where devices connect, needs its record.
		s.DNS.Refresh()
	}
	redirect(w, r, "/settings/vpn?saved=settings")
}

// settingsVPNRemove removes anyone's device.
func (s *Server) settingsVPNRemove(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := s.Store.VPNDevice(r.Context(), id)
	if s.VPN == nil || errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err == nil {
		err = s.removeDevice(r.Context(), currentUser(r), d)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/settings/vpn?saved=removed#devices")
}
