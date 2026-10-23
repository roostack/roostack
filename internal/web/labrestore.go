package web

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/labrestore"
	"roostack.cloud/roostack/internal/words"
)

// Restoring a whole lab onto a new server (SPEC.md §6.9): on a fresh
// install, instead of creating the Owner, point Roostack at the lab's backups
// and enter the recovery key. Like creating the Owner, it needs the setup
// code, so whoever reaches the page first can't take over the server.

// LabRestorer rebuilds a lab from its backups (cmd/roostack, labrestore).
type LabRestorer interface {
	Find(ctx context.Context, src labrestore.Source, key string) ([]labrestore.Found, []labrestore.Unopened, error)
	Start(fd labrestore.Found) error
	Running() bool
	// Unfinished: a restore started and didn't finish, so it can be
	// started again over what it put back.
	Unfinished() bool
	Progress() (activity.Snapshot, bool)
	// Drives lists this server's drives; UseDrive mounts one, returning
	// where (to look for backups on it).
	Drives(ctx context.Context) ([]agent.Drive, error)
	UseDrive(ctx context.Context, id string) (string, error)
}

// restoreDrives are the drives that might hold backups (not this server's
// own disk: it's new).
func (s *Server) restoreDrives(r *http.Request, data map[string]any) {
	out := s.connectedDrives(r)
	data["Drives"] = out
	if d, _ := data["Drive"].(string); d == "" {
		data["Drive"] = "other"
		if len(out) > 0 {
			data["Drive"] = out[0].ID
		}
	}
}

func (s *Server) connectedDrives(r *http.Request) []agent.Drive {
	drives, _ := s.LabRestore.Drives(r.Context())
	var out []agent.Drive
	for _, d := range drives {
		if d.Usable && d.Kind != "system" {
			out = append(out, d)
		}
	}
	return out
}

// restoreCookie ties the browser that entered the setup code to what it
// found.
const restoreCookie = "roostack_restore"

// restoreSession is one browser's search (in memory: a restart ends it).
type restoreSession struct {
	id    string
	found []labrestore.Found
	// origin is where the search was made (originOf): it's only found
	// there.
	origin string
}

func (s *Server) restoreSessionOf(r *http.Request) *restoreSession {
	c, err := r.Cookie(restoreCookie)
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if err != nil || s.restore == nil || s.restore.origin != originOf(r) || !authkit.TokensEqual(c.Value, s.restore.id) {
		return nil
	}
	return s.restore
}

// restoreAllowed: a fresh install, with restoring available. Its pages
// aren't cached: they echo where the backups are, credentials included.
func (s *Server) restoreAllowed(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case !s.needsSetup():
		// Done (Roostack restarted as the restored lab), or never needed.
		if r.Header.Get("HX-Request") != "" {
			w.Header().Set("HX-Redirect", "/login")
			return false
		}
		redirect(w, r, "/login")
		return false
	case s.LabRestore == nil:
		http.NotFound(w, r)
		return false
	}
	return true
}

func (s *Server) labRestorePage(w http.ResponseWriter, r *http.Request) {
	if !s.restoreAllowed(w, r) {
		return
	}
	if s.LabRestore.Running() && s.restoreSessionOf(r) != nil {
		redirect(w, r, "/setup/restore/progress")
		return
	}
	data := map[string]any{"Kind": "s3", "Passed": s.setupPassed(r), "CloudOn": s.Cloud != nil,
		"Unfinished": !s.LabRestore.Running() && s.LabRestore.Unfinished()}
	if s.Cloud != nil {
		data["Kind"] = "cloud"
	}
	s.restoreDrives(r, data)
	if len(data["Drives"].([]agent.Drive)) > 0 {
		data["Kind"] = "folder"
	}
	s.render(w, "setup-restore", data)
}

func (s *Server) labRestoreFind(w http.ResponseWriter, r *http.Request) {
	if !s.restoreAllowed(w, r) {
		return
	}
	src := labrestore.Source{Kind: r.FormValue("kind"), Folder: r.FormValue("folder"), URL: r.FormValue("url"),
		S3: backup.S3{Endpoint: r.FormValue("endpoint"), Bucket: r.FormValue("bucket"), Prefix: r.FormValue("prefix"),
			AccessKey: r.FormValue("access_key"), SecretKey: r.FormValue("secret_key")}}
	// However it's grouped or spaced, as it was made.
	key := backup.TypedRecoveryKey(r.FormValue("recovery_key"))
	// Everything typed comes back, except the secrets.
	data := map[string]any{"Kind": src.Kind, "Folder": src.Folder, "URL": src.URL, "S3": src.S3, "Drive": r.FormValue("drive"),
		"Passed": s.setupPassed(r), "CloudOn": s.Cloud != nil}
	// The form's choice of where comes back as it was, if it's one.
	if !slices.Contains([]string{"s3", "folder", "url"}, src.Kind) && !(src.Kind == "cloud" && s.Cloud != nil) {
		data["Kind"] = "s3"
	}
	s.restoreDrives(r, data)
	fail := func(msg string) {
		data["Error"] = msg
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup-restore", data)
	}
	if !s.setupPassed(r) {
		time.Sleep(failedLoginDelay)
		fail("The setup code is wrong. It is shown in the Roostack log and in " + s.SetupTokenFile + ".")
		return
	}
	// A drive is mounted (if it isn't) and searched.
	if id := r.FormValue("drive"); src.Kind == "folder" && id != "" && id != "other" {
		dir, err := s.LabRestore.UseDrive(r.Context(), id)
		if err != nil {
			fail(words.Capitalise(err.Error()) + ".")
			return
		}
		src.Folder = dir
	}
	if msg := src.Problem(); msg != "" {
		fail(msg)
		return
	}
	if key == "" {
		fail("Enter the recovery key: the one you wrote down or printed when you set the lab up.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	found, unopened, err := s.LabRestore.Find(ctx, src, key)
	if err != nil {
		fail(words.Capitalise(err.Error()) + ".")
		return
	}
	id, err := authkit.NewToken()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.setupMu.Lock()
	s.restore = &restoreSession{id: id, found: found, origin: originOf(r)}
	s.setupMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: restoreCookie, Value: id, Path: "/setup/restore", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r)})
	data["Found"], data["Searched"] = found, true
	data["Groups"], data["Forged"], data["Unopened"] = labGroups(found, unopened)
	s.render(w, "setup-restore", data)
}

// labChoice is a backup found, with its place in the search (what
// the restore form sends).
type labChoice struct {
	Index int
	labrestore.Found
}

// labGroup is one lab's backups in Roostack Cloud (Lab is nil for
// backups found elsewhere). Gone says why a lab has none.
type labGroup struct {
	Lab   *labrestore.CloudLab
	Found []labChoice
	Gone  string
}

// labGroups sorts a search's results for the page: the backups by
// lab in Roostack Cloud (a lab unlinked and linked again has two, and the
// person chooses; SPEC.md §6.9), the backups refused as forged, and the
// rest that weren't opened.
func labGroups(found []labrestore.Found, unopened []labrestore.Unopened) (groups []*labGroup, forged, others []labrestore.Unopened) {
	group := func(lab *labrestore.CloudLab) *labGroup {
		for _, g := range groups {
			if g.Lab == lab {
				return g
			}
		}
		g := &labGroup{Lab: lab}
		groups = append(groups, g)
		return g
	}
	for i, fd := range found {
		g := group(fd.Lab)
		g.Found = append(g.Found, labChoice{Index: i, Found: fd})
	}
	for _, u := range unopened {
		switch {
		case u.Forged:
			forged = append(forged, u)
		case u.Repository == "" && u.Lab != nil:
			group(u.Lab).Gone = u.Problem
		default:
			others = append(others, u)
		}
	}
	return groups, forged, others
}

func (s *Server) labRestoreStart(w http.ResponseWriter, r *http.Request) {
	if !s.restoreAllowed(w, r) {
		return
	}
	sess := s.restoreSessionOf(r)
	i, err := strconv.Atoi(r.FormValue("backup"))
	if sess == nil || err != nil || i < 0 || i >= len(sess.found) {
		redirect(w, r, "/setup/restore")
		return
	}
	if err := s.LabRestore.Start(sess.found[i]); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup-restore", map[string]any{"Kind": "s3", "CloudOn": s.Cloud != nil, "Error": words.Capitalise(err.Error()) + "."})
		return
	}
	redirect(w, r, "/setup/restore/progress")
}

func (s *Server) labRestoreProgress(w http.ResponseWriter, r *http.Request) {
	if !s.restoreAllowed(w, r) {
		return
	}
	if s.restoreSessionOf(r) == nil {
		redirect(w, r, "/setup/restore")
		return
	}
	p, _ := s.LabRestore.Progress()
	data := map[string]any{"Progress": p, "Running": s.LabRestore.Running() || p.Finished.IsZero()}
	// On the setup pages at home, the page can't follow Roostack once it
	// restarts as the restored lab: that closes them. It says where to go
	// instead, from the restored settings once they're back (restore-next).
	if fromSetupListener(r) {
		data["OnSetup"] = true
		if s.FirstRunAddress != nil {
			data["Address"] = s.FirstRunAddress()
		}
		if ap, err := netip.ParseAddrPort(r.Host); err == nil {
			data["ServerAddr"] = ap.Addr().String()
		}
	}
	if r.Header.Get("HX-Request") != "" {
		s.renderPart(w, "setup-restore", "restore-progress", data)
		return
	}
	data["Restoring"] = true
	s.render(w, "setup-restore", data)
}
