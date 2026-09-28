package web

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/store"
)

// A server's backup plan (SPEC.md §6.9), on the Backups page and in the
// setup wizard alike: numbered backups (1 is the main one, which restores
// read first; the others are copies), an empty slot for each up to three,
// and one chooser for adding a backup anywhere.

// planSlots is how many numbered places the plan shows.
const planSlots = 3

// planEntry is one of a server's backups.
type planEntry struct {
	Number                 int
	Ref, Title, Repository string
	Kind, KindTitle        string // manager.Kind*
	AtHome, Away, Main     bool
	Last, LastOK           *store.BackupRun
	CanPromote             bool
	// AskWhere are the servers whose location decides at home or away.
	AskWhere []locationAsk
}

// planSlot is an empty place in the plan, with a suggestion for it.
type planSlot struct {
	Number  int
	Title   string
	Suggest string
}

// planChecks are the 3-2-1 checks.
type planChecks struct {
	BackedUp, TwoKinds, Away bool
}

// planView is a server's plan for the page.
type planView struct {
	Server  serverRef
	Wizard  bool
	Entries []planEntry
	Slots   []planSlot
	More    int // the number an "Add another backup" would get
	Checks  planChecks
	// Hint suggests a change to the plan (e.g. an at-home copy as backup 1).
	Hint string
	// Adding is the number whose chooser is open (0 if none); Replacing:
	// it replaces backup 1.
	Adding    int
	Replacing bool
	Options   planOptions
	Busy      *activityView
	Error     string
	// Next is when the next backup runs (zero if unknown).
	Next time.Time
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
	p := &planView{Server: m, Wizard: wizard, Back: m.Backups(), Action: m.path("/backups/plan", "/backups/plan")}
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
	used := map[string]bool{}
	add := func(e planEntry) {
		e.Number = len(p.Entries) + 1
		e.Kind = s.Manager.BackupKind(ctx, m.ID, e.Ref)
		e.KindTitle = kindTitle(e.Kind)
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
			e.Last, e.LastOK = st.Last, st.LastOK
		}
		add(e)
	}
	for _, c := range s.Manager.BackupCopiesOf(ctx, m.ID) {
		e := planEntry{Ref: c.Ref, Title: c.Title, Repository: c.Repository}
		if st != nil {
			for _, cs := range st.Copies {
				if cs.Name == c.Ref {
					e.Last, e.LastOK = cs.Last, cs.LastOK
				}
			}
		}
		add(e)
	}

	// 3-2-1: backed up, two kinds of storage, one away from home.
	families := map[string]bool{}
	for _, e := range p.Entries {
		families[manager.StorageFamily(e.Kind)] = true
		p.Checks.Away = p.Checks.Away || e.Away
	}
	p.Checks.TwoKinds = len(families) >= 2
	if len(p.Entries) > 0 && p.Entries[0].LastOK != nil && !isStale(p.Entries[0].LastOK.Finished) {
		p.Checks.BackedUp = true
	}
	// Backup 1 is best at home: restores from it are quick.
	if len(p.Entries) > 1 && !p.Entries[0].AtHome {
		for i := range p.Entries[1:] {
			if e := &p.Entries[i+1]; e.AtHome {
				p.Hint = "Backup 1 is the one restores use first, so it's best at home, where restoring is quick: consider making " +
					e.Title + " backup 1."
				break
			}
		}
	}
	for i := range p.Entries {
		p.Entries[i].CanPromote = i > 0
	}

	// Empty slots, each with a suggestion from what's there.
	for n := len(p.Entries) + 1; n <= planSlots; n++ {
		slot := planSlot{Number: n, Title: "Add a backup"}
		// Only the next empty slot suggests what's missing.
		switch {
		case n > len(p.Entries)+1:
			slot.Suggest = "Optional: an extra copy, anywhere you like."
		case len(p.Entries) == 0 && n == 1:
			slot.Suggest = "Suggested: a drive at home, like a USB drive or a NAS. Restoring from it is quick."
		case !p.Checks.Away:
			slot.Title, slot.Suggest = "Add a backup away from home",
				"Suggested: online storage, or space at a friend's lab. It survives a fire or theft at home."
		case !p.Checks.TwoKinds:
			slot.Title, slot.Suggest = "Add a different kind of backup",
				"Suggested: a kind of storage you don't use yet, so one failing doesn't take the others with it."
		default:
			slot.Suggest = "An extra copy, anywhere you like."
		}
		if n > 1 && len(p.Entries) == 0 {
			slot.Suggest = "Add backup 1 first."
		}
		p.Slots = append(p.Slots, slot)
	}
	p.More = len(p.Entries) + 1

	// The chooser.
	// Backups are added in order: only the next one.
	if n, _ := strconv.Atoi(r.FormValue("add")); n == len(p.Entries)+1 {
		p.Adding = n
	}
	p.Replacing = r.FormValue("replace") == "1" && hasMain
	if p.Replacing {
		p.Adding = 1
	}
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
		if wizard && s.Setup != nil {
			if err := s.Setup.BackupAdded(ctx, !hasMain); err != nil {
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
