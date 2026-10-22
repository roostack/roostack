package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/importer"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/units"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// The check before anything is copied (SPEC.md §8.4, docs/MIGRATION-PLAN.md
// §3.6): what each folder holds and what Roostack can't read, room for it,
// each dump against the package's own database image, the old images and
// settings against the package's, old paths left in the data, and data
// that was in no folder before. Nothing changes.

// spaceToSpare is how much more than the copy there must be room for.
const spaceToSpare = 1.1

func (n *Local) CheckImport(ctx context.Context, service string) error {
	s, err := n.importService(service)
	if err != nil {
		return err
	}
	if st := n.loadImport(ctx, service); st.Source == nil {
		return usererr.New("Say where your files are first.")
	}
	if err := n.updateImport(ctx, service, func(st *ImportState) error {
		st.Checking, st.CheckErr = true, ""
		return nil
	}); err != nil {
		return err
	}
	return n.startImportRun(service, func(ctx context.Context) {
		var act *activity.Log
		if n.Activity != nil {
			act = n.Activity.Start(activity.ImportRunFor(service), "Checking what's brought into "+s.Title())
		}
		report, err := n.runImportCheck(ctx, s, act)
		act.Finish(err)
		n.updateImport(context.WithoutCancel(ctx), service, func(st *ImportState) error {
			st.Checking = false
			if err != nil {
				st.CheckErr = err.Error()
				return nil
			}
			st.Check = report
			return nil
		})
	})
}

// runImportCheck works out the check's report.
func (n *Local) runImportCheck(ctx context.Context, s state.Service, act *activity.Log) (*importer.Report, error) {
	service, title, p := s.Name(), s.Title(), s.Package
	st := n.loadImport(ctx, service)
	files := importFiles(service, st)
	if files == nil {
		return nil, fmt.Errorf("your files aren't mounted at %s", filepath.Join(importRoot, service))
	}
	base := filepath.Join(importRoot, service)
	plan := importer.Match(p, st.Setup, st.Prefixes, st.Choices, files, n.libraryFinder(s, files))
	r := &importer.Report{}
	add := func(level, text, more string) *importer.Item {
		r.Items = append(r.Items, importer.Item{Level: level, Text: text, More: more})
		return &r.Items[len(r.Items)-1]
	}

	// Each folder brought: what it holds, and what can't be read.
	need := map[string]int64{} // a disk's folder → bytes the copy needs on it
	var sqlite []string        // SQLite databases brought (for old paths)
	for _, sp := range plan.Storage {
		if sp.From == "" {
			n.notBrought(sp, st, title, add)
			continue
		}
		act.Step("Looking through %s", strings.ToLower(sp.Title[:1])+sp.Title[1:])
		dir := importer.Rel(base, sp.From)
		c, err := importer.Scan(ctx, dir, sp.Exclude, func(c importer.Count) {
			act.Printf("%s: %d files so far", sp.Title, c.Files)
		})
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", sp.From, err)
		}
		r.Files += c.Files
		r.Bytes += c.Bytes
		disk := n.Lab.Paths.ServicesDir
		if sp.Media {
			disk = n.Lab.Paths.MediaDir
		}
		need[disk] += c.Bytes
		for _, db := range c.SQLite {
			sqlite = append(sqlite, filepath.Join(dir, filepath.FromSlash(db)))
		}
		head := fmt.Sprintf("%s: %s, %s.", sp.Title, words.Count(c.Files, "file", "files"), units.Bytes(c.Bytes))
		if c.Unreadable > 0 {
			it := add(importer.LevelBad, fmt.Sprintf("%s Roostack can't read, in %s:", words.Count(c.Unreadable, "file or folder", "files and folders"),
				pathFor(sp.From)),
				fmt.Sprintf("for example %s. Only their owner (%s) can read them. Make them readable on your copy, then check again:",
					pathFor(path.Join(sp.From, c.Example)), ownerText(c.Owner)))
			it.Code = "sudo chmod -R a+rX " + shellQuote(path.Base(cmpPath(sp.From, st.Source)))
			continue
		}
		more := "All readable."
		if c.Links > 0 {
			more += fmt.Sprintf(" %s in it %s copied as %s, never followed.", words.Count(c.Links, "link", "links"), words.Plural(int(c.Links), "is", "are"),
				words.Plural(int(c.Links), "a link", "links"))
		}
		if c.Special > 0 {
			more += fmt.Sprintf(" %s (sockets, devices) aren't copied.", words.Count(c.Special, "special file", "special files"))
		}
		add(importer.LevelOK, head, more)
	}

	// Room for it, with some to spare.
	for _, disk := range slices.Sorted(maps.Keys(need)) {
		want := int64(float64(need[disk]) * spaceToSpare)
		free := freeSpace(disk)
		where := "this server's disk"
		if disk == n.Lab.Paths.MediaDir && n.Lab.Paths.MediaDir != n.Lab.Paths.ServicesDir {
			where = title + "'s media disk"
		}
		if free >= 0 && free < want {
			add(importer.LevelBad, "Not enough room.", fmt.Sprintf("It needs %s with 10%% to spare, and %s has %s free. Free some space, or add a bigger disk.",
				units.Bytes(want), where, units.Bytes(free)))
		} else if free >= 0 {
			add(importer.LevelOK, "Room for it.", fmt.Sprintf("It needs %s with 10%% to spare, and %s has %s free.", units.Bytes(want), where, units.Bytes(free)))
		}
	}

	// Each dump, against the package's own database image.
	var dumps []string // Postgres dumps brought (for old paths)
	for _, db := range plan.Databases {
		d := p.Databases[db.Name]
		image := databaseImage(d)
		label := fmt.Sprintf("Database %q", db.Name)
		if db.From == "" {
			if db.Why == importer.WhyLeft {
				add(importer.LevelWarn, label+": not brought.", title+" starts with an empty database.")
			} else {
				add(importer.LevelBad, label+": no dump found.", "Take a dump on the old machine (the commands are beside these steps) and put it with your files, or choose it in step 3.")
			}
			continue
		}
		file := importer.Rel(base, db.From)
		act.Step("Reading the dump of database %s", db.Name)
		if d.Type == pkgspec.DBPostgres {
			n.checkPgDump(ctx, act, label, title, file, image, add)
			dumps = append(dumps, file)
		} else {
			head := make([]byte, 4096)
			if f, err := os.Open(file); err == nil {
				k, _ := f.Read(head)
				head = head[:k]
				f.Close()
			}
			if kind := importer.SQLDump(head); kind != "" {
				add(importer.LevelOK, fmt.Sprintf("%s: a %s dump.", label, kind), "Loaded as Roostack's restores load one.")
			} else {
				add(importer.LevelBad, label+" isn't a dump Roostack can load.",
					"It should be what mariadb-dump (or mysqldump) prints, as SQL, not compressed.")
			}
		}
	}

	// The old images, against the package's.
	if st.Setup == nil {
		add(importer.LevelInfo, fmt.Sprintf("Bring data from %s %s, the version this lab runs.", title, appVersionOf(p)), "")
	}
	for _, m := range plan.Containers {
		if m.Part == "" {
			continue
		}
		oc := st.Setup.Container(m.Old)
		newImage := p.Containers[m.Part].Image
		switch importer.CompareImages(oc.Image, newImage) {
		case importer.SameImage:
			add(importer.LevelOK, "The same version.", fmt.Sprintf("The old %s ran %s, as %s does here.", m.Old, importer.TagVersion(oc.Image), title))
		case importer.OlderImage:
			add(importer.LevelWarn, "An older version.", fmt.Sprintf("The old %s ran %s, and %s here is %s. %s upgrades its data as it first starts, if it can make the jump: read its release notes first.",
				m.Old, importer.TagVersion(oc.Image), title, importer.TagVersion(newImage), title))
		case importer.NewerImage:
			add(importer.LevelWarn, "A newer version.", fmt.Sprintf("The old %s ran %s, newer than %s here (%s): data from a newer version is likely not to work. Wait for the package to catch up, or update the lab's.",
				m.Old, importer.TagVersion(oc.Image), title, importer.TagVersion(newImage)))
		case importer.OtherImage:
			add(importer.LevelWarn, "Another image.", fmt.Sprintf("The old %s ran %s, and %s here runs %s: its folders and users may differ.",
				m.Old, importer.Repository(oc.Image), title, importer.Repository(newImage)))
		default:
			add(importer.LevelInfo, "Versions that can't be compared.", fmt.Sprintf("The old %s ran %s; %s here is %s.", m.Old, oc.Image, title, importer.TagVersion(newImage)))
		}
		if oc.NVIDIA {
			add(importer.LevelInfo, "An NVIDIA graphics card isn't used here.", m.Old+" used one; Roostack doesn't support NVIDIA cards, so the processor does the work.")
		}
		// Settings the package doesn't set, leaving out the image's own.
		c := p.Containers[m.Part]
		set := slices.Collect(maps.Keys(c.Env))
		set = append(set, slices.Collect(maps.Keys(c.Secrets))...)
		set = append(set, "TZ")
		// Its image is fetched now if need be (the switch would anyway), so
		// its own defaults aren't reported as the old container's.
		act.Step("Fetching %s's image to compare its settings", title)
		n.pullImage(ctx, act, newImage)
		if diff := importer.EnvDiff(oc.Env, set, n.imageEnv(ctx, newImage)); len(diff) > 0 {
			it := add(importer.LevelWarn, fmt.Sprintf("%s the old %s had that Roostack doesn't set:", words.Count(int64(len(diff)), "setting", "settings"), m.Old),
				"compare them with "+title+"'s settings here, and its own settings page. A config file's settings belong in "+title+"'s own settings before you move, or they're lost.")
			it.List = diff
		}
	}

	// Old paths left in the data: container paths the old app had that the
	// new container won't (where libraries go, they will).
	needles, shown := oldPaths(p, st.Setup, plan)
	if len(needles) > 0 && (len(dumps) > 0 || len(sqlite) > 0) {
		act.Step("Looking for old folders named in the data")
		counts := map[string]int{}
		for _, f := range dumps {
			n.countInPgDump(ctx, f, databaseImageOf(p), needles, counts)
		}
		for _, f := range sqlite {
			if fh, err := os.Open(f); err == nil {
				got, _ := importer.CountPaths(fh, needles)
				fh.Close()
				for k, v := range got {
					counts[k] += v
				}
			}
		}
		left := false
		for _, nd := range needles {
			at := strings.TrimSuffix(nd, "/")
			switch {
			case counts[nd] == 0:
			case shown[at] != "":
				add(importer.LevelOK, fmt.Sprintf("The data names %s %s times:", nd, units.Count(int64(counts[nd]))),
					"a media library is shown there, so they're right as they are.")
			default:
				left = true
				add(importer.LevelWarn, fmt.Sprintf("Old folders left in the data: %s %s times,", nd, units.Count(int64(counts[nd]))),
					fmt.Sprintf("and %s has no %s here. An app may change its own paths when its folder moves (Immich does); otherwise give a media library that place, or change the paths in the app.", title, at))
			}
		}
		if !left {
			add(importer.LevelOK, "No old folders left in the data.", "The data names only folders "+title+" has here.")
		}
	}

	// The service's data now: set aside at the switch, not lost.
	if s.Instance.AwaitingData {
		add(importer.LevelOK, title+" has no data yet,", "so nothing is replaced.")
	} else if size, err := n.DataSize(ctx, service); err == nil && size > 1<<20 {
		add(importer.LevelWarn, fmt.Sprintf("%s has data already (%s).", title, units.Bytes(size)),
			"Switching sets it aside, kept until you delete it, so you can put it back.")
	}
	r.At = time.Now()
	return r, nil
}

// notBrought says what it means that a storage isn't brought.
func (n *Local) notBrought(sp importer.Storage, st ImportState, title string, add func(level, text, more string) *importer.Item) {
	critical := sp.Class == pkgspec.ClassCritical && !slices.Contains(st.Choices.Without, sp.Name)
	switch {
	case sp.Why == importer.WhyNotMount && !critical:
		add(importer.LevelInfo, sp.Title+": not a folder of its own before,",
			fmt.Sprintf("so it was inside the old container, in no folder. %s makes it again.", title))
	case sp.Why == importer.WhyNotMount:
		add(importer.LevelBad, sp.Title+": not a folder of its own before,",
			fmt.Sprintf("so it was inside the old container (at %s), in no folder you could copy. Copy it out of the old container (docker cp), or tick Start without it in step 3.", sp.Path))
	case !critical:
		add(importer.LevelInfo, sp.Title+": not brought.", title+" makes it again.")
	case slices.Contains(st.Choices.Without, sp.Name):
		add(importer.LevelWarn, sp.Title+": not brought.", title+" starts without it, as you chose.")
	default:
		add(importer.LevelBad, sp.Title+": not brought.", "Choose a folder for it in step 3, or tick Start without it there.")
	}
}

// checkPgDump reads a Postgres dump with the package's own image: the
// Postgres it came from (newer than the image's can't be loaded) and the
// extensions it creates (the image must have them).
func (n *Local) checkPgDump(ctx context.Context, act *activity.Log, label, title, file, image string,
	add func(level, text, more string) *importer.Item) {
	f, err := os.Open(file)
	if err != nil {
		add(importer.LevelBad, label+": Roostack can't read its dump.", unwrapPathError(err).Error()+".")
		return
	}
	defer f.Close()
	n.pullImage(ctx, act, image)
	list, err := n.podman(ctx, f, "run", "--rm", "-i", "--network=none", "--entrypoint", "pg_restore", image, "--list")
	if err != nil {
		if strings.Contains(string(list), "unsupported version") {
			add(importer.LevelBad, label+" comes from a newer Postgres than "+title+"'s here.",
				"Its file format is too new for this one to read. Data can't go to an older Postgres: wait for a package that has a newer one.")
		} else {
			add(importer.LevelBad, label+" isn't a dump Roostack can load.",
				"It should be what pg_dump --format=custom writes (Immich's own nightly backups, .sql.gz, can't be loaded yet). "+words.FirstLine(strings.TrimSpace(string(list))))
		}
		return
	}
	d, err := importer.ParsePgList(string(list))
	if err != nil {
		add(importer.LevelBad, label+" isn't a dump Roostack can load:", err.Error()+". Take it again with pg_dump --format=custom.")
		return
	}
	ver, _ := n.podman(ctx, nil, "run", "--rm", "--network=none", "--entrypoint", "postgres", image, "--version")
	have := importer.PgMajor(string(ver))
	if have > 0 && d.Major > have {
		add(importer.LevelBad, fmt.Sprintf("%s comes from Postgres %s, newer than %s's here (%v).", label, d.Version, title, have),
			"Data can't go to an older Postgres: wait for a package that has a newer one.")
		return
	}
	ls, _ := n.podman(ctx, nil, "run", "--rm", "--network=none", "--entrypoint", "sh", image, "-c", "ls /usr/share/postgresql/*/extension/ /usr/local/share/postgresql/extension/ 2>/dev/null")
	var missing []string
	for _, e := range d.Extensions {
		if !bytes.Contains(ls, []byte(e+".control")) {
			missing = append(missing, e)
		}
	}
	if len(missing) > 0 {
		add(importer.LevelBad, fmt.Sprintf("%s needs %s, which %s's database here doesn't have.", label, words.And(missing), title),
			"Move the old app off it first, by its own upgrade steps (Immich's from pgvecto.rs to VectorChord, say), then take the dump again.")
		return
	}
	more := fmt.Sprintf("%s's database here (%v) can load it.", title, have)
	if len(d.Extensions) > 0 {
		more += fmt.Sprintf(" It needs %s: all there.", words.And(d.Extensions))
	}
	add(importer.LevelOK, fmt.Sprintf("%s: a dump from Postgres %s,", label, d.Version), more)
}

// countInPgDump counts old paths in a Postgres dump, read as text.
func (n *Local) countInPgDump(ctx context.Context, file, image string, needles []string, counts map[string]int) {
	if image == "" {
		return
	}
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	cmd := n.Launch(ctx, nil, "podman", "run", "--rm", "-i", "--network=none", "--entrypoint", "pg_restore", image, "-f", "-")
	cmd.Stdin = f
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	got, _ := importer.CountPaths(pipe, needles)
	io.Copy(io.Discard, pipe)
	cmd.Wait()
	for k, v := range got {
		counts[k] += v
	}
}

// oldPaths are the old app containers' paths (with a trailing /) that the
// new container won't have, to look for in the data; shown maps those a
// library will be at to its name.
func oldPaths(p *pkgspec.Package, setup *importer.Setup, plan importer.Plan) ([]string, map[string]string) {
	shown := map[string]string{}
	for _, l := range plan.Libraries {
		if l.Library != "" {
			shown[l.OldTarget] = l.Library
		}
	}
	if setup == nil {
		return nil, shown
	}
	var out []string
	for _, m := range plan.Containers {
		if m.Part == "" {
			continue
		}
		oc := setup.Container(m.Old)
		for _, mt := range oc.Mounts {
			t := strings.TrimSuffix(mt.Target, "/")
			if strings.HasPrefix(t, "/etc/") || t == "" || slices.Contains(out, t+"/") {
				continue
			}
			kept := shown[t] != ""
			for _, c := range p.Containers {
				for _, pm := range c.Mounts {
					kept = kept || fsutil.Within(t, pm.Path)
				}
			}
			if !kept || shown[t] != "" {
				out = append(out, t+"/")
			}
		}
	}
	slices.Sort(out)
	return out, shown
}

// imageEnv is an image's own environment, KEY → value (empty if it isn't
// here).
func (n *Local) imageEnv(ctx context.Context, image string) map[string]string {
	out, err := n.podman(ctx, nil, "image", "inspect", "--format", "{{json .Config.Env}}", image)
	if err != nil {
		return nil
	}
	var env []string
	json.Unmarshal(bytes.TrimSpace(out), &env)
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// podman runs podman as the roostack user, stdin optional, returning its
// output (with what it said on stderr after an error).
func (n *Local) podman(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	if n.Launch == nil {
		return nil, fmt.Errorf("podman can't be run here")
	}
	cmd := n.Launch(ctx, nil, "podman", args...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// pullImage downloads an image if it isn't here yet.
func (n *Local) pullImage(ctx context.Context, act *activity.Log, image string) {
	if n.Reconciler == nil || n.Reconciler.Pull == nil {
		return
	}
	act.Printf("Making sure %s is here", image)
	if err := n.Reconciler.Pull(ctx, image, io.Discard); err != nil {
		act.Printf("Downloading %s: %v", image, err)
	}
}

// databaseImage is a database's image, the package's or Roostack's.
func databaseImage(d *pkgspec.Database) string {
	switch {
	case d.Image != "":
		return d.Image
	case d.Type == pkgspec.DBPostgres:
		return generate.DefaultPostgresImage
	case d.Type == pkgspec.DBMariaDB:
		return generate.DefaultMariaDBImage
	case d.Type == pkgspec.DBMySQL:
		return generate.DefaultMySQLImage
	}
	return ""
}

// databaseImageOf is the package's first Postgres database's image.
func databaseImageOf(p *pkgspec.Package) string {
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		if d := p.Databases[name]; d.Type == pkgspec.DBPostgres {
			return databaseImage(d)
		}
	}
	return ""
}

// freeSpace is how much a folder's disk has free (-1 if unknown).
func freeSpace(dir string) int64 {
	for d := dir; ; d = filepath.Dir(d) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(d, &st); err == nil {
			return int64(st.Bavail) * int64(st.Bsize)
		}
		if d == filepath.Dir(d) {
			return -1
		}
	}
}

// appVersionOf is a package's app version (3.3.3 from 3.3.3-roostack.1).
func appVersionOf(p *pkgspec.Package) string {
	v, _, _ := strings.Cut(p.Version, "-roostack")
	return v
}

// ownerText names a file's owner on the server, for people.
func ownerText(uid int) string {
	if uid == 0 {
		return "root"
	}
	return fmt.Sprintf("user %d", uid)
}

// pathFor names a folder of the person's copy, for people.
func pathFor(rel string) string {
	if rel == "." || rel == "" {
		return "the top of your files"
	}
	return rel
}

// cmpPath is a folder's name for the command that makes it readable: the
// folder itself, or for the top, the copy's own folder.
func cmpPath(rel string, src *ImportSource) string {
	if rel != "." && rel != "" {
		return strings.SplitN(rel, "/", 2)[0]
	}
	if src != nil && src.Library.Sub != "" {
		return path.Base(src.Library.Sub)
	}
	if src != nil && src.Library.Folder != "" {
		return path.Base(src.Library.Folder)
	}
	return "."
}

// shellQuote quotes a name for a shell command, if it needs it.
func shellQuote(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9._/-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
