package web

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/importer"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/units"
)

// Bringing a service's data in from the person's own copy (SPEC.md §8.4):
// one page of numbered steps (the Owner's choice of mock-ups, 2026-10-21:
// docs/mockups/bring-your-own-data), each done step one line with Change,
// each ending with Continue: where the files are, the old setup, what goes
// where, the check, the copy, and the switch, whose result is the last
// step. The service's server keeps the import (node.Imports); the
// controller switches it (manager.SwitchImport). The Owner only.

// Steps of the import page.
const (
	stepSource = 1 + iota
	stepSetup
	stepMatch
	stepCheck
	stepCopy
	stepSwitch
	stepDone
)

// importPage is the import page's data.
type importPage struct {
	Name, Title string
	Service     ServiceView
	Package     *pkgspec.Package
	V           node.ImportView
	// Open is the step shown open, Reached the furthest the person got.
	Open, Reached int
	Source        importSourceForm
	// From is how the old setup is given: inspect, compose or none.
	From  string
	Paths []importPath
	Rows  []importRow
	DBs   []importDBRow
	Libs  []importLibRow
	Left  []string
	// Libraries are the server's, to choose for an old media folder.
	Libraries []libraryOption
	// Placement says where libraries at their old paths go.
	Placement string
	Commands  importCommands
	Items     []importItem
	// Copy's figures, for people.
	CopyText, CopyLeft string
	CopyPercent        int
	Switch             *activityView
	Switching          bool
	// Live: something runs, so the steps refresh themselves.
	Live  bool
	Error string
	// Choose is a folder just chosen in the picker, to confirm.
	Choose *importChoose
}

// importSourceForm is step 1's form.
type importSourceForm struct {
	Where      string
	ServerName string
	Drives     []agent.Drive
	Drive      string
	DriveName  string
	Sub        string
	Folder     string
	// Place is the folder chosen: what it's called and holds, its check.
	Label, Counts string
	PickHref      string
	Check         *agent.Readability
	PlaceError    string
	Roots         []string
	ShareType     string
	ShareServer   string
	SharePath     string
	ShareUser     string
	DrivesError   string
}

// importPath is a prefix line: where an old folder is now.
type importPath struct {
	I        int
	Old, Now string
	NowLabel string
	PickHref string
}

// importRow is one of the package's storage in step 3.
type importRow struct {
	importer.Storage
	FromLabel string
	// Picked is a folder just chosen in the picker (its label and path),
	// kept when the person continues.
	Picked, PickedPath string
	// What it is, for people: "backed up", or "Immich makes it again".
	Kept     string
	Critical bool
	Without  bool
	WhyText  string
	PickHref string
}

// importDBRow is a database in step 3, with the dumps to choose from.
type importDBRow struct {
	importer.Database
	FromLabel string
	Choices   []string
	WhyText   string
}

// importLibRow is an old media folder in step 3.
type importLibRow struct {
	importer.Library
	Title string
}

// libraryOption is one of the server's libraries.
type libraryOption struct{ Name, Title string }

// importCommands are what to run on the old machine.
type importCommands struct {
	Inspect string
	Dumps   []importDump
	Stop    string
	Filled  bool
}

// importDump is a database's dump command.
type importDump struct {
	Name, Command string
}

// importItem is a check's item, its mark's icon chosen.
type importItem struct {
	importer.Item
	Icon string
}

// importChoose is a folder chosen in the picker, for confirming.
type importChoose struct {
	For, Path, Label string
}

// importOf finds the service and its server's imports, for the Owner.
func (s *Server) importOf(w http.ResponseWriter, r *http.Request) (string, node.Imports, bool) {
	name := r.PathValue("name")
	x, err := s.Manager.ImportsOf(name)
	if errors.Is(err, manager.ErrNotFound) || errors.Is(err, node.ErrNotFound) {
		http.NotFound(w, r)
		return "", nil, false
	}
	if msg, ok := userMessage(err); ok {
		http.Error(w, msg, http.StatusBadRequest)
		return "", nil, false
	}
	if err != nil {
		s.serverError(w, err)
		return "", nil, false
	}
	return name, x, true
}

func (s *Server) importPage(w http.ResponseWriter, r *http.Request) {
	s.renderImport(w, r, "", http.StatusOK)
}

// renderImport shows the import page (or its live part), with a mistake.
func (s *Server) renderImport(w http.ResponseWriter, r *http.Request, errMsg string, status int) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v, err := x.Import(ctx, name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	p, err := s.Manager.ServicePackage(name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := s.pageData(r)
	svc := s.serviceRow(r.Context(), name, true)
	pg := importPage{Name: name, Title: cmp.Or(svc.Title, p.Title), Service: svc, Package: p, V: v, Error: errMsg}
	pg.Switching = s.Manager.Switching(name)
	pg.Switch = s.activityFor(r, activity.ImportFor(name))
	pg.Reached = importReached(v, pg.Switching || pg.Switch != nil && pg.Switch.Running())
	pg.Open = pg.Reached
	if n, err := strconv.Atoi(r.URL.Query().Get("step")); err == nil && n >= stepSource && n < pg.Reached {
		pg.Open = n
	}
	if r.Method == http.MethodPost && errMsg != "" {
		if n, err := strconv.Atoi(r.FormValue("step")); err == nil && n >= stepSource && n <= pg.Reached {
			pg.Open = n
		}
	}
	pg.Live = v.Checking || (v.Copy != nil && v.Copy.Running) || pg.Switching || (v.Step != nil && v.Step.Running)
	if err := s.importSource(r, name, &pg); err != nil {
		s.serverError(w, err)
		return
	}
	pg.From = "inspect"
	if v.Setup != nil && v.Setup.From == importer.FromCompose {
		pg.From = "compose"
	} else if v.Setup == nil && v.SetupDone {
		pg.From = "none"
	}
	if f := r.FormValue("from"); r.Method == http.MethodPost && slices.Contains([]string{"inspect", "compose", "none"}, f) {
		pg.From = f
	}
	// A folder just chosen in a picker, for a storage or a prefix line.
	if c := r.URL.Query().Get("for"); c != "" {
		rel := cmp.Or(strings.Trim(r.URL.Query().Get("path"), "/"), ".")
		pg.Choose = &importChoose{For: c, Path: rel, Label: importLabel(v, rel)}
		pg.Open = stepMatch
		if strings.HasPrefix(c, "prefix:") {
			pg.Open = stepSetup
		}
	}
	if r.URL.Query().Has("where") {
		pg.Open = stepSource // the picker, or another drive chosen
	}
	s.importRows(r, &pg)
	data["Import"] = pg
	data["Service"] = svc
	if r.URL.Query().Get("part") == "live" {
		if !pg.Live {
			// Finished: the whole page, so its status and the side
			// panel change too, and the refreshing stops.
			w.Header().Set("HX-Refresh", "true")
			return
		}
		s.renderLive(w, r, "import", "import-steps", pg)
		return
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	s.render(w, "import", data)
}

// importReached is the furthest step the person got to.
func importReached(v node.ImportView, switching bool) int {
	switch {
	case !v.Switched.IsZero():
		return stepDone
	case switching:
		return stepSwitch
	case v.Source == nil:
		return stepSource
	case !v.SetupDone:
		return stepSetup
	case v.Check == nil && !v.Checking && v.CheckErr == "":
		return stepMatch
	case v.Checking || v.CheckErr != "" || !v.Check.Ready():
		return stepCheck
	case v.Copy == nil || v.Copy.Passes == 0:
		return stepCopy
	}
	return stepSwitch
}

// importLabel names a folder of the person's copy: "T7 Shield › immich".
func importLabel(v node.ImportView, rel string) string {
	base := "Your files"
	if v.Source != nil {
		base = v.Source.Title
	}
	if rel == "" || rel == "." {
		return base
	}
	return base + " › " + strings.ReplaceAll(rel, "/", " › ")
}

// importSource fills step 1's form: the drive or folder chosen, as the
// form came back or as the import has it.
func (s *Server) importSource(r *http.Request, name string, pg *importPage) error {
	f := &pg.Source
	f.Roots = agent.LibraryFolderRoots
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		q = r.PostForm
	}
	src := pg.V.Source
	f.Where = q.Get("where")
	if !slices.Contains([]string{"drive", "folder", "share"}, f.Where) {
		f.Where = "drive"
		if src != nil {
			f.Where = src.Kind
		}
	}
	f.Drive, f.Sub, f.Folder = q.Get("drive"), cleanSub(q.Get("sub")), q.Get("folder")
	if q.Get("drive") == "" && q.Get("folder") == "" && src != nil {
		f.Drive, f.Sub, f.Folder = src.Library.Drive, src.Library.Sub, src.Library.Folder
		if sh := src.Library.Share; sh != nil {
			f.ShareType, f.ShareServer, f.SharePath, f.ShareUser = sh.Type, sh.Server, sh.Path, sh.Username
		}
	}
	if t := q.Get("share_type"); t != "" {
		f.ShareType, f.ShareServer, f.SharePath, f.ShareUser = t, q.Get("share_server"), q.Get("share_path"), q.Get("share_user")
	}
	if f.ShareType != "nfs" {
		f.ShareType = "smb"
	}
	n := s.Manager.NodeFor(name)
	if n == nil || s.Libraries == nil {
		return nil
	}
	server := n.Identity().ID
	f.ServerName = n.Identity().Name
	drives, err := s.Libraries.LibraryDrives(r.Context(), server)
	if msg, ok := userMessage(err); ok {
		f.DrivesError = msg
	} else if err != nil {
		return err
	}
	f.Drives = drives
	for _, d := range drives {
		if d.UUID == f.Drive {
			f.DriveName = d.Name
		}
	}
	if f.DriveName == "" && f.Where == "drive" {
		f.Drive, f.Sub = "", ""
		for _, d := range drives {
			if !d.Backup && d.Usable {
				f.Drive, f.DriveName = d.UUID, d.Name
				break
			}
		}
	}
	place := mediaPlace{Drive: f.Drive, DriveName: f.DriveName, Sub: f.Sub}
	if f.Where == "folder" {
		place = mediaPlace{Folder: f.Folder}
		if f.Folder != "" {
			place.Folder = path.Clean("/" + f.Folder)
		}
	}
	if place.Drive == "" && place.Folder == "" {
		return nil
	}
	got, err := s.mediaPlaceAt(r.Context(), server, place)
	if err != nil {
		return err
	}
	f.Label, f.Counts, f.Check, f.PlaceError = got.Label, got.Counts, got.Check, got.Error
	pick := url.Values{}
	if place.Drive != "" {
		pick = url.Values{"drive": {f.Drive}, "name": {f.DriveName}, "sub": {f.Sub}}
	} else {
		pick.Set("folder", place.Folder)
	}
	f.PickHref = "/services/" + url.PathEscape(name) + "/import/folders?" + pick.Encode()
	return nil
}

// importRows fills steps 2 and 3: the prefix lines, what goes where, and
// the commands for the old machine.
func (s *Server) importRows(r *http.Request, pg *importPage) {
	v, p, name := pg.V, pg.Package, pg.Name
	pick := func(forWhat, rel string) string {
		return "/services/" + url.PathEscape(name) + "/import/browse?" + url.Values{"for": {forWhat}, "path": {rel}}.Encode()
	}
	for i, pr := range v.Prefixes {
		pg.Paths = append(pg.Paths, importPath{I: i, Old: pr.Old, Now: pr.Now, NowLabel: importLabel(v, pr.Now),
			PickHref: pick("prefix:"+strconv.Itoa(i), pr.Now)})
	}
	pg.Paths = append(pg.Paths, importPath{I: len(v.Prefixes), Now: ".", NowLabel: importLabel(v, "."), PickHref: pick("prefix:"+strconv.Itoa(len(v.Prefixes)), ".")})
	if c := pg.Choose; c != nil {
		if i, err := strconv.Atoi(strings.TrimPrefix(c.For, "prefix:")); err == nil && i >= 0 && i < len(pg.Paths) {
			pg.Paths[i].Now, pg.Paths[i].NowLabel = c.Path, c.Label
		}
	}
	for _, st := range v.Plan.Storage {
		row := importRow{Storage: st, Critical: st.Class == pkgspec.ClassCritical, Without: slices.Contains(v.Choices.Without, st.Name),
			PickHref: pick("storage:"+st.Name, cmp.Or(st.From, "."))}
		row.Kept = "backed up"
		if !row.Critical {
			row.Kept = pg.Title + " can make it again"
		}
		if st.From != "" {
			row.FromLabel = importLabel(v, st.From)
		}
		if c := pg.Choose; c != nil && c.For == "storage:"+st.Name {
			row.Picked, row.PickedPath = c.Label, c.Path
		}
		row.WhyText = map[string]string{
			importer.WhyLeft:     "Left out, as you chose.",
			importer.WhyNotFound: "Not in your files" + importOldText(st.Old) + ".",
			importer.WhyNotMount: "It wasn't a folder of its own in the old container.",
			importer.WhyNoSetup:  "Choose the folder that holds it.",
		}[st.Why]
		pg.Rows = append(pg.Rows, row)
	}
	for _, db := range v.Plan.Databases {
		row := importDBRow{Database: db}
		if db.From != "" {
			row.FromLabel = importLabel(v, db.From)
		}
		row.WhyText = map[string]string{importer.WhyLeft: "Left out, as you chose.", importer.WhyNotFound: "No dump found in your files."}[db.Why]
		row.Choices = s.dumpChoices(r, pg)
		pg.DBs = append(pg.DBs, row)
	}
	titles := map[string]string{}
	if s.Libraries != nil {
		if n := s.Manager.NodeFor(name); n != nil {
			for _, l := range s.Libraries.ServerLibraryChoices(r.Context(), n.Identity().ID) {
				pg.Libraries = append(pg.Libraries, libraryOption{l.Name, l.Title})
				titles[l.Name] = l.Title
			}
		}
	}
	var at []string
	libs := v.Plan.Libraries
	if pg.Reached == stepDone {
		libs = v.BroughtLibraries // the copy is unmounted now
	}
	for _, l := range libs {
		pg.Libs = append(pg.Libs, importLibRow{Library: l, Title: cmp.Or(titles[l.Library], l.Library)})
		if l.Library != "" {
			at = append(at, l.OldTarget)
		}
	}
	pg.Placement = libraryPlacementText(at, pg.Title)
	for _, l := range v.Plan.Left {
		pg.Left = append(pg.Left, leftText(l))
	}
	// The commands for the old machine, filled in from the old setup.
	c := &pg.Commands
	c.Inspect = "docker inspect $(docker compose ps -aq)"
	var stop []string
	for _, m := range v.Plan.Containers {
		if m.Part != "" {
			oc := oldContainer(v.Setup, m.Old)
			stop = append(stop, cmp.Or(oc.Service, oc.Name))
		}
	}
	if len(stop) > 0 {
		c.Stop, c.Filled = "docker compose stop "+strings.Join(stop, " "), true
	} else {
		c.Stop = "docker compose stop"
	}
	for _, dbn := range slices.Sorted(maps.Keys(p.Databases)) {
		d := p.Databases[dbn]
		container := "<your " + d.Type + " container>"
		for _, m := range v.Plan.Containers {
			if m.Database == dbn {
				container, c.Filled = m.Old, true
			}
		}
		switch {
		case d.Type == pkgspec.DBPostgres && d.BackedUp():
			c.Dumps = append(c.Dumps, importDump{dbn, "docker exec " + container +
				` sh -c 'pg_dump --format=custom --no-owner -U "$POSTGRES_USER" "${POSTGRES_DB:-$POSTGRES_USER}"' > ` + dbn + ".dump"})
		case pkgspec.MySQLFamily(d.Type) && d.BackedUp():
			pre, tool := "MARIADB", "mariadb-dump"
			if d.Type == pkgspec.DBMySQL {
				pre, tool = "MYSQL", "mysqldump"
			}
			c.Dumps = append(c.Dumps, importDump{dbn, "docker exec " + container + " sh -c '" + tool +
				` --single-transaction --routines --triggers --events -u"$` + pre + `_USER" -p"$` + pre + `_PASSWORD" "$` + pre + `_DATABASE"' > ` + dbn + ".sql"})
		}
	}
	// The check, each item with its mark.
	if v.Check != nil {
		for _, it := range v.Check.Items {
			icon := map[string]string{importer.LevelOK: "check", importer.LevelWarn: "warn", importer.LevelBad: "close", importer.LevelInfo: "spark"}[it.Level]
			pg.Items = append(pg.Items, importItem{Item: it, Icon: icon})
		}
	}
	// The copy's figures.
	if cp := v.Copy; cp != nil {
		total := max(cp.Bytes, 1)
		pg.CopyPercent = int(min(100, cp.Progress.Bytes*100/total))
		pg.CopyText = fmt.Sprintf("%s of %s · %s of %s files", sizeText(cp.Progress.Bytes), sizeText(cp.Bytes),
			units.Count(cp.Progress.Files), units.Count(cp.Files))
		if cp.Running && cp.Progress.Copied > 0 {
			elapsed := time.Since(cp.Started)
			rate := float64(cp.Progress.Copied) / elapsed.Seconds()
			if left := time.Duration(float64(cp.Bytes-cp.Progress.Bytes)/rate) * time.Second; rate > 0 && left > 0 {
				pg.CopyLeft = "about " + roughDuration(left) + " left"
			}
		}
	}
}

// importOldText says which old folder wasn't found.
func importOldText(old string) string {
	if old == "" {
		return ""
	}
	return " (the old " + old + ")"
}

// leftText says why an old folder isn't brought.
func leftText(l importer.Left) string {
	why := map[string]string{
		importer.WhyDBFiles: "the old database's own files: never copied, as its dump brings the database",
		importer.WhyFile:    "a single file of the system's: Roostack sets what it's for itself",
		importer.WhyUnused:  "nothing here is kept there",
	}[l.Why]
	return fmt.Sprintf("%s (%s's %s): %s.", l.Source, l.Container, l.Target, why)
}

// libraryPlacementText says where libraries at old paths go (as
// instance.PlaceLibraries decides: a folder of them at a shared parent,
// else each at its own place, the service restarting when its drive
// comes back).
func libraryPlacementText(at []string, title string) string {
	if len(at) == 0 {
		return ""
	}
	parent := ""
	for _, a := range at {
		d := cmp.Or(path.Dir(a), "/")
		if parent == "" {
			parent = d
		} else if parent != d {
			parent = "/"
		}
	}
	if parent != "/" && !slices.Contains([]string{"/app", "/opt", "/srv", "/var"}, parent) {
		return fmt.Sprintf("%s's media folder goes at %s, with each library in it under the name it had. A drive unplugged and plugged back in comes back by itself.", title, parent)
	}
	return fmt.Sprintf("These aren't in one folder, so each library is shown at its own place. If a library's drive is unplugged, %s can't see it again until it restarts: Roostack restarts %s by itself once the drive is back, which takes a few seconds.", title, title)
}

// oldContainer finds an old container by name.
func oldContainer(s *importer.Setup, name string) importer.Container {
	if s != nil {
		for _, c := range s.Containers {
			if c.Name == name {
				return c
			}
		}
	}
	return importer.Container{}
}

// dumpChoices are the dump-like files at the top of the person's copy and
// a folder down, to choose a database's dump from.
func (s *Server) dumpChoices(r *http.Request, pg *importPage) []string {
	x, err := s.Manager.ImportsOf(pg.Name)
	if err != nil || pg.V.Source == nil {
		return nil
	}
	var out []string
	look := func(dir string) []string {
		l, err := x.BrowseImport(r.Context(), pg.Name, dir)
		if err != nil {
			return nil
		}
		for _, f := range l.Files {
			if slices.ContainsFunc([]string{".dump", ".sql", ".pgdump", ".backup"}, func(e string) bool { return strings.HasSuffix(f.Name, e) }) {
				out = append(out, path.Join(dir, f.Name))
			}
		}
		var folders []string
		for _, d := range l.Folders {
			folders = append(folders, path.Join(dir, d.Name))
		}
		return folders
	}
	for i, d := range look(".") {
		if i < 20 {
			look(d)
		}
	}
	slices.Sort(out)
	return out
}

// roughDuration says how long, roughly: "25 minutes", "3 hours".
func roughDuration(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return "a minute"
	case d < 90*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()+0.5))
}

// importFoldersPage is the folder picker for step 1 (a drive's folders, or
// the server's), choosing the folder the person's copy is in.
func (s *Server) importFoldersPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	n := s.Manager.NodeFor(name)
	if n == nil || s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	data := s.pageData(r)
	server := n.Identity().ID
	q := r.URL.Query()
	base := "/services/" + url.PathEscape(name) + "/import"
	at := func(set url.Values) string { return base + "/folders?" + set.Encode() }
	back := func(set url.Values) string { return base + "?" + set.Encode() + "#step-1" }
	var p folderPicker
	var l agent.Library
	var folderHref func(string) string
	if drive := q.Get("drive"); drive != "" {
		dn, sub := q.Get("name"), cleanSub(q.Get("sub"))
		l = agent.Library{Drive: drive, Sub: sub}
		p.Title, p.Label, p.UseText = "Choose a folder on "+dn, dn, "Use "+dn
		p.Crumbs = []mediaCrumb{{dn, at(url.Values{"drive": {drive}, "name": {dn}})}}
		if sub != "" {
			parts := strings.Split(sub, "/")
			for i, part := range parts {
				p.Crumbs = append(p.Crumbs, mediaCrumb{part, at(url.Values{"drive": {drive}, "name": {dn}, "sub": {strings.Join(parts[:i+1], "/")}})})
			}
			p.Label, p.UseText = dn+" › "+strings.Join(parts, " › "), "Use "+parts[len(parts)-1]
		}
		p.CancelHref = back(url.Values{"where": {"drive"}})
		p.UseHref = back(url.Values{"where": {"drive"}, "drive": {drive}, "sub": {sub}})
		folderHref = func(f string) string {
			return at(url.Values{"drive": {drive}, "name": {dn}, "sub": {strings.TrimPrefix(sub+"/"+f, "/")}})
		}
	} else {
		folder := q.Get("folder")
		p.CancelHref = back(url.Values{"where": {"folder"}})
		p.Title = "Choose a folder on " + n.Identity().Name
		p.Crumbs = []mediaCrumb{{n.Identity().Name, at(url.Values{})}}
		if folder == "" {
			for _, root := range agent.LibraryFolderRoots {
				p.Folders = append(p.Folders, pickerFolder{Name: root, Href: at(url.Values{"folder": {root}}), NoCounts: true})
			}
			p.Crumbs[0].Href = ""
			data["Picker"], data["PickerBack"] = p, base
			s.render(w, "import-folders", data)
			return
		}
		folder = path.Clean("/" + folder)
		l = agent.Library{Folder: folder}
		dir := ""
		for _, part := range strings.Split(strings.Trim(folder, "/"), "/") {
			dir += "/" + part
			p.Crumbs = append(p.Crumbs, mediaCrumb{part, at(url.Values{"folder": {dir}})})
		}
		p.Label, p.UseText = folder, "Use "+path.Base(folder)
		p.UseHref = back(url.Values{"where": {"folder"}, "folder": {folder}})
		folderHref = func(f string) string { return at(url.Values{"folder": {path.Join(folder, f)}}) }
	}
	p.Crumbs[len(p.Crumbs)-1].Href = ""
	list, err := s.Libraries.LibraryFolders(r.Context(), server, l)
	if msg, ok := userMessage(err); ok {
		p.Error, p.UseHref = msg, ""
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	for _, f := range list.Folders {
		p.Folders = append(p.Folders, pickerFolder{Name: f.Name, Href: folderHref(f.Name), Folders: f.Folders, Files: f.Files})
	}
	p.Counts = folderCounts(len(list.Folders), list.Files)
	data["Picker"], data["PickerBack"] = p, base
	s.render(w, "import-folders", data)
}

// importBrowsePage is the folder picker inside the person's copy: for a
// storage's folder (for=storage:<name>) or where an old folder is now
// (for=prefix:<n>).
func (s *Server) importBrowsePage(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	data := s.pageData(r)
	v, err := x.Import(r.Context(), name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	q := r.URL.Query()
	forWhat, rel := q.Get("for"), strings.Trim(q.Get("path"), "/")
	if rel == "" {
		rel = "."
	}
	base := "/services/" + url.PathEscape(name) + "/import"
	at := func(p string) string { return base + "/browse?" + url.Values{"for": {forWhat}, "path": {p}}.Encode() }
	top := "Your files"
	if v.Source != nil {
		top = v.Source.Title
	}
	p := folderPicker{Title: "Choose a folder in " + top, Label: importLabel(v, rel), CancelHref: base + "#step-3"}
	if strings.HasPrefix(forWhat, "prefix:") {
		p.CancelHref = base + "#step-2"
	}
	p.Crumbs = []mediaCrumb{{top, at(".")}}
	if rel != "." {
		parts := strings.Split(rel, "/")
		for i, part := range parts {
			p.Crumbs = append(p.Crumbs, mediaCrumb{part, at(strings.Join(parts[:i+1], "/"))})
		}
	}
	p.Crumbs[len(p.Crumbs)-1].Href = ""
	p.UseText = "Use " + path.Base(cmp.Or(strings.TrimPrefix(rel, "."), top))
	p.UseHref = base + "?" + url.Values{"for": {forWhat}, "path": {rel}}.Encode()
	list, err := x.BrowseImport(r.Context(), name, rel)
	if msg, ok := userMessage(err); ok {
		p.Error, p.UseHref = msg, ""
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	for _, f := range list.Folders {
		p.Folders = append(p.Folders, pickerFolder{Name: f.Name, Href: at(path.Join(rel, f.Name)), NoCounts: true})
	}
	p.Counts = folderCounts(len(list.Folders), len(list.Files))
	data["Picker"], data["PickerBack"] = p, base
	s.render(w, "import-folders", data)
}

// importDone audits a change and goes back to the import page.
func (s *Server) importDone(w http.ResponseWriter, r *http.Request, err error, what, anchor string) {
	if msg, ok := userMessage(err); ok {
		s.renderImport(w, r, msg, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	name := r.PathValue("name")
	s.Store.Audit(r.Context(), currentUser(r), "import", name+": "+what)
	http.Redirect(w, r, "/services/"+url.PathEscape(name)+"/import"+anchor, http.StatusSeeOther)
}

func (s *Server) importSourceSubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	src := node.ImportSource{Kind: r.FormValue("where")}
	switch src.Kind {
	case "drive":
		src.Library = agent.Library{Drive: r.FormValue("drive"), Sub: cleanSub(r.FormValue("sub"))}
		src.Title = cmp.Or(r.FormValue("drive_name"), "The drive")
		if src.Library.Sub != "" {
			src.Title += " › " + strings.ReplaceAll(src.Library.Sub, "/", " › ")
		}
		if r.FormValue("as_owner") == "on" {
			uid, _ := strconv.Atoi(r.FormValue("owner_uid"))
			gid, _ := strconv.Atoi(r.FormValue("owner_gid"))
			src.Library.AsOwner = &agent.Owner{UID: uid, GID: gid}
		}
		if src.Library.Drive == "" {
			s.renderImport(w, r, "Choose the drive your files are on.", http.StatusBadRequest)
			return
		}
	case "folder":
		folder := r.FormValue("folder")
		if folder == "" {
			s.renderImport(w, r, "Choose the folder your files are in.", http.StatusBadRequest)
			return
		}
		src.Library = agent.Library{Folder: path.Clean("/" + folder)}
		src.Title = src.Library.Folder
	case "share":
		sh := &agent.Share{Type: r.FormValue("share_type"), Server: strings.TrimSpace(r.FormValue("share_server")),
			Path: strings.TrimSpace(r.FormValue("share_path")), Username: r.FormValue("share_user"), Password: r.FormValue("share_password")}
		if sh.Type == "nfs" {
			sh.Username, sh.Password = "", ""
		}
		src.Library = agent.Library{Share: sh}
		src.Title = sh.Server + ", share " + sh.Path
	default:
		s.renderImport(w, r, "Choose where your files are.", http.StatusBadRequest)
		return
	}
	err := x.SetImportSource(r.Context(), name, src)
	s.importDone(w, r, err, "files at "+src.Title, "#step-2")
}

// maxPaste caps what's pasted: docker inspect of a big project is some
// hundreds of kilobytes.
const maxPaste = 4 << 20

func (s *Server) importSetupSubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPaste)
	if err := r.ParseForm(); err != nil {
		s.renderImport(w, r, "That's more than Roostack reads: paste the old service's containers only.", http.StatusBadRequest)
		return
	}
	var setup *importer.Setup
	switch r.FormValue("from") {
	case "inspect":
		got, err := importer.ParseInspect([]byte(r.FormValue("inspect")))
		if err != nil {
			s.renderImport(w, r, capitalise(err.Error())+".", http.StatusBadRequest)
			return
		}
		setup = &got
	case "compose":
		// Read in the browser by the package editor's Compose reader
		// (compose.js), and checked again here.
		var got importer.Setup
		if err := json.Unmarshal([]byte(r.FormValue("compose_setup")), &got); err != nil || got.From != importer.FromCompose {
			s.renderImport(w, r, "Roostack couldn't read that Compose file. Reading one needs scripts on in your browser: paste what docker inspect prints instead.", http.StatusBadRequest)
			return
		}
		setup = &got
	case "none":
	default:
		s.renderImport(w, r, "Paste docker inspect's output or the Compose file, or choose Neither.", http.StatusBadRequest)
		return
	}
	err := x.SetImportSetup(r.Context(), name, setup)
	what := "no old setup"
	if setup != nil {
		what = "the old setup (" + setup.From + ")"
	}
	s.importDone(w, r, err, what, "#step-2")
}

func (s *Server) importPathsSubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	var prefixes []importer.Prefix
	for i := 0; i < 64; i++ {
		old := strings.TrimSpace(r.FormValue("old." + strconv.Itoa(i)))
		if old != "" {
			prefixes = append(prefixes, importer.Prefix{Old: old, Now: cmp.Or(r.FormValue("now."+strconv.Itoa(i)), ".")})
		}
	}
	err := x.SetImportPrefixes(r.Context(), name, prefixes)
	s.importDone(w, r, err, "where the old folders are now", "#step-3")
}

// importChoicesSubmit keeps step 3's choices: dumps, libraries, leaving
// storage out and starting without it.
func (s *Server) importChoicesSubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	v, err := x.Import(r.Context(), name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	ch := v.Choices
	ch.Storage, ch.Dumps, ch.Libraries = maps.Clone(ch.Storage), maps.Clone(ch.Dumps), maps.Clone(ch.Libraries)
	set := func(m *map[string]string, k, v string) {
		if *m == nil {
			*m = map[string]string{}
		}
		(*m)[k] = v
	}
	for k, vs := range r.PostForm {
		val := vs[0]
		switch {
		case strings.HasPrefix(k, "dump."):
			set(&ch.Dumps, strings.TrimPrefix(k, "dump."), val)
		case strings.HasPrefix(k, "library."):
			set(&ch.Libraries, strings.TrimPrefix(k, "library."), cmp.Or(val, importer.Leave))
		case strings.HasPrefix(k, "pick."):
			set(&ch.Storage, strings.TrimPrefix(k, "pick."), cmp.Or(strings.Trim(val, "/"), "."))
		case strings.HasPrefix(k, "leave."):
			set(&ch.Storage, strings.TrimPrefix(k, "leave."), importer.Leave)
		case strings.HasPrefix(k, "bring."):
			delete(ch.Storage, strings.TrimPrefix(k, "bring."))
		}
	}
	if r.PostForm.Has("without_shown") {
		ch.Without = r.PostForm["without"]
	}
	err = x.SetImportChoices(r.Context(), name, ch)
	if err == nil && r.FormValue("continue") != "" {
		err = x.CheckImport(r.Context(), name)
	}
	s.importDone(w, r, err, "what goes where", "#step-3")
}

func (s *Server) importCheckSubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	s.importDone(w, r, x.CheckImport(r.Context(), name), "checked", "#step-4")
}

func (s *Server) importCopySubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	s.importDone(w, r, x.CopyImport(r.Context(), name), "copying", "#step-5")
}

func (s *Server) importStopSubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	s.importDone(w, r, x.StopImportCopy(r.Context(), name), "stopped the copy", "#step-5")
}

func (s *Server) importSwitchSubmit(w http.ResponseWriter, r *http.Request) {
	name, _, ok := s.importOf(w, r)
	if !ok {
		return
	}
	if r.FormValue("stopped") != "on" {
		s.renderImport(w, r, "Stop the old service first, and tick that it's stopped.", http.StatusBadRequest)
		return
	}
	s.importDone(w, r, s.Manager.SwitchImport(r.Context(), name), "switching to the data brought", "#step-6")
}

func (s *Server) importCancelSubmit(w http.ResponseWriter, r *http.Request) {
	name, x, ok := s.importOf(w, r)
	if !ok {
		return
	}
	if err := x.CancelImport(r.Context(), name); err != nil {
		s.importDone(w, r, err, "", "")
		return
	}
	if r.FormValue("again") != "" {
		// Done, and starting another: the page from step 1.
		s.Store.Audit(r.Context(), currentUser(r), "import", name+": starting again")
		http.Redirect(w, r, "/services/"+url.PathEscape(name)+"/import", http.StatusSeeOther)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "import", name+": cancelled")
	http.Redirect(w, r, "/services/"+url.PathEscape(name), http.StatusSeeOther)
}

// importUnderway says how far bringing a service's data in got, for its
// page: "copying, 18.4 GB of 49.6 GB".
func importUnderway(v node.ImportView) string {
	switch {
	case v.Copy != nil && v.Copy.Running:
		return fmt.Sprintf("copying, %s of %s", sizeText(v.Copy.Progress.Bytes), sizeText(v.Copy.Bytes))
	case v.Copy != nil && v.Copy.Passes > 0:
		return "copied, ready to switch"
	case v.Checking:
		return "checking your files"
	case v.Check != nil && v.Check.Ready():
		return "checked, ready to copy"
	}
	return "under way"
}
