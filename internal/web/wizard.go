package web

import (
	"cmp"
	"errors"
	"net/http"
	"slices"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/hosttime"
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
		data["Wizard"], data["Back"], data["BackText"] = false, "/settings/servers", "Servers"
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
	s.render(w, "setup-timezone", s.formMode(r, "timezone", map[string]any{"User": currentUser(r),
		"Current": current, "Zone": current, "Zones": zones, "HostClock": s.clockOf(r)}))
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
		s.wizardError(w, r, err, "setup-timezone", s.formMode(r, "timezone", map[string]any{
			"Current": current, "Zone": zone, "Zones": zones, "HostClock": s.clockOf(r)}))
		return
	}
	if changed {
		s.Store.Audit(r.Context(), currentUser(r), "setup", m.Label()+" time zone set to "+zone)
	}
	switch {
	case !m.This:
		redirect(w, r, m.TimeZone())
	case !changed:
		// On to the next wizard step, or back to Settings.
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
	if choice == "" {
		choice = cmp.Or(a.Choice, config.SSHFromLAN)
	}
	return s.formMode(r, "ssh", map[string]any{"User": currentUser(r), "Choice": choice, "Clients": clients,
		"CutByLAN": cutByLAN, "Error": errMsg}), nil
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
	redirect(w, r, "/restarting")
}

func (s *Server) domainPage(w http.ResponseWriter, r *http.Request) {
	provider := s.DNSProvider
	if dns.Get(provider) == nil {
		provider = dns.Providers()[0].ID
	}
	s.render(w, "setup-domain", s.formMode(r, "domain", map[string]any{"User": currentUser(r), "Domain": s.Domain,
		"Providers": dns.Providers(), "Provider": provider, "Values": map[string]string{}, "Manage": true}))
}

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
	if s.wizardError(w, r, err, "setup-domain", s.formMode(r, "domain", map[string]any{"Domain": domain, "Email": email,
		"Providers": dns.Providers(), "Provider": provider, "Values": kept, "Manage": opts.ManageRecords, "AcceptRisk": opts.AcceptRisk})) {
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "domain set to "+domain)
	redirect(w, r, "/restarting")
}

func (s *Server) domainLater(w http.ResponseWriter, r *http.Request) {
	if err := s.Setup.DomainLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/")
}

func (s *Server) backupPage(w http.ResponseWriter, r *http.Request) {
	s.renderBackupStep(w, r, map[string]any{"Kind": "drive"})
}

// renderBackupStep shows the backup step with this server's drives: the
// best one (a USB drive or second disk, if any) chosen unless another is.
func (s *Server) renderBackupStep(w http.ResponseWriter, r *http.Request, data map[string]any) {
	data["User"], data["Current"] = currentUser(r), s.BackupRepo
	s.addDrives(r, data, r.FormValue("drive"))
	if data["Error"] != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "setup-backup", s.formMode(r, "backup", data))
}

// addDrives lists the drives, marking the chosen one (or the best).
func (s *Server) addDrives(r *http.Request, data map[string]any, chosen string) {
	drives, err := s.Setup.BackupDrives(r.Context())
	if err != nil {
		data["DrivesError"] = "Couldn't list this server's drives: " + err.Error()
		return
	}
	if !slices.ContainsFunc(drives, func(d agent.Drive) bool { return d.ID == chosen && d.Usable }) {
		chosen = ""
		for _, d := range drives {
			if d.Usable {
				chosen = d.ID
				break
			}
		}
	}
	data["Drives"], data["Selected"] = drives, chosen
}

// drivesPartial refreshes the drive list (a drive plugged in or out).
func (s *Server) drivesPartial(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	s.addDrives(r, data, r.FormValue("drive"))
	s.renderPart(w, "setup-backup", "backup-drives", data)
}

// eraseDriveSubmit erases a disk and prepares it for backups.
func (s *Server) eraseDriveSubmit(w http.ResponseWriter, r *http.Request) {
	disk := r.FormValue("disk")
	err := s.Setup.EraseDrive(r.Context(), disk)
	data := map[string]any{"Kind": "drive"}
	if msg, ok := userMessage(err); ok {
		data["Error"] = msg
	} else if err != nil {
		s.serverError(w, err)
		return
	} else {
		s.Store.Audit(r.Context(), currentUser(r), "setup", "erased "+disk+" for backups")
		data["Notice"] = "The disk is ready. It's chosen below: carry on when you're ready."
		// The prepared disk's filesystem is the one chosen now.
		if drives, err := s.Setup.BackupDrives(r.Context()); err == nil {
			for _, d := range drives {
				if d.Disk == disk && d.Usable {
					r.Form.Set("drive", d.ID)
				}
			}
		}
	}
	s.renderBackupStep(w, r, data)
}

func (s *Server) backupSubmit(w http.ResponseWriter, r *http.Request) {
	c := setup.BackupChoice{
		Kind: r.FormValue("kind"), Drive: r.FormValue("drive"), Path: r.FormValue("path"),
		Endpoint: r.FormValue("endpoint"), Bucket: r.FormValue("bucket"), Prefix: r.FormValue("prefix"),
		AccessKey: r.FormValue("access_key"), SecretKey: r.FormValue("secret_key"),
	}
	err := s.Setup.SaveBackup(r.Context(), c)
	// Keep what was typed, except the secret key. Kind ends up in an Alpine
	// expression, so only known values go back.
	if c.Kind != "drive" && c.Kind != "local" && c.Kind != "s3" {
		c.Kind = "drive"
	}
	if msg, ok := userMessage(err); ok {
		s.renderBackupStep(w, r, map[string]any{"Kind": c.Kind, "Path": c.Path, "Error": msg,
			"Endpoint": c.Endpoint, "Bucket": c.Bucket, "Prefix": c.Prefix, "AccessKey": c.AccessKey})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "backup destination set")
	redirect(w, r, "/setup/recovery-key")
}

func (s *Server) backupLater(w http.ResponseWriter, r *http.Request) {
	if err := s.Setup.BackupLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/")
}

func (s *Server) recoveryPage(w http.ResponseWriter, r *http.Request) {
	key, err := s.Setup.RecoveryKey()
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "recovery-key", map[string]any{"User": currentUser(r), "Key": key})
}

func (s *Server) recoveryConfirm(w http.ResponseWriter, r *http.Request) {
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
	if err := s.Setup.ConfirmRecoveryKey(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "recovery key confirmed")
	redirect(w, r, "/restarting")
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
