package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/setup"
)

// requireWizard allows the setup wizard to the Owner.
func (s *Server) requireWizard(h http.HandlerFunc) http.Handler {
	return s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		if s.Setup == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	})
}

// requireOwner allows host settings (SPEC.md §5) to the Owner, once they
// have 2FA.
func (s *Server) requireOwner(h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if !auth.Can(u.Role, auth.ManageHost) {
			http.Error(w, "Only the Owner can change these settings.", http.StatusForbidden)
			return
		}
		if u.TOTPSecret == "" {
			redirect(w, r, "/account/2fa")
			return
		}
		if u.MustChangePassword {
			redirect(w, r, "/account")
			return
		}
		h(w, r)
	})
}

// healthz reports this process's boot ID. With ?since=<old boot ID> from the
// restarting page, it tells htmx to move on once a new process answers.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if since := r.URL.Query().Get("since"); since != "" && since != s.bootID {
		w.Header().Set("HX-Redirect", "/")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("ok"))
}

func (s *Server) restartingPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"User": currentUser(r), "BootID": s.bootID}
	if s.RunningJob != nil {
		data["Waiting"] = s.RunningJob()
	}
	s.render(w, "restarting", data)
}

// formMode says how a setup form is being used, for its wording:
//   - Wizard: the Owner is being walked through first-run setup ("step 1
//     of 2", "set this up later")
//   - otherwise it's a settings page: "Set up ..." if the step was
//     postponed, "Change ..." if it's done, with a back link instead.
func (s *Server) formMode(r *http.Request, step string, data map[string]any) map[string]any {
	p, err := s.Setup.Progress(r.Context())
	if err != nil {
		return data
	}
	done := map[string]bool{"timezone": p.TimezoneDone, "ssh": p.SSHDone, "domain": p.DomainDone, "backup": p.BackupDone}[step]
	postponed := map[string]bool{"domain": p.DomainLater, "backup": p.BackupLater}[step]
	data["Wizard"] = !done && !postponed
	data["StepDone"] = done
	if area := map[string]string{"timezone": "time-zone", "ssh": "remote-access", "domain": "domain"}[step]; area != "" && !data["Wizard"].(bool) {
		withArea(data, area)
	}
	data["Back"] = map[string]string{"timezone": "/settings", "ssh": "/settings", "domain": "/settings/domain", "backup": "/backups"}[step]
	m := s.serverRefOf(r)
	data["Server"], data["BackText"] = m, "Settings"
	if area := map[string]string{"timezone": "time-zone", "ssh": "remote-access"}[step]; area != "" && data["Wizard"] != true {
		data["Switcher"] = s.serverTabs(r, area)
	}
	if !m.This {
		// Another server's settings are never a wizard step.
		data["Wizard"], data["Back"], data["BackText"] = false, "/servers", "Servers"
	}
	return data
}

// clockOf is the clock of the server a request is about; only this
// server's is known.
func (s *Server) clockOf(r *http.Request) hosttime.Clock {
	if !s.serverRefOf(r).This {
		return hosttime.Clock{}
	}
	return hosttime.Now()
}

// clockView is the host's clock for the layout's time zone banner.
type clockView struct {
	hosttime.Clock
	Owner bool // may change the time zone
}

func (s *Server) timezonePage(w http.ResponseWriter, r *http.Request) {
	if s.server(r) == nil {
		http.NotFound(w, r)
		return
	}
	current, zones, err := s.server(r).Timezones()
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := s.formMode(r, "timezone", map[string]any{"User": currentUser(r),
		"Current": current, "Zone": current, "Zones": zones, "HostClock": s.clockOf(r)})
	s.addNetworkForm(r, data)
	s.render(w, "setup-timezone", data)
}

// addNetworkForm asks where the lab is on the wizard's first step, which is
// about where this server is (SPEC.md §6.12).
func (s *Server) addNetworkForm(r *http.Request, data map[string]any) {
	if data["Wizard"] == true && s.serverRefOf(r).This {
		data["NetworkForm"] = s.networkForm(r, true)
	}
}

func (s *Server) timezoneSubmit(w http.ResponseWriter, r *http.Request) {
	zone := r.FormValue("timezone")
	if s.server(r) == nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.Setup.Progress(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	m := s.serverRefOf(r)
	changed, err := s.server(r).SetTimezone(r.Context(), zone)
	if err == nil && m.This && s.inWizard(r) && r.FormValue("network") != "" {
		// Where the lab is, asked with it (its restart waits for the
		// wizard's end).
		network := r.FormValue("network")
		err = s.Setup.SetNetwork(network, network == config.NetworkInternet && r.FormValue("ui") == "tunnel")
		if err == nil {
			s.Store.Audit(r.Context(), currentUser(r), "setup", "lab is "+map[string]string{
				config.NetworkHome: "at home", config.NetworkInternet: "on the internet"}[network])
		}
	}
	if err == nil && m.This {
		// The setup wizard is about this server.
		err = s.Setup.Done(r.Context(), setup.StepTimezone)
	}
	if err != nil {
		current, zones, zerr := s.server(r).Timezones()
		if zerr != nil {
			s.serverError(w, zerr)
			return
		}
		data := s.formMode(r, "timezone", map[string]any{
			"Current": current, "Zone": zone, "Zones": zones, "HostClock": s.clockOf(r)})
		s.addNetworkForm(r, data)
		s.wizardError(w, r, err, "setup-timezone", data)
		return
	}
	if changed {
		s.Store.Audit(r.Context(), currentUser(r), "setup", m.Label()+" time zone set to "+zone)
	}
	switch {
	case !m.This:
		redirect(w, r, m.TimeZone())
	case !changed || s.inWizard(r):
		// On to the next wizard step (its restart waits for the wizard's
		// end), or back to Settings.
		redirect(w, r, map[bool]string{true: "/settings/time-zone", false: "/"}[p.TimezoneDone])
	default:
		redirect(w, r, "/restarting")
	}
}

// sshClient is a current SSH connection, and which choices would cut it off.
type sshClient struct {
	Addr     string
	LAN      bool
	CutByLAN bool
}

func (s *Server) sshData(r *http.Request, choice, errMsg string) (map[string]any, error) {
	if s.server(r) == nil {
		return nil, errNoServer
	}
	a, err := s.server(r).SSHAccess()
	if err != nil {
		return nil, err
	}
	var clients []sshClient
	cutByLAN := false
	for _, c := range a.Clients {
		lan := agent.IsLAN(c)
		clients = append(clients, sshClient{Addr: c.String(), LAN: lan, CutByLAN: !lan})
		cutByLAN = cutByLAN || !lan
	}
	// A lab on the internet is reached from anywhere (SPEC.md §6.12); the
	// choice is this server's (the lab's controller).
	internet := s.serverRefOf(r).This && s.network().Internet()
	if choice == "" {
		choice = cmp.Or(a.Choice, map[bool]string{true: config.SSHFromAny, false: config.SSHFromLAN}[internet])
	}
	return s.formMode(r, "ssh", map[string]any{"User": currentUser(r), "Choice": choice, "Clients": clients,
		"CutByLAN": cutByLAN, "Error": errMsg, "Internet": internet}), nil
}

func (s *Server) sshPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.sshData(r, "", "")
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "setup-ssh", data)
}

func (s *Server) sshSubmit(w http.ResponseWriter, r *http.Request) {
	choice := r.FormValue("ssh")
	if s.server(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	err := s.server(r).SetSSH(r.Context(), choice, r.FormValue("confirm") == "on")
	if err == nil && m.This {
		err = s.Setup.Done(r.Context(), setup.StepSSH)
	}
	if msg, ok := userMessage(err); ok {
		data, derr := s.sshData(r, choice, msg)
		if derr != nil {
			s.serverError(w, derr)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup-ssh", data)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", m.Label()+" SSH access set to "+choice)
	if !m.This {
		redirect(w, r, m.RemoteAccess())
		return
	}
	s.restartingOrNext(w, r)
}

func (s *Server) domainPage(w http.ResponseWriter, r *http.Request) {
	// A provider that isn't offered for an own domain (Roostack Cloud's,
	// for a roost.ac name) would leave no provider's fields shown, while
	// the list shows its first: start from that one.
	provider := s.DNSProvider
	if p := dns.Get(provider); p == nil || p.Hidden {
		provider = dns.Providers()[0].ID
	}
	// Until a domain is saved, the lab has the example one from the lab.yaml
	// the package installs: show it only as the field's placeholder.
	domain := ""
	if s.domainDone(r) {
		domain = s.Domain
	}
	choice := ""
	if domain != "" {
		choice = "own" // changing an own domain: its form open
	}
	s.render(w, "setup-domain", s.formMode(r, "domain", s.addressData(map[string]any{"User": currentUser(r), "Domain": domain,
		"Providers": dns.Providers(), "Provider": provider, "Values": map[string]string{}, "Manage": true, "Choice": choice})))
}

// addressData adds what the address step's two choices need (SPEC.md
// §6.6).
func (s *Server) addressData(data map[string]any) map[string]any {
	data["CloudDisabled"] = s.Cloud == nil
	data["OldDays"] = config.OldAddressDays
	if o := s.oldAddress(); o != nil {
		data["Old"] = o
	}
	data["Suffix"] = s.cloudSuffix(context.Background())
	if s.Cloud != nil {
		data["CloudView"] = cloudView{Status: s.Cloud.Status(), Setup: true, Suffix: data["Suffix"].(string)}
	}
	if _, ok := data["Choice"]; !ok {
		data["Choice"] = ""
	}
	return data
}

// roostAcName is a roost.ac name: a DNS label, 3 to 40 characters.
var roostAcName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

func (s *Server) domainSubmit(w http.ResponseWriter, r *http.Request) {
	domain, email, provider := r.FormValue("domain"), r.FormValue("email"), r.FormValue("provider")
	// Fields are named dns.<provider>.<key>; only the chosen provider's count.
	values := map[string]string{}
	kept := map[string]string{} // re-shown on error: never the sensitive ones
	if p := dns.Get(provider); p != nil {
		for _, f := range p.Fields {
			name := "dns." + p.ID + "." + f.Key
			values[f.Key] = r.FormValue(name)
			if !f.Sensitive {
				kept[name] = values[f.Key]
			}
		}
	}
	opts := setup.DomainOptions{ManageRecords: r.FormValue("manage_records") == "on", AcceptRisk: r.FormValue("accept_risk") == "on"}
	err := s.Setup.SaveDomain(r.Context(), domain, email, provider, values, opts)
	// The provider ends up in an Alpine expression: only known IDs go back.
	if dns.Get(provider) == nil {
		provider = dns.Providers()[0].ID
	}
	if s.wizardError(w, r, err, "setup-domain", s.formMode(r, "domain", s.addressData(map[string]any{"Domain": domain, "Email": email,
		"Providers": dns.Providers(), "Provider": provider, "Values": kept, "Manage": opts.ManageRecords, "AcceptRisk": opts.AcceptRisk,
		"Choice": "own"}))) {
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "domain set to "+domain)
	s.restartingOrNext(w, r)
}

func (s *Server) domainLater(w http.ResponseWriter, r *http.Request) {
	if err := s.Setup.DomainLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/")
}

func (s *Server) backupPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	// Just back from Roostack Cloud with a roost.ac address (cloudNext).
	if r.FormValue("address") != "" && s.Cloud != nil {
		if a := s.Cloud.Status().Address; a != "" {
			data["AddressSet"] = a
		}
	}
	s.renderBackupStep(w, r, data)
}

// renderBackupStep shows the wizard's backup step: the backup plan
// (plan.go), as on the Backups page. Once the step is over, the plan lives
// there.
func (s *Server) renderBackupStep(w http.ResponseWriter, r *http.Request, data map[string]any) {
	// Backup 1 finishes the step, but the plan stays here, for backups 2
	// and 3, until the wizard moves on.
	if data = s.formMode(r, "backup", data); !s.inWizard(r) {
		redirect(w, r, "/backups#plan")
		return
	}
	data["User"], data["Wizard"] = currentUser(r), true
	if p := s.backupPlan(r, s.serverRefOf(r), true); p != nil {
		p.Error, _ = data["Error"].(string)
		data["Plan"] = p
	}
	if data["Error"] != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "setup-backup", data)
}

// backupDrives lists this server's drives for adding a backup, leaving
// out those it already backs up to, with the chosen one (or the best)
// marked.
func (s *Server) backupDrives(r *http.Request, chosen string) (planOptions, error) {
	drives, err := s.Setup.BackupDrives(r.Context())
	if err != nil {
		return planOptions{}, err
	}
	m := s.serverRefOf(r)
	used := s.planRefs(r, m)
	// This server's own disk counts once, however its backup got there.
	for ref := range used {
		if s.Manager.BackupKind(r.Context(), m.ID, ref) == manager.KindSystem {
			used["drive:system"] = true
		}
	}
	o := planOptions{CanDrives: true}
	for _, d := range drives {
		if !used["drive:"+d.ID] {
			o.Drives = append(o.Drives, d)
		}
	}
	if !slices.ContainsFunc(o.Drives, func(d agent.Drive) bool { return d.ID == chosen && d.Usable }) {
		chosen = ""
		for _, d := range o.Drives {
			if d.Usable {
				chosen = d.ID
				break
			}
		}
	}
	o.Selected = chosen
	return o, nil
}

// drivesPartial refreshes the drive list (a drive plugged in or out).
func (s *Server) drivesPartial(w http.ResponseWriter, r *http.Request) {
	o, err := s.backupDrives(r, r.FormValue("drive"))
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<p class="error">%s</p>`, template.HTMLEscapeString("Couldn't list this server's drives: "+err.Error()))
		return
	}
	s.renderLive(w, r, "backups", "backup-drives", o)
}

// eraseDriveSubmit erases a disk and prepares it for backups, then goes
// back to the plan's chooser with it chosen.
func (s *Server) eraseDriveSubmit(w http.ResponseWriter, r *http.Request) {
	disk := r.FormValue("disk")
	wizard := s.inWizard(r)
	err := s.Setup.EraseDrive(r.Context(), disk)
	if msg, ok := userMessage(err); ok {
		s.renderPlanPage(w, r, wizard, msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "erased "+disk+" for backups")
	q := url.Values{"add": {r.FormValue("add")}}
	if r.FormValue("replace") == "1" {
		q.Set("replace", "1")
	}
	// The prepared disk's filesystem is the one chosen now.
	if drives, err := s.Setup.BackupDrives(r.Context()); err == nil {
		for _, d := range drives {
			if d.Disk == disk && d.Usable {
				q.Set("drive", d.ID)
			}
		}
	}
	back := "/backups"
	if wizard {
		back = "/setup/backup"
	}
	redirect(w, r, back+"?"+q.Encode()+"#plan")
}

// backupSubmit leaves the wizard's backup step, once there's a backup 1.
func (s *Server) backupSubmit(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.Manager.BackupTargetOf(r.Context(), s.serverRefOf(r).ID); !ok {
		s.renderBackupStep(w, r, map[string]any{"Error": "Add your main backup first, or set up backups later."})
		return
	}
	redirect(w, r, "/setup/recovery-key")
}

func (s *Server) backupLater(w http.ResponseWriter, r *http.Request) {
	pending := s.Setup.PendingRestart()
	if err := s.Setup.BackupLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	// The wizard's end applies what its steps saved.
	if pending {
		redirect(w, r, "/restarting")
		return
	}
	redirect(w, r, "/")
}

// inWizard reports whether the setup wizard is still going: settings
// saved in it wait for one restart at its end.
func (s *Server) inWizard(r *http.Request) bool {
	p, err := s.Setup.Progress(r.Context())
	return err == nil && p.Next() != ""
}

// restartingOrNext follows a setting that restarts Roostack: during the
// wizard, on to the next step; otherwise the "restarting" page.
func (s *Server) restartingOrNext(w http.ResponseWriter, r *http.Request) {
	if s.inWizard(r) {
		redirect(w, r, "/")
		return
	}
	redirect(w, r, "/restarting")
}

// The setup page shows the recovery key only while it's waiting to be
// saved: right after the lab's first backup. After that, the Owner sees it
// on the Backups page, after proving it's them (recoverykey.go).

// recoveryKeyDue reports whether the Owner has yet to confirm saving the
// key. Otherwise the setup page sends them to the Backups page.
func (s *Server) recoveryKeyDue(w http.ResponseWriter, r *http.Request) bool {
	p, err := s.Setup.Progress(r.Context())
	if err != nil {
		s.serverError(w, err)
		return false
	}
	if !p.BackupDone || p.RecoverySaved {
		redirect(w, r, "/backups#recovery-key")
		return false
	}
	return true
}

func (s *Server) recoveryPage(w http.ResponseWriter, r *http.Request) {
	if !s.recoveryKeyDue(w, r) {
		return
	}
	key, err := s.Setup.RecoveryKey()
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "recovery-key", map[string]any{"User": currentUser(r), "Key": key})
}

func (s *Server) recoveryConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.recoveryKeyDue(w, r) {
		return
	}
	if r.FormValue("saved") != "on" {
		key, err := s.Setup.RecoveryKey()
		if err != nil {
			s.serverError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "recovery-key", map[string]any{"User": currentUser(r), "Key": key,
			"Error": "Tick the box once you've saved the key."})
		return
	}
	// Confirming ends the wizard: Roostack restarts if a step's settings
	// were waiting for it, and the page waits for that.
	restarting := s.Setup.PendingRestart()
	if err := s.Setup.ConfirmRecoveryKey(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "recovery key confirmed")
	if restarting {
		redirect(w, r, "/restarting")
		return
	}
	redirect(w, r, "/backups")
}

// hasRecoveryKey reports whether the lab has its recovery key yet.
func (s *Server) hasRecoveryKey() bool {
	_, err := s.Setup.RecoveryKey()
	return err == nil
}

// wizardError re-renders a wizard page for a user-fixable error. It returns
// true if it handled err.
// errNoServer means this Roostack can't change its server's settings.
var errNoServer = errors.New("server settings aren't available")

// userMessage returns an error's message if it is one the user can act on
// (a problem with what they entered), rather than a server error.
func userMessage(err error) (string, bool) {
	var ce *setup.CheckError
	var fe *node.FormError
	switch {
	case errors.As(err, &ce):
		return ce.Msg, true
	case errors.As(err, &fe):
		return fe.Msg, true
	}
	return "", false
}

func (s *Server) wizardError(w http.ResponseWriter, r *http.Request, err error, page string, data map[string]any) bool {
	if err == nil {
		return false
	}
	msg, ok := userMessage(err)
	if !ok {
		s.serverError(w, err)
		return true
	}
	data["User"] = currentUser(r)
	data["Error"] = msg
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, page, data)
	return true
}
