package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
)

// A server's backup plan (SPEC.md §6.9), on the Backups page and in the
// setup wizard alike, numbered as the 3-2-1 rule counts copies: 1 is the
// data on the server itself, 2 the main backup (restores read it first),
// 3 a second backup; any more are optional extras, not numbered. Empty
// places show what to add, and one chooser adds a backup anywhere.

// planCopies is how many numbered copies the plan shows (the 3 of 3-2-1).
const planCopies = 3

// planEntry is one of a server's backups.
type planEntry struct {
	// Number is its place in the plan (2 for the main backup); Extra: past
	// the 3 copies, shown without a number.
	Number                 int
	Extra                  bool
	Ref, Title, Repository string
	Kind, KindTitle        string // manager.Kind*
	AtHome, Away, Main     bool
	Last, LastOK           *store.BackupRun
	Retry                  *backup.Retry // the next try after a failure
	CanPromote             bool
	// AskWhere are the servers whose location decides at home or away.
	AskWhere []locationAsk
	// Space, where known: Stored is what the backups there take (from
	// the last backup); Free the free space on its drive (-1 unknown);
	// Lab the whole lab's use of a friend's lab's space (nil unknown).
	Stored int64
	Free   int64
	// FreeOn names where Free is ("this server's disk", "the drive",
	// "vps's disk"); LowSpace is lowSpace's level for it.
	FreeOn, LowSpace string
	Lab              *labnet.GuestUsage
	// Path says how backups reach another server's disk: manager.PathDirect
	// or PathHub (through the controller, named by Via); "" otherwise.
	// For PathHub, Host and HostName are that server, and LinkFailed says
	// a direct link was tried and didn't connect.
	Path, Via      string
	Host, HostName string
	LinkFailed     bool
}

// planSlot is an empty place in the plan, with a suggestion for it.
type planSlot struct {
	Number  int
	Extra   bool
	Title   string
	Suggest string
}

// planChecks are the 3-2-1 checks.
type planChecks struct {
	Copies, TwoKinds, Away, BackedUp bool
}

// planView is a server's plan for the page.
type planView struct {
	Server  serverRef
	Wizard  bool
	Entries []planEntry
	Slots   []planSlot
	More    int // the number the next backup gets
	Checks  planChecks
	// Hint suggests a change to the plan (e.g. an at-home copy as the main
	// backup).
	Hint string
	// Adding is the number whose chooser is open (0 if none); Replacing:
	// it replaces the main backup. ChooserTitle and ChooserButton say so.
	Adding                      int
	Replacing                   bool
	ChooserTitle, ChooserButton string
	ChooserNum                  planSlot
	Options                     planOptions
	Busy                        *activityView
	Error                       string
	// Next is when the next backup runs (zero if unknown).
	Next time.Time
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
}

func kindTitle(kind string) string {
	return map[string]string{manager.KindUSB: "USB drive", manager.KindDisk: "Second disk", manager.KindNetwork: "Network share",
		manager.KindSystem: "This server's disk", manager.KindFolder: "Folder", manager.KindOnline: "Online storage",
		manager.KindFriend: "A friend's lab", manager.KindServer: "Another Roostack instance"}[kind]
}

// backupPlan builds a server's plan. adding and replacing open the
// chooser (from the page's query).
func (s *Server) backupPlan(r *http.Request, m serverRef, wizard bool) *planView {
	if s.Manager == nil {
		return nil
	}
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
	if m.This && s.Setup != nil {
		if ds, err := s.Setup.BackupDrives(ctx); err == nil {
			for _, d := range ds {
				drives[d.ID] = d
			}
		}
	}
	used := map[string]bool{}
	add := func(e planEntry) {
		e.Number = len(p.Entries) + 2
		e.Extra = e.Number > planCopies
		e.Kind = s.Manager.BackupKind(ctx, m.ID, e.Ref)
		e.KindTitle = kindTitle(e.Kind)
		e.Free = -1
		if e.LastOK != nil {
			e.Stored = int64(e.LastOK.Stored)
		}
		if d, ok := drives[driveOf(e.Ref, e.Kind)]; ok && m.This {
			e.Free, e.FreeOn, e.LowSpace = d.Free, "the drive", lowSpace(d.Free, d.Size)
			if e.Kind == manager.KindSystem {
				e.FreeOn = "this server's disk"
			}
		}
		if host, ok := strings.CutPrefix(e.Ref, "server:"); ok {
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
			if d, ok := s.Manager.NodeByID(host).(node.Disks); ok {
				dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				if disks, err := d.DiskSpaces(dctx); err == nil {
					if ds, ok := node.StoreDisk(disks); ok {
						e.Free, e.FreeOn, e.LowSpace = ds.Free, "that disk", lowSpace(ds.Free, ds.Total)
					}
				}
				cancel()
			}
		}
		e.AtHome, e.Away = manager.AtHome(e.Kind), manager.Away(e.Kind)
		if e.Kind == manager.KindServer {
			e.AtHome, e.Away, e.AskWhere = s.backupPlace(ctx, m, strings.TrimPrefix(e.Ref, "server:"))
		}
		p.Entries = append(p.Entries, e)
		used[e.Ref] = true
	}
	main, mainTitle, hasMain := s.Manager.BackupTargetOf(ctx, m.ID)
	if hasMain {
		e := planEntry{Ref: main.Provider, Title: mainTitle, Repository: main.Repository, Main: true}
		if st != nil {
			e.Last, e.LastOK, e.Retry = st.Last, st.LastOK, st.Retry
		}
		add(e)
	}
	for _, c := range s.Manager.BackupCopiesOf(ctx, m.ID) {
		e := planEntry{Ref: c.Ref, Title: c.Title, Repository: c.Repository}
		if st != nil {
			for _, cs := range st.Copies {
				if cs.Name == c.Ref {
					e.Last, e.LastOK, e.Retry = cs.Last, cs.LastOK, cs.Retry
				}
			}
		}
		add(e)
	}

	// 3-2-1: three copies (the data here and two backups), two kinds of
	// storage (the data here is on a disk), one away from home; and backed
	// up lately.
	p.Checks.Copies = len(p.Entries) >= planCopies-1
	for _, e := range p.Entries {
		p.Checks.TwoKinds = p.Checks.TwoKinds || manager.StorageFamily(e.Kind) != manager.StorageFamily(manager.KindSystem)
		p.Checks.Away = p.Checks.Away || e.Away
	}
	if len(p.Entries) > 0 && p.Entries[0].LastOK != nil && !isStale(p.Entries[0].LastOK.Finished) {
		p.Checks.BackedUp = true
	}
	// The main backup is best at home: restores from it are quick. (A cloud
	// server has no home to suggest.)
	if len(p.Entries) > 1 && !p.Entries[0].AtHome && !p.Internet {
		for i := range p.Entries[1:] {
			if e := &p.Entries[i+1]; e.AtHome {
				p.Hint = "Restores use the main backup first, so it's best at home, where restoring is quick: consider making " +
					e.Title + " the main backup."
				break
			}
		}
	}
	for i := range p.Entries {
		p.Entries[i].CanPromote = i > 0
	}

	// Empty places, each with a suggestion from what's there; only the
	// next one can be added.
	p.More = len(p.Entries) + 2
	need := func() (title, suggest string) {
		switch {
		case !p.Checks.Away && p.Internet:
			return "A backup away from this server", "Suggested: online storage with another company, or space at your own or a friend's lab. " +
				"It survives losing this server, or your account with its provider."
		case !p.Checks.Away:
			return "A backup away from home", "Suggested: online storage, or space at a friend's lab. It survives a fire or theft at home."
		case !p.Checks.TwoKinds:
			return "A different kind of storage", "Suggested: online storage, or a friend's lab: not a disk like the one your data is on."
		}
		return "", ""
	}
	for n := p.More; n <= planCopies; n++ {
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
			if slot.Title, slot.Suggest = need(); slot.Title == "" {
				slot.Title, slot.Suggest = "A second backup", "Anywhere you like, so losing one backup still leaves another."
			}
		}
		p.Slots = append(p.Slots, slot)
	}
	// Past the three copies, once they're all there: an optional extra
	// backup.
	if p.More > planCopies {
		extra := planSlot{Extra: true, Number: p.More, Title: "An extra backup (optional)"}
		if title, suggest := need(); title != "" {
			extra.Title, extra.Suggest = title+" (optional)", suggest
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
	case p.Adding == 2:
		p.ChooserTitle, p.ChooserButton = "Where should your main backup go?", "Add the main backup"
	case p.Adding > planCopies:
		p.ChooserTitle, p.ChooserButton = "Where should the extra backup go?", "Add the extra backup"
	default:
		p.ChooserTitle, p.ChooserButton = "Where should backup "+strconv.Itoa(p.Adding)+" go?", "Add backup "+strconv.Itoa(p.Adding)
	}
	p.ChooserNum = planSlot{Number: p.Adding, Extra: p.Adding > planCopies}
	if p.Adding > 0 {
		p.Options = s.planOptions(r, m, main, hasMain, used, wizard)
	}
	// Setting a backup up: while it runs, and if it failed lately (so the
	// reason stays in view).
	if a := s.activityFor(r, activity.BackupSetupFor(m.ID)); a != nil && (a.Running() || a.Err != "" && time.Since(a.Finished) < time.Hour) {
		p.Busy = a
	}
	return p
}

// driveOf is the drive (agent.Drive.ID) a backup is on, if on one of the
// server's drives.
func driveOf(ref, kind string) string {
	if id, ok := strings.CutPrefix(ref, "drive:"); ok {
		return id
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
	if m.This && s.Setup != nil {
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
		case manager.KindOnline:
			o.Online = append(o.Online, c)
		case manager.KindFriend:
			o.Friends = append(o.Friends, c)
		case manager.KindServer:
			// A copy can't share the main backup's store.
			if !wizard && !(hasMain && c.Ref == "server:"+main.StoreHost) {
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
		if s.Manager == nil {
			http.NotFound(w, r)
			return
		}
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
		if msg, ok := userMessage(err); ok {
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
		keyIsNew := s.Setup != nil && !s.hasRecoveryKey()
		if !hasMain || r.FormValue("replace") == "1" {
			err = s.Manager.SetBackupDestination(ctx, m.ID, ref)
		} else {
			err = s.Manager.AddBackupCopy(ctx, m.ID, ref)
		}
		if msg, ok := userMessage(err); ok {
			fail(msg)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		// In the wizard, backup 1 shows the key (the same one, if backups
		// were set up before); anywhere, a key made just now is shown.
		if s.Setup != nil && (wizard || keyIsNew) {
			if err := s.Setup.BackupAdded(ctx, (wizard && !hasMain) || keyIsNew); err != nil {
				s.serverError(w, err)
				return
			}
		}
		s.Store.Audit(ctx, currentUser(r), "backup", "added a backup for "+m.Label()+": "+ref)
		redirect(w, r, back+"#plan")
	}
}

// resolveChoice turns the chooser's fields into a destination reference,
// adding an online storage account or another lab's storage first when
// their details were given.
func (s *Server) resolveChoice(r *http.Request) (string, error) {
	ctx := r.Context()
	switch r.FormValue("choice") {
	case "drive":
		if d := r.FormValue("drive"); strings.HasPrefix(d, "server:") {
			return d, nil // another server's own disk
		} else if d != "" {
			return "drive:" + d, nil
		}
		if f := strings.TrimSpace(r.FormValue("folder")); f != "" {
			return "folder:" + f, nil
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
	if s.Manager == nil || s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	err := s.Manager.PromoteBackupCopy(r.Context(), m.ID, r.FormValue("destination"))
	if msg, ok := userMessage(err); ok {
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
	s.Store.Audit(r.Context(), currentUser(r), "backup", "made "+r.FormValue("destination")+" backup 1 for "+m.Label())
	redirect(w, r, m.Backups()+"#plan")
}

// planRemoveSubmit stops one of a server's backups (not backup 1: another
// takes its place first). What's there already stays.
func (s *Server) planRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil || s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	ref := r.FormValue("destination")
	err := s.Manager.RemoveBackupCopy(r.Context(), m.ID, ref)
	if msg, ok := userMessage(err); ok {
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
	s.Store.Audit(r.Context(), currentUser(r), "backup", "stopped a backup for "+m.Label()+": "+ref)
	redirect(w, r, m.Backups()+"#plan")
}

// renderPlanPage shows the page the plan is on, with an error.
func (s *Server) renderPlanPage(w http.ResponseWriter, r *http.Request, wizard bool, msg string) {
	if wizard {
		s.renderBackupStep(w, r, map[string]any{"Error": msg})
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Error"] = msg
	s.renderBackups(w, r, data)
}
