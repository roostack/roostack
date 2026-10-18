// Package reconcile makes the host match desired state (Principle 2).
//
// It compares generated output with the files on disk, then plans and
// applies the changes: directories, secrets, Quadlet files, Caddy config and
// systemd units. Planning has no side effects, so a plan can be shown before
// it is applied (`roostack apply --dry-run`).
//
// Roostack only touches files it owns: roostack-*.container / roostack-*.network in
// the Quadlet directory, and *.caddy in the Caddy sites directory. Service
// data is never deleted here.
package reconcile

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/systemd"
)

// Desired is the complete desired state of the host.
type Desired struct {
	System   *generate.SystemOutput
	Services []*generate.Output
}

// Systemd is the subset of systemd control the reconciler needs.
type Systemd interface {
	DaemonReload(ctx context.Context) error
	Start(ctx context.Context, units ...string) error
	StartNoBlock(ctx context.Context, units ...string) error
	Restart(ctx context.Context, units ...string) error
	Stop(ctx context.Context, units ...string) error
	ReloadOrRestart(ctx context.Context, units ...string) error
}

// Secrets is the subset of Podman secret handling the reconciler needs.
type Secrets interface {
	List(ctx context.Context) (map[string]bool, error)
	Create(ctx context.Context, name string, value []byte) error
}

// NoImageForArch is a Pull that failed because the image has no build for
// this server's kind of processor (ARM64-PLAN.md 1.3). Its message is for
// the Owner, in place of Podman's "no image found in image index".
type NoImageForArch struct {
	Image string
	Arch  string // Go's name for the processor, e.g. arm64
}

func (e *NoImageForArch) Error() string {
	return fmt.Sprintf("This service has no version for this server's kind of processor (%s), so it can't run here. (Image: %s)", e.Arch, e.Image)
}

// Reconciler applies desired state.
type Reconciler struct {
	Paths   config.Paths
	Systemd Systemd
	Secrets Secrets
	// Log shows an apply's steps live in the UI (optional, per apply).
	Log *activity.Log
	// UserSecret returns a user-provided secret value (e.g. the DNS token)
	// for secrets that are not generated. It may be nil.
	UserSecret func(name string) ([]byte, bool)
	// Pull downloads an image before a new container's first start, writing
	// its output to w, so a download is its own step with its own log
	// (optional: without it, starting the unit pulls the image). An image
	// with no build for this server's processor is a *NoImageForArch.
	Pull func(ctx context.Context, image string, w io.Writer) error
	// Progress hears each step of starting new containers, for showing a
	// service's install step by step (optional).
	Progress func(Event)
	// Settle is how long a unit that failed to start gets to come up after
	// all, through systemd restarting it (0 means 30 s).
	Settle time.Duration
	// Unfinished are units of new containers whose first start didn't
	// finish (a failed download, or Roostack restarted halfway): they're
	// started as new ones again, downloading first, though their Quadlet
	// files are written already (optional).
	Unfinished map[string]bool
}

// UnitStatuser reads units' states; a Systemd that implements it lets a
// failed start be retried by systemd (Reconciler.start).
type UnitStatuser interface {
	Status(ctx context.Context, units ...string) ([]systemd.UnitStatus, error)
}

// start starts units. If that fails, systemd restarts failed units
// (Restart=always), and one that failed only because it started too early,
// such as pasta binding a port at boot before the network was fully up,
// is running a few seconds later (docs/ASSUMPTIONS.md C11). So a failure
// counts only if the units aren't all active within Settle.
func (r *Reconciler) start(ctx context.Context, log *activity.Log, units ...string) error {
	err := r.Systemd.Start(ctx, units...)
	st, ok := r.Systemd.(UnitStatuser)
	if err == nil || len(units) == 0 || !ok {
		return err
	}
	log.Printf("Starting failed; waiting for systemd to try again: %v", err)
	deadline := time.Now().Add(cmp.Or(r.Settle, 30*time.Second))
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return err
		case <-time.After(2 * time.Second):
		}
		states, serr := st.Status(ctx, units...)
		if serr == nil && len(states) == len(units) && !slices.ContainsFunc(states, func(s systemd.UnitStatus) bool {
			return s.ActiveState != "active"
		}) {
			log.Printf("They're running now.")
			return nil
		}
	}
	return err
}

// Event is a step of an apply that people wait for: downloading or starting
// a new container, or waiting for Caddy to serve new web addresses. Done
// is false when the step begins and true when it ends; Err is set if it
// failed.
type Event struct {
	Stage string // StageDownload, StageStart or StageProxy
	Unit  string // the container's unit (none for StageProxy)
	Done  bool
	Err   error
}

// Event stages.
const (
	StageDownload = "download"
	StageStart    = "start"
	StageProxy    = "proxy"
)

func (r *Reconciler) progress(stage, unit string, done bool, err error) {
	if r.Progress != nil {
		r.Progress(Event{Stage: stage, Unit: unit, Done: done, Err: err})
	}
}

// FileChange is a file to write or remove.
type FileChange struct {
	Path    string
	Content string // empty for removals
}

// Plan is the set of changes needed. The zero Plan means nothing to do.
type Plan struct {
	Dirs    []string
	Secrets []generate.Secret
	Write   []FileChange
	Remove  []FileChange
	// StopUnits are units whose Quadlet file is being removed.
	StopUnits []string
	// RestartUnits have a changed Quadlet file.
	RestartUnits []string
	// StartUnits are all desired units; starting a running unit is a no-op.
	StartUnits   []string
	DaemonReload bool
	ReloadCaddy  bool
	// Waiting are Quadlet files left as they are because a secret they
	// need hasn't been given yet (generate.Secret.Holds).
	Waiting []string
	// BuildUnits build images (Quadlet .build files) whose file is new or
	// changed. They start in the background as soon as they're written, so
	// a slow first build (Caddy's compiles it: minutes) runs while nothing
	// needs it yet, e.g. during the setup wizard, whose domain step the
	// Caddy container waits for.
	BuildUnits []string
	// Pulls are the registry images of new containers, by unit, downloaded
	// before they start (Reconciler.Pull).
	Pulls map[string]string
	// StartNotes say what a new container's first start involves (a
	// download, or building an image), for the log: starting them can take
	// minutes, and people should know why.
	StartNotes map[string]string
}

// quadletImage is a Quadlet file's Image= value.
func quadletImage(quadlet string) string {
	for _, line := range strings.Split(quadlet, "\n") {
		if image, ok := strings.CutPrefix(line, "Image="); ok {
			return image
		}
	}
	return ""
}

// startNote is what starting a new container's unit involves.
func startNote(quadlet string) string {
	for _, line := range strings.Split(quadlet, "\n") {
		image, ok := strings.CutPrefix(line, "Image=")
		if !ok {
			continue
		}
		if strings.HasSuffix(image, ".build") {
			return "builds its image the first time (compiling: several minutes)"
		}
		name, _, _ := strings.Cut(image, "@") // without the digest
		return "downloads " + name + " if it isn't here yet"
	}
	return ""
}

// Empty reports whether the plan changes nothing (StartUnits is always
// populated, and starting running units is harmless). Starting a new
// container again (Reconciler.Unfinished) is a change.
func (p *Plan) Empty() bool {
	return len(p.Dirs) == 0 && len(p.Secrets) == 0 && len(p.Write) == 0 &&
		len(p.Remove) == 0 && !p.ReloadCaddy && len(p.StartNotes) == 0
}

// String describes the plan for people.
func (p *Plan) String() string {
	var b strings.Builder
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", title)
		for _, it := range items {
			fmt.Fprintf(&b, "  %s\n", it)
		}
	}
	list("Create directories", p.Dirs)
	var secrets []string
	for _, s := range p.Secrets {
		how := "user-provided"
		if s.Generate {
			how = "generated"
		}
		secrets = append(secrets, fmt.Sprintf("%s (%s)", s.Name, how))
	}
	list("Create secrets", secrets)
	list("Stop units", p.StopUnits)
	list("Write files", paths(p.Write))
	list("Remove files", paths(p.Remove))
	if p.DaemonReload {
		b.WriteString("Reload systemd\n")
	}
	list("Restart units", p.RestartUnits)
	list("Start units", p.StartUnits)
	list("Waiting for a secret", p.Waiting)
	if p.ReloadCaddy {
		b.WriteString("Reload Caddy\n")
	}
	if b.Len() == 0 {
		return "Nothing to do.\n"
	}
	return b.String()
}

func paths(fs []FileChange) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Path
	}
	return out
}

// Plan works out what must change. It reads the filesystem and the list of
// existing secrets, but changes nothing.
func (r *Reconciler) Plan(ctx context.Context, d Desired) (*Plan, error) {
	if d.System == nil {
		return nil, errors.New("reconcile: desired state has no system output")
	}
	plan := &Plan{}
	quadletDir := r.Paths.QuadletDir()
	sitesDir := r.Paths.CaddySitesDir()

	// Collect desired files, units, dirs and secrets.
	wantQuadlets := map[string]string{}
	wantSites := map[string]string{}
	var units, dirs []string
	var secrets []generate.Secret
	for _, f := range d.System.Quadlets {
		wantQuadlets[f.Name] = f.Content
	}
	caddyfile := filepath.Join(r.Paths.CaddyDir(), d.System.Caddyfile.Name)
	units = append(units, d.System.Units...)
	dirs = append(dirs, d.System.Dirs...)
	secrets = append(secrets, d.System.Secrets...)
	for _, s := range d.Services {
		for _, f := range s.Quadlets {
			if _, dup := wantQuadlets[f.Name]; dup {
				return nil, fmt.Errorf("reconcile: %s is generated twice", f.Name)
			}
			wantQuadlets[f.Name] = f.Content
		}
		if s.CaddySite != nil {
			wantSites[s.CaddySite.Name] = s.CaddySite.Content
		}
		units = append(units, s.Units...)
		dirs = append(dirs, s.Dirs...)
		secrets = append(secrets, s.Secrets...)
	}

	// Directories.
	for _, dir := range dirs {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			plan.Dirs = append(plan.Dirs, dir)
		} else if err != nil {
			return nil, err
		}
	}

	// Secrets.
	existing, err := r.Secrets.List(ctx)
	if err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, s := range secrets {
		if s.InstallOnly && !r.hasUserSecret(s.Name) {
			continue // already used and deleted, or never given
		}
		if s.Holds != "" && !existing[s.Name] && !s.Generate && !r.hasUserSecret(s.Name) {
			held[s.Holds] = true
			continue
		}
		if !existing[s.Name] {
			plan.Secrets = append(plan.Secrets, s)
			existing[s.Name] = true // shared secrets are listed once
		}
	}

	// Quadlet files. One waiting for a secret is neither written nor
	// removed, and its unit isn't started.
	haveQuadlets, err := readOwned(quadletDir, isOwnedQuadlet)
	if err != nil {
		return nil, err
	}
	for name := range held {
		plan.Waiting = append(plan.Waiting, name)
		delete(wantQuadlets, name)
		delete(haveQuadlets, name)
		units = slices.DeleteFunc(units, func(u string) bool { return u == unitFor(name) })
	}
	slices.Sort(plan.Waiting)
	// startNew starts a container as a new one: saying what its first
	// start involves, and downloading its image first.
	startNew := func(name, content string) {
		note := startNote(content)
		if note == "" {
			return
		}
		if plan.StartNotes == nil {
			plan.StartNotes = map[string]string{}
		}
		plan.StartNotes[unitFor(name)] = note
		if image := quadletImage(content); image != "" && !strings.HasSuffix(image, ".build") {
			if plan.Pulls == nil {
				plan.Pulls = map[string]string{}
			}
			plan.Pulls[unitFor(name)] = image
		}
	}
	for _, name := range slices.Sorted(maps.Keys(wantQuadlets)) {
		content := wantQuadlets[name]
		old, exists := haveQuadlets[name]
		if exists && old == content {
			if r.Unfinished[unitFor(name)] {
				startNew(name, content)
			}
			continue
		}
		plan.Write = append(plan.Write, FileChange{filepath.Join(quadletDir, name), content})
		plan.DaemonReload = true
		if strings.HasSuffix(name, ".build") {
			plan.BuildUnits = append(plan.BuildUnits, unitFor(name))
		}
		if exists {
			plan.RestartUnits = append(plan.RestartUnits, unitFor(name))
		} else {
			startNew(name, content)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(haveQuadlets)) {
		if _, ok := wantQuadlets[name]; !ok {
			plan.Remove = append(plan.Remove, FileChange{Path: filepath.Join(quadletDir, name)})
			plan.StopUnits = append(plan.StopUnits, unitFor(name))
			plan.DaemonReload = true
		}
	}

	// Caddy config. A node has none: the controller's Caddy serves its
	// routes (SPEC.md §10.5).
	if d.System.Caddyfile.Name != "" {
		haveSites, err := readOwned(sitesDir, func(n string) bool { return strings.HasSuffix(n, ".caddy") })
		if err != nil {
			return nil, err
		}
		for _, name := range slices.Sorted(maps.Keys(wantSites)) {
			if haveSites[name] != wantSites[name] {
				plan.Write = append(plan.Write, FileChange{filepath.Join(sitesDir, name), wantSites[name]})
				plan.ReloadCaddy = true
			}
		}
		for _, name := range slices.Sorted(maps.Keys(haveSites)) {
			if _, ok := wantSites[name]; !ok {
				plan.Remove = append(plan.Remove, FileChange{Path: filepath.Join(sitesDir, name)})
				plan.ReloadCaddy = true
			}
		}
		if old, err := os.ReadFile(caddyfile); err != nil || string(old) != d.System.Caddyfile.Content {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			plan.Write = append(plan.Write, FileChange{caddyfile, d.System.Caddyfile.Content})
			plan.ReloadCaddy = true
		}
	}

	// Other system files (e.g. CrowdSec's acquisition file), written when
	// they change, restarting the container that reads them if it was
	// already there and isn't restarting anyway. Removing the unit that
	// uses one leaves it, like data.
	for _, f := range d.System.Files {
		if old, err := os.ReadFile(f.Name); err != nil || string(old) != f.Content {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			plan.Write = append(plan.Write, FileChange{f.Name, f.Content})
			if _, ok := haveQuadlets[f.Restarts]; ok && !slices.Contains(plan.RestartUnits, unitFor(f.Restarts)) {
				plan.RestartUnits = append(plan.RestartUnits, unitFor(f.Restarts))
			}
		}
	}

	if held[generate.CaddyUnit+".container"] {
		plan.ReloadCaddy = false // its files are written for when it starts
	}
	plan.StartUnits = units
	return plan, nil
}

// Apply carries out a plan. It stops at the first error; running Plan and
// Apply again continues from where it left off.
func (r *Reconciler) Apply(ctx context.Context, p *Plan) error {
	log := r.Log
	if len(p.Dirs) > 0 {
		log.Step("Creating folders")
	}
	for _, dir := range p.Dirs {
		log.Printf("%s", dir)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	if len(p.Secrets) > 0 {
		log.Step("Creating secrets")
	}
	for _, s := range p.Secrets {
		log.Printf("%s", s.Name) // never the value
		value, err := r.secretValue(s)
		if err != nil {
			return err
		}
		if err := r.Secrets.Create(ctx, s.Name, value); err != nil {
			return err
		}
	}
	if len(p.StopUnits) > 0 {
		log.Step("Stopping removed services")
		log.Printf("%s", strings.Join(p.StopUnits, " "))
	}
	if err := r.Systemd.Stop(ctx, p.StopUnits...); err != nil {
		return err
	}
	if len(p.Write)+len(p.Remove) > 0 {
		log.Step("Writing configuration")
	}
	for _, f := range p.Write {
		log.Printf("write  %s", f.Path)
		if err := writeAtomic(f.Path, f.Content); err != nil {
			return err
		}
	}
	for _, f := range p.Remove {
		log.Printf("remove %s", f.Path)
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if p.DaemonReload {
		log.Step("Reloading systemd")
		if err := r.Systemd.DaemonReload(ctx); err != nil {
			return err
		}
	}
	if len(p.RestartUnits) > 0 {
		log.Step("Restarting changed services")
		log.Printf("%s", strings.Join(p.RestartUnits, " "))
	}
	if err := r.Systemd.Restart(ctx, p.RestartUnits...); err != nil {
		return err
	}
	if len(p.BuildUnits) > 0 {
		log.Step("Building images in the background")
		log.Printf("%s", strings.Join(p.BuildUnits, " "))
		if err := r.Systemd.StartNoBlock(ctx, p.BuildUnits...); err != nil {
			return err
		}
	}
	// Running services (a no-op for those already running) first, then new
	// ones one at a time: download, then start, saying what each involves.
	// A new Caddy comes last: its first start waits for its image build
	// (minutes), which new services don't need.
	caddy := generate.CaddyUnit + ".service"
	var running, fresh []string
	newCaddy := false
	for _, u := range p.StartUnits {
		switch _, ok := p.StartNotes[u]; {
		case ok && u == caddy:
			newCaddy = true
		case ok:
			fresh = append(fresh, u)
		default:
			running = append(running, u)
		}
	}
	log.Step("Starting services")
	log.Printf("%s", strings.Join(running, " "))
	if err := r.start(ctx, log, running...); err != nil {
		return err
	}
	for _, u := range fresh {
		name := strings.TrimSuffix(u, ".service")
		if image := p.Pulls[u]; image != "" && r.Pull != nil {
			log.Step("Downloading %s's image", name)
			log.Printf("%s", image)
			r.progress(StageDownload, u, false, nil)
			err := r.Pull(ctx, image, log)
			r.progress(StageDownload, u, true, err)
			if err != nil {
				// Already a sentence for the Owner.
				var noArch *NoImageForArch
				if errors.As(err, &noArch) {
					return err
				}
				return fmt.Errorf("downloading %s: %w", image, err)
			}
			log.Step("Starting %s", name)
		} else {
			log.Step("Starting %s: %s", name, p.StartNotes[u])
		}
		r.progress(StageStart, u, false, nil)
		err := r.start(ctx, log, u)
		r.progress(StageStart, u, true, err)
		if err != nil {
			return err
		}
	}
	// reload-or-restart also starts Caddy if it isn't running
	// (docs/ASSUMPTIONS.md L2).
	if newCaddy || p.ReloadCaddy {
		if newCaddy {
			log.Step("Starting Caddy: %s", p.StartNotes[caddy])
		} else {
			log.Step("Reloading Caddy (web addresses and certificates)")
		}
		r.progress(StageProxy, "", false, nil)
		err := r.Systemd.ReloadOrRestart(ctx, caddy)
		r.progress(StageProxy, "", true, err)
		return err
	}
	return nil
}

func (r *Reconciler) hasUserSecret(name string) bool {
	if r.UserSecret == nil {
		return false
	}
	_, ok := r.UserSecret(name)
	return ok
}

func (r *Reconciler) secretValue(s generate.Secret) ([]byte, error) {
	if s.Generate {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		return []byte(base64.RawURLEncoding.EncodeToString(buf)), nil
	}
	if r.UserSecret != nil {
		if v, ok := r.UserSecret(s.Name); ok {
			return v, nil
		}
	}
	return nil, fmt.Errorf("reconcile: secret %s must be provided by the user", s.Name)
}

// isOwnedQuadlet reports whether Roostack manages a file in the Quadlet dir.
func isOwnedQuadlet(name string) bool {
	return strings.HasPrefix(name, "roostack-") &&
		(strings.HasSuffix(name, ".container") || strings.HasSuffix(name, ".network") || strings.HasSuffix(name, ".build"))
}

// unitFor maps a Quadlet file name to its systemd service (ASSUMPTIONS A4).
func unitFor(file string) string {
	if base, ok := strings.CutSuffix(file, ".network"); ok {
		return base + "-network.service"
	}
	if base, ok := strings.CutSuffix(file, ".build"); ok {
		return base + "-build.service"
	}
	return strings.TrimSuffix(file, ".container") + ".service"
}

// readOwned reads the files in dir accepted by owned. A missing dir is empty.
func readOwned(dir string, owned func(string) bool) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !owned(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = string(data)
	}
	return files, nil
}

// writeAtomic writes a file via a temporary file and rename, so readers
// never see a partial file.
func writeAtomic(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".roostack-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
