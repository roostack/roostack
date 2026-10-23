package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/destination"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// A server's backup plan (SPEC.md §6.9), on the Backups page and in the
// setup wizard alike, numbered as the 3-2-1 rule counts copies: 1 is the
// data on the server itself, 2 the main backup (restores read it first),
// 3 a second backup; any more are optional extras, not numbered. Empty
// places show what to add, and one chooser adds a backup anywhere.

// planEntry is one of a server's backups.
type planEntry struct {
	// Number is its place in the plan (2 for the main backup); Extra: past
	// the 3 copies, shown without a number.
	Number                 int
	Extra                  bool
	Ref, Title, Repository string
	Kind, KindTitle        string // manager.Kind*
	AtHome, Away, Main     bool
	// Access is what this server's credential can do there: whether the
	// backups survive the server being taken over.
	Access manager.Access
	// KeyAction is where a new key for this online storage account is
	// posted, and KeyOpen opens its form; KeyChange is the latest change,
	// while it runs and for an hour after (it says what to do next).
	KeyAction    string
	KeyOpen      bool
	KeyChange    *activityView
	Last, LastOK *store.BackupRun
	Retry        *backup.Retry // the next try after a failure
	CanPromote   bool
	// AskWhere are the servers whose location decides at home or away.
	AskWhere []locationAsk
	// Space, where known: Stored is what the backups there take (from
	// the last backup); Free the free space on its drive (-1 unknown);
	// Lab the whole lab's use of a friend's lab's space (nil unknown).
	Stored int64
	Free   int64
	// FreeOn names where Free is ("this server's disk", "the drive",
	// "vps's disk"); LowSpace is attention.LowSpace's level for it.
	FreeOn, LowSpace string
	Lab              *labnet.GuestUsage
	// Cloud is Roostack Cloud backup's space, on its backup (nil
	// otherwise, or not known yet).
	Cloud *attention.CloudSpace
	// Path says how backups reach another server's disk: manager.PathDirect
	// or PathHub (through the controller, named by Via); "" otherwise.
	// For PathHub, Host and HostName are that server, and LinkFailed says
	// a direct link was tried and didn't connect.
	Path, Via      string
	Host, HostName string
	LinkFailed     bool
	// RollbackAction is where rolling this backup back to an earlier time
	// is posted, where the storage keeps old versions (Access.Versioned);
	// RollbackOpen opens its form, RollbackBefore is the time in it
	// (datetime-local's form). RollbackLog is the latest check or
	// rollback (for an hour), and RollbackPlan a check that can be acted
	// on.
	RollbackAction, RollbackBefore string
	RollbackOpen                   bool
	RollbackLog                    *activityView
	RollbackPlan                   *manager.RollbackPlan
}

// planSlot is an empty place in the plan, with a suggestion for it.
type planSlot struct {
	Number  int
	Extra   bool
	Title   string
	Suggest string
}

// planView is a server's plan for the page.
type planView struct {
	Server  serverRef
	Wizard  bool
	Entries []planEntry
	Slots   []planSlot
	More    int // the number the next backup gets
	// Checks are how it stands against the 3-2-1 rule, with a hint for
	// the main backup and what's missing for the empty places.
	Checks manager.PlanCheck
	// Adding is the number whose chooser is open (0 if none); Replacing:
	// it replaces the main backup. ChooserTitle and ChooserButton say so.
	Adding                      int
	Replacing                   bool
	ChooserTitle, ChooserButton string
	ChooserNum                  planSlot
	Options                     planOptions
	// Busy is a backup being set up (or one that failed lately); BusyIn
	// the number of the box it's for while it runs (from the chooser's
	// redirect, "setting"), where it shows instead of above the list.
	Busy   *activityView
	BusyIn int
	Error  string
	// Next is when the next backup runs (zero if unknown).
	Next time.Time
	// Lost: the main backup was on a server that left the lab, and there's
	// no new one yet.
	Lost *store.LostBackup
	// Internet: this server is the lab on the internet (a cloud server,
	// SPEC.md §6.12): "away" means away from it and its provider, and a
	// drive at home isn't there to suggest.
	Internet bool
	// Action is where the chooser posts; Back where the page is.
	Action, Back string
}

// planOptions are the places a backup can be added, by kind.
type planOptions struct {
	CanDrives bool
	Drives    []agent.Drive
	// OwnDisk is another server's own disk: its drives aren't listed here.
	OwnDisk                  *manager.BackupChoice
	Selected                 string
	Online, Friends, Servers []manager.BackupChoice
	// Cloud is Roostack Cloud backup, when this lab's account may use it.
	Cloud *manager.BackupChoice
}

func kindTitle(kind string) string {
	return map[string]string{manager.KindUSB: "USB drive", manager.KindDisk: "Second disk", manager.KindNetwork: "Network share",
		manager.KindSystem: "This server's disk", manager.KindFolder: "Folder", manager.KindOnline: "Online storage",
		manager.KindFriend: "A friend's lab", manager.KindServer: "Another Roostack instance", manager.KindCloud: "Roostack Cloud"}[kind]
}

// backupPlan builds a server's plan. adding and replacing open the
// chooser (from the page's query).
func (s *Server) backupPlan(r *http.Request, m serverRef, wizard bool) *planView {
	ctx := r.Context()
	p := &planView{Server: m, Wizard: wizard, Back: m.Backups(), Action: m.path("/backups/plan", "/backups/plan"),
		Internet: m.This && s.onInternet()}
	if wizard {
		p.Back, p.Action = "/setup/backup", "/setup/backup/add"
	}
	var st *backup.Status
	if b := s.backups(r); b != nil {
		if x, err := b.Status(ctx); err == nil {
			st = &x
			p.Next = x.Next
		}
	}
	// This server's drives, for the free space on them.
	drives := map[string]agent.Drive{}
	if m.This {
		if ds, err := s.Setup.BackupDrives(ctx); err == nil {
			for _, d := range ds {
				drives[d.ID] = d
			}
		}
	}
	used := map[string]bool{}
	add := func(e planEntry) {
		e.Number = len(p.Entries) + 2
		e.Extra = e.Number > manager.PlanCopies
		e.Kind = s.Manager.BackupKind(ctx, m.ID, e.Ref)
		e.KindTitle = kindTitle(e.Kind)
		e.Free = -1
		if e.LastOK != nil {
			e.Stored = int64(e.LastOK.Stored)
		}
		if d, ok := drives[driveOf(e.Ref, e.Kind)]; ok && m.This {
			e.Free, e.FreeOn, e.LowSpace = d.Free, "the drive", attention.LowSpace(d.Free, d.Size)
			if e.Kind == manager.KindSystem {
				e.FreeOn = "this server's disk"
			}
		}
		if ref := destination.Parse(e.Ref); ref.Kind == destination.Server {
			host := ref.ID
			e.Path = s.Manager.LinkPath(ctx, m.ID, host)
			if s.Node != nil {
				e.Via = s.Node.Identity().Name
			}
			if e.Path == manager.PathHub {
				e.Host, e.LinkFailed = host, s.Manager.LinkFailed(ctx, m.ID, host)
				if n := s.Manager.NodeByID(host); n != nil {
					e.HostName = n.Identity().Name
				}
			}
			// Another server's backup store: its disk, as it says.
			if d := node.Capability(s.Manager.NodeByID(host), node.Node.Disks); d != nil {
				dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				if disks, err := d.DiskSpaces(dctx); err == nil {
					if ds, ok := node.StoreDisk(disks); ok {
						e.Free, e.FreeOn, e.LowSpace = ds.Free, "that disk", attention.LowSpace(ds.Free, ds.Total)
					}
				}
				cancel()
			}
		}
		if e.Kind == manager.KindCloud && s.Cloud != nil {
			e.Cloud = attention.CloudSpaceOf(s.Cloud.Status())
		}
		e.AtHome, e.Away = manager.AtHome(e.Kind), manager.Away(e.Kind)
		e.Access = s.Manager.BackupAccess(ctx, m.ID, e.Ref)
		if ref := destination.Parse(e.Ref); ref.Kind == destination.Provider && e.Kind == manager.KindOnline && !wizard {
			id := ref.ID
			e.KeyAction, e.KeyOpen = p.Action+"/key", r.FormValue("key") == e.Ref
			if a := s.activityFor(r, activity.BackupKeyFor(id)); a != nil && a.Recent(time.Hour) {
				e.KeyChange = a
			}
		}
		if e.Access.Versioned && !wizard {
			s.planRollback(r, m, &e)
		}
		if e.Kind == manager.KindServer {
			e.AtHome, e.Away, e.AskWhere = s.backupPlace(ctx, m, destination.Parse(e.Ref).ID)
		}
		p.Entries = append(p.Entries, e)
		used[e.Ref] = true
	}
	main, mainTitle, hasMain := s.Manager.BackupTargetOf(ctx, m.ID)
	if hasMain {
		e := planEntry{Ref: main.Provider, Title: mainTitle, Repository: main.Repository, Main: true}
		if st != nil {
			e.Last, e.LastOK, e.Retry = st.For(main.Repository)
		}
		add(e)
	}
	if l, ok := s.Manager.LostMainBackup(ctx, m.ID); ok && !hasMain {
		p.Lost = &l
	}
	for _, c := range s.Manager.BackupCopiesOf(ctx, m.ID) {
		e := planEntry{Ref: c.Ref, Title: c.Title, Repository: c.Repository}
		if st != nil {
			e.Last, e.LastOK, e.Retry = st.For(c.Repository)
		}
		add(e)
	}

	// 3-2-1, and what's missing (manager.PlanChecks).
	var backups []manager.PlanBackup
	for _, e := range p.Entries {
		backups = append(backups, manager.PlanBackup{Title: e.Title, Kind: e.Kind, AtHome: e.AtHome, Away: e.Away,
			Access: e.Access, LastOK: e.LastOK})
	}
	p.Checks = manager.PlanChecks(backups, p.Internet)
	for i := range p.Entries {
		p.Entries[i].CanPromote = i > 0
	}

	// Empty places, each with a suggestion from what's there; only the
	// next one can be added.
	p.More = len(p.Entries) + 2
	for n := p.More; n <= manager.PlanCopies; n++ {
		slot := planSlot{Number: n}
		switch {
		case n == 2 && p.Internet:
			slot.Title, slot.Suggest = "Your main backup", "Suggested: online storage (such as Backblaze B2), or space at your own or a friend's lab. "+
				"A disk on this server restores quickest, but goes if the server does."
		case n == 2:
			slot.Title, slot.Suggest = "Your main backup", "Suggested: a drive at home, like a USB drive or a NAS. Restoring from it is quick."
		case n > p.More:
			slot.Title, slot.Suggest = "A second backup", "Add your main backup first."
		default:
			if slot.Title, slot.Suggest = p.Checks.Missing, p.Checks.Suggest; slot.Title == "" {
				slot.Title, slot.Suggest = "A second backup", "Anywhere you like, so losing one backup still leaves another."
			}
		}
		p.Slots = append(p.Slots, slot)
	}
	// Past the three copies, once they're all there: an optional extra
	// backup.
	if p.More > manager.PlanCopies {
		extra := planSlot{Extra: true, Number: p.More, Title: "An extra backup (optional)"}
		if p.Checks.Missing != "" {
			extra.Title, extra.Suggest = p.Checks.Missing+" (optional)", p.Checks.Suggest
		} else {
			extra.Suggest = "Another copy, anywhere you like. The 3-2-1 rule is met already."
		}
		p.Slots = append(p.Slots, extra)
	}

	// The chooser. Backups are added in order: only the next one.
	if n, _ := strconv.Atoi(r.FormValue("add")); n == p.More {
		p.Adding = n
	}
	p.Replacing = r.FormValue("replace") == "1" && hasMain
	switch {
	case p.Replacing:
		p.Adding, p.ChooserTitle, p.ChooserButton = 2, "Where should the main backup go instead?", "Use this for the main backup"
	case p.Adding == 2 || p.Lost != nil:
		p.ChooserTitle, p.ChooserButton = "Where should your main backup go?", "Add the main backup"
	case p.Adding > manager.PlanCopies:
		p.ChooserTitle, p.ChooserButton = "Where should the extra backup go?", "Add the extra backup"
	default:
		p.ChooserTitle, p.ChooserButton = "Where should backup "+strconv.Itoa(p.Adding)+" go?", "Add backup "+strconv.Itoa(p.Adding)
	}
	p.ChooserNum = planSlot{Number: p.Adding, Extra: p.Adding > manager.PlanCopies}
	if p.Adding > 0 {
		p.Options = s.planOptions(r, m, main, hasMain, used, wizard)
	}
	// Setting a backup up: while it runs, and if it failed lately (so the
	// reason stays in view).
	if a := s.activityFor(r, activity.BackupSetupFor(m.ID)); a != nil && a.Recent(time.Hour) && (a.Running() || a.Err != "") {
		p.Busy = a
		if n, _ := strconv.Atoi(r.FormValue("setting")); a.Running() && n >= 2 && n <= p.More && p.Adding == 0 {
			p.BusyIn = n
		}
	}
	return p
}

// driveOf is the drive (agent.Drive.ID) a backup is on, if on one of the
// server's drives.
func driveOf(ref, kind string) string {
	if r := destination.Parse(ref); r.Kind == destination.Drive {
		return r.ID
	}
	if kind == manager.KindSystem {
		return "system"
	}
	return ""
}

// addLabUsage shows, on a backup at a friend's lab, the whole lab's use
// of the space there (remotes is what the page asked of those labs).
func (p *planView) addLabUsage(remotes []remoteView) {
	for i := range p.Entries {
		for _, rv := range remotes {
			if rv.Ref == p.Entries[i].Ref && rv.Reachable {
				u := rv.Usage
				p.Entries[i].Lab = &u
			}
		}
	}
}

// planRefs are where a server's backups go now.
func (s *Server) planRefs(r *http.Request, m serverRef) map[string]bool {
	used := map[string]bool{}
	if t, _, ok := s.Manager.BackupTargetOf(r.Context(), m.ID); ok {
		used[t.Provider] = true
	}
	for _, c := range s.Manager.BackupCopiesOf(r.Context(), m.ID) {
		used[c.Ref] = true
	}
	return used
}

// planOptions lists where a backup can go, leaving out where this server's
// backups already go.
func (s *Server) planOptions(r *http.Request, m serverRef, main store.BackupTarget, hasMain bool, used map[string]bool, wizard bool) planOptions {
	ctx := r.Context()
	var o planOptions
	// Drives: this server's own (roostack-agent sees only its server's).
	if m.This {
		if drives, err := s.backupDrives(r, r.FormValue("drive")); err == nil {
			o = drives
		}
	}
	choices, _ := s.Manager.BackupChoices(ctx)
	for _, c := range choices {
		if used[c.Ref] {
			continue
		}
		switch s.Manager.BackupKind(ctx, m.ID, c.Ref) {
		case manager.KindCloud:
			o.Cloud = &c
		case manager.KindOnline:
			o.Online = append(o.Online, c)
		case manager.KindFriend:
			o.Friends = append(o.Friends, c)
		case manager.KindServer:
			// A copy can't share the main backup's store.
			if !wizard && !(hasMain && c.Ref == destination.New(destination.Server, main.StoreHost)) {
				o.Servers = append(o.Servers, c)
			}
		case manager.KindSystem:
			// The server's own disk: a drive here, or its store elsewhere.
			if !o.CanDrives {
				o.OwnDisk = &c
			}
		}
	}
	return o
}

// planAddSubmit adds a backup to a server's plan (or, replacing, changes
// backup 1): the chooser's kind picks where.
func (s *Server) planAddSubmit(wizard bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := s.serverRefOf(r)
		if !wizard && s.serverOf(r) == nil {
			http.NotFound(w, r)
			return
		}
		ctx := r.Context()
		back := cmp.Or(map[bool]string{true: "/setup/backup", false: m.Backups()}[wizard], "/backups")
		fail := func(msg string) {
			s.renderPlanPage(w, r, wizard, msg)
		}
		ref, err := s.resolveChoice(r)
		if msg, ok := usererr.Message(err); ok {
			fail(msg)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		if ref == "" {
			fail("Choose where this backup goes.")
			return
		}
		_, _, hasMain := s.Manager.BackupTargetOf(ctx, m.ID)
		// The lab's first backup makes the recovery key (manager), which the
		// Owner must then see and save, wherever the backup was added.
		keyIsNew := !s.hasRecoveryKey()
		if !hasMain || r.FormValue("replace") == "1" {
			err = s.Manager.SetBackupDestination(ctx, m.ID, ref)
		} else {
			err = s.Manager.AddBackupCopy(ctx, m.ID, ref)
		}
		if msg, ok := usererr.Message(err); ok {
			fail(msg)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		// In the wizard, backup 1 shows the key (the same one, if backups
		// were set up before); anywhere, a key made just now is shown.
		if wizard || keyIsNew {
			if err := s.Setup.BackupAdded(ctx, (wizard && !hasMain) || keyIsNew); err != nil {
				s.serverError(w, err)
				return
			}
		}
		s.Store.Audit(ctx, currentUser(r), "backup", "added a backup for "+m.InSentence()+": "+ref)
		// Its progress shows in the box it's for (the Owner, 2026-10-18).
		box := r.FormValue("add")
		if r.FormValue("replace") == "1" || !hasMain {
			box = "2"
		}
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		redirect(w, r, back+sep+url.Values{"setting": {box}}.Encode()+"#plan")
	}
}

// resolveChoice turns the chooser's fields into a destination reference,
// adding an online storage account or another lab's storage first when
// their details were given.
func (s *Server) resolveChoice(r *http.Request) (string, error) {
	ctx := r.Context()
	switch r.FormValue("choice") {
	case "drive":
		if d := r.FormValue("drive"); destination.Parse(d).Kind == destination.Server {
			return d, nil // another server's own disk
		} else if d != "" {
			return destination.New(destination.Drive, d), nil
		}
		if f := strings.TrimSpace(r.FormValue("folder")); f != "" {
			return destination.New(destination.Folder, f), nil
		}
		return "", nil
	case "online":
		if ref := r.FormValue("provider"); ref != "" && ref != "new" {
			return ref, nil
		}
		before, _ := s.Manager.BackupChoices(ctx)
		p := manager.S3Provider{Endpoint: r.FormValue("endpoint"), Bucket: r.FormValue("bucket"), Prefix: r.FormValue("prefix"),
			AccessKey: r.FormValue("access_key"), SecretKey: r.FormValue("secret_key")}
		if err := s.Manager.AddS3Provider(ctx, cmp.Or(strings.TrimSpace(r.FormValue("name")), "Online storage"), p); err != nil {
			return "", err
		}
		return newChoice(before, s.choicesNow(r)), nil
	case "friend":
		if ref := r.FormValue("remote"); ref != "" && ref != "new" {
			return ref, nil
		}
		before, _ := s.Manager.BackupChoices(ctx)
		if err := s.Manager.AddRemoteStorage(ctx, r.FormValue("invite")); err != nil {
			return "", err
		}
		return newChoice(before, s.choicesNow(r)), nil
	case "server":
		return r.FormValue("server"), nil
	case "cloud":
		return destination.Cloud, nil
	}
	return "", nil
}

func (s *Server) choicesNow(r *http.Request) []manager.BackupChoice {
	c, _ := s.Manager.BackupChoices(r.Context())
	return c
}

// newChoice is the one choice in after that isn't in before.
func newChoice(before, after []manager.BackupChoice) string {
	for _, c := range after {
		if !slices.ContainsFunc(before, func(b manager.BackupChoice) bool { return b.Ref == c.Ref }) {
			return c.Ref
		}
	}
	return ""
}

// planPromoteSubmit makes a copy backup 1.
func (s *Server) planPromoteSubmit(w http.ResponseWriter, r *http.Request) {
	if s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	err := s.Manager.PromoteBackupCopy(r.Context(), m.ID, r.FormValue("destination"))
	if msg, ok := usererr.Message(err); ok {
		s.renderPlanPage(w, r, false, msg)
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
	s.Store.Audit(r.Context(), currentUser(r), "backup", "made "+r.FormValue("destination")+" backup 1 for "+m.InSentence())
	redirect(w, r, m.Backups()+"#plan")
}

// planRemoveSubmit stops one of a server's backups (not backup 1: another
// takes its place first). What's there already stays.
func (s *Server) planRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	if s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	ref := r.FormValue("destination")
	err := s.Manager.RemoveBackupCopy(r.Context(), m.ID, ref)
	if msg, ok := usererr.Message(err); ok {
		s.renderPlanPage(w, r, false, msg)
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
	s.Store.Audit(r.Context(), currentUser(r), "backup", "stopped a backup for "+m.InSentence()+": "+ref)
	redirect(w, r, m.Backups()+"#plan")
}

// planKeySubmit gives Roostack a new key for an online storage account
// one of this server's backups uses; every server backing up there gets
// its new credentials in the background (SPEC.md §10.10).
func (s *Server) planKeySubmit(w http.ResponseWriter, r *http.Request) {
	if s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	ref := r.FormValue("key")
	if !s.planRefs(r, m)[ref] {
		http.NotFound(w, r)
		return
	}
	err := s.Manager.ChangeS3Key(r.Context(), ref, r.FormValue("access_key"), r.FormValue("secret_key"))
	if msg, ok := usererr.Message(err); ok {
		s.renderPlanPage(w, r, false, msg)
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
	s.Store.Audit(r.Context(), currentUser(r), "backup", "gave Roostack a new key for "+ref)
	redirect(w, r, m.Backups()+"#plan")
}

// renderPlanPage shows the page the plan is on, with an error.
func (s *Server) renderPlanPage(w http.ResponseWriter, r *http.Request, wizard bool, msg string) {
	if wizard {
		s.renderBackupStep(w, r, map[string]any{"Error": msg})
		return
	}
	data := s.pageData(r)
	data["Error"] = msg
	s.renderBackups(w, r, data)
}

// rollbackTime is how the time to go back to is passed ("before"): a
// date and a time, as datetime-local writes them.
const rollbackTime = "2006-01-02T15:04"

// rollbackBefore is the time to go back to: "before", or the form's date
// and time fields.
func rollbackBefore(r *http.Request) (time.Time, error) {
	v := r.FormValue("before")
	if v == "" {
		v = r.FormValue("before_date") + "T" + r.FormValue("before_time")
	}
	return time.ParseInLocation(rollbackTime, v, time.Local)
}

// planRollback fills in rolling a backup back (SPEC.md §10.10): the form,
// the latest check or rollback, and a check that can be acted on.
func (s *Server) planRollback(r *http.Request, m serverRef, e *planEntry) {
	e.RollbackAction = m.path("/backups/plan", "/backups/plan") + "/rollback"
	e.RollbackOpen = r.FormValue("rollback") == e.Ref
	if a := s.activityFor(r, activity.BackupRollbackFor(m.ID, e.Ref)); a != nil && a.Recent(time.Hour) {
		e.RollbackLog = a
		e.RollbackOpen = e.RollbackOpen || a.Running()
	}
	before := time.Now().Add(-24 * time.Hour)
	if p := s.Manager.RollbackChecked(m.ID, e.Ref); p != nil {
		e.RollbackPlan, before = p, p.Before
	} else if t, err := rollbackBefore(r); err == nil {
		before = t
	}
	e.RollbackBefore = before.Local().Format(rollbackTime)
}

// planRollbackSubmit checks what putting one of a server's backups back
// as it was at a time changes ("check"), or, once checked, does it
// ("run"), in the background.
func (s *Server) planRollbackSubmit(w http.ResponseWriter, r *http.Request) {
	if s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	ref := r.FormValue("destination")
	if !s.planRefs(r, m)[ref] {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	before, err := rollbackBefore(r)
	if err != nil {
		s.renderPlanPage(w, r, false, "Choose the date and time to go back to.")
		return
	}
	run := r.FormValue("step") == "run"
	if run {
		err = s.Manager.RollBack(ctx, m.ID, ref, before)
	} else {
		err = s.Manager.CheckRollback(ctx, m.ID, ref, before)
	}
	if msg, ok := usererr.Message(err); ok {
		s.renderPlanPage(w, r, false, msg)
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
	if run {
		s.Store.Audit(ctx, currentUser(r), "backup", "rolled "+m.InSentence()+"'s backups at "+ref+" back to "+before.Format(time.RFC3339))
	}
	redirect(w, r, m.Backups()+"?"+url.Values{"rollback": {ref}}.Encode()+"#plan-rollback-"+strconv.Itoa(s.planNumber(r, m, ref)))
}

// planNumber is a backup's number in a server's plan (2 for the main
// backup, then its copies in order), as backupPlan numbers them.
func (s *Server) planNumber(r *http.Request, m serverRef, ref string) int {
	n := 2
	if t, _, ok := s.Manager.BackupTargetOf(r.Context(), m.ID); ok {
		if t.Provider == ref {
			return n
		}
		n++
	}
	for _, c := range s.Manager.BackupCopiesOf(r.Context(), m.ID) {
		if c.Ref == ref {
			break
		}
		n++
	}
	return n
}
