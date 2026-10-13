package node

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
)

// A new service's install, step by step (SPEC.md §6.3.3): downloading and
// starting each container, Caddy serving its web address, and its setup
// steps. Shown instead of "stopped" until the last step is done, so people
// can see it's still going (a first install can take minutes).

// InstallProgress is a service's install so far.
type InstallProgress struct {
	Steps []InstallStep `json:"steps"`
	// Failed says why a step failed; the install stops there.
	Failed string `json:"failed,omitempty"`
	// Log is the activity kind whose log shows the step in progress, and
	// LogUpdated when it last had output: a long step whose log still moves
	// hasn't hung.
	Log        string    `json:"log,omitempty"`
	LogUpdated time.Time `json:"log_updated,omitempty"`
}

// InstallStep is one step. Started is zero until it begins, Finished until
// it's done.
type InstallStep struct {
	Key      string    `json:"key"` // what the step waits for (installKey)
	Title    string    `json:"title"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
}

// Current is the index of the step in progress or next (len(Steps) once
// every step is done).
func (p *InstallProgress) Current() int {
	for i, s := range p.Steps {
		if s.Finished.IsZero() {
			return i
		}
	}
	return len(p.Steps)
}

// State is a step's state for display: done, now or todo.
func (p *InstallProgress) State(i int) string {
	switch c := p.Current(); {
	case i < c:
		return "done"
	case i == c:
		return "now"
	}
	return "todo"
}

// Step is the step in progress (or next).
func (p *InstallProgress) Step() InstallStep {
	if c := p.Current(); c < len(p.Steps) {
		return p.Steps[c]
	}
	return InstallStep{}
}

// Keys of steps: a stage and, for containers, their unit.
func installKey(stage, unit string) string { return strings.TrimSpace(stage + " " + unit) }

const stageSetup = "setup"

// Waiting, before the apply that installs it has begun (another change is
// still being applied, e.g. Caddy's first start).
const stageQueued = "queued"

// planInstalls starts tracking every service this apply installs: one
// whose containers are all new. buildRunning says Caddy's image is still
// building, so the web address step will wait for it.
func (n *Local) planInstalls(services []state.Service, desired reconcile.Desired, plan *reconcile.Plan, buildRunning bool) {
	byName := map[string]state.Service{}
	for _, s := range services {
		byName[s.Name()] = s
	}
	// Setup steps run only for a new install, not for a service that
	// arrives already set up (moved or restored from another server):
	// otherwise "finishing setup" would never finish.
	setUp := map[string]bool{}
	if n.State != nil {
		for _, s := range services {
			if v, err := n.State.Setting(context.Background(), hookKey(s.Name())); err == nil && v == hookPending {
				setUp[s.Name()] = true
			}
		}
	}
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	// A failed install is shown until the next apply, which tries again
	// with its units already written: its units' state says how that went.
	for name, p := range n.installs {
		if p.Failed != "" {
			delete(n.installs, name)
		}
	}
	for _, o := range desired.Services {
		s, ok := byName[o.Service]
		if !ok || len(o.Units) == 0 {
			continue
		}
		fresh := true
		for _, u := range o.Units {
			if _, ok := plan.StartNotes[u]; !ok {
				fresh = false
			}
		}
		if !fresh {
			continue
		}
		p := &InstallProgress{Log: activity.Apply, LogUpdated: now}
		images := containerImages(o)
		// With one container of its own (besides databases), it's just
		// the service's name.
		own := 0
		for _, u := range o.Units {
			if !strings.HasPrefix(u, "roostack-"+o.Service+"-db-") {
				own++
			}
		}
		for _, u := range plan.StartUnits {
			if !slices.Contains(o.Units, u) {
				continue
			}
			label := containerLabel(s.Title(), o.Service, u, images[u], own == 1)
			if plan.Pulls[u] != "" && n.Reconciler != nil && n.Reconciler.Pull != nil {
				p.Steps = append(p.Steps, InstallStep{Key: installKey(reconcile.StageDownload, u), Title: "Downloading " + label})
			}
			p.Steps = append(p.Steps, InstallStep{Key: installKey(reconcile.StageStart, u), Title: "Starting " + label})
		}
		// Caddy serves its web address, unless Caddy itself is waiting for
		// the domain step (then nothing is reachable yet).
		if len(o.Hosts) > 0 && !slices.Contains(plan.Waiting, generate.CaddyUnit+".container") {
			title := "Making it reachable at its web address"
			if buildRunning {
				title = "Waiting for Roostack's web proxy to finish building (first time only; a few minutes)"
			}
			p.Steps = append(p.Steps, InstallStep{Key: reconcile.StageProxy, Title: title})
		}
		if hooks := s.Package.HooksOn(pkgspec.HookInstall); len(hooks) > 0 && setUp[o.Service] {
			title := "Finishing setup"
			if t := hooks[0].Title; t != "" {
				title += " (" + lowerFirst(t) + ")"
			}
			p.Steps = append(p.Steps, InstallStep{Key: stageSetup, Title: title})
		}
		if n.installs == nil {
			n.installs = map[string]*InstallProgress{}
		}
		n.installs[o.Service] = p
	}
}

// installEvent records a step of the apply (reconcile.Progress) in every
// install waiting for it.
func (n *Local) installEvent(ev reconcile.Event) {
	key := installKey(ev.Stage, ev.Unit)
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.installs {
		for i := range p.Steps {
			if p.Steps[i].Key != key {
				continue
			}
			switch {
			case ev.Err != nil:
				p.Failed = ev.Err.Error()
			case ev.Done:
				p.Steps[i].Finished = now
			default:
				p.Steps[i].Started = now
			}
		}
	}
}

// failInstalls stops the installs of an apply that failed before their
// steps ran.
func (n *Local) failInstalls(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.installs {
		if p.Failed == "" && p.Current() < len(p.Steps) {
			p.Failed = err.Error()
		}
	}
}

// installStep marks a service's step started or done, and forgets the
// install once every step is done.
func (n *Local) installStep(service, key string, done bool) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.installs[service]
	if p == nil {
		return
	}
	for i := range p.Steps {
		if p.Steps[i].Key == key {
			if done {
				p.Steps[i].Finished = now
			} else {
				p.Steps[i].Started = now
				p.Log = activity.Setup
			}
		}
	}
	n.forgetFinishedLocked()
}

// dropInstall stops showing a service's install (its setup failed, and
// the service page says so).
func (n *Local) dropInstall(service string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.installs, service)
}

// installsApplied is called once an apply has finished: every step it
// covers is done (an apply that had nothing to do for Caddy sends no
// proxy event), and installs without setup steps are complete.
func (n *Local) installsApplied() {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range n.installs {
		for i := range p.Steps {
			if p.Steps[i].Key != stageSetup && p.Steps[i].Finished.IsZero() {
				if p.Steps[i].Started.IsZero() {
					p.Steps[i].Started = now
				}
				p.Steps[i].Finished = now
			}
		}
	}
	n.forgetFinishedLocked()
}

func (n *Local) forgetFinishedLocked() {
	for name, p := range n.installs {
		if p.Failed == "" && p.Current() == len(p.Steps) {
			delete(n.installs, name)
		}
	}
}

// installProgress is a copy of a service's install progress, with its
// log's latest output time; nil if it isn't being installed.
func (n *Local) installProgress(service string) *InstallProgress {
	n.mu.Lock()
	p := n.installs[service]
	var out *InstallProgress
	if p != nil {
		cp := *p
		cp.Steps = slices.Clone(p.Steps)
		out = &cp
	}
	n.mu.Unlock()
	if out != nil && n.Activity != nil {
		if snap, ok := n.Activity.Latest(out.Log); ok && snap.Updated.After(out.LogUpdated) {
			out.LogUpdated = snap.Updated
		}
	}
	return out
}

// queuedInstall is shown for a service whose install hasn't begun: it's
// assigned, but no apply has written its units yet.
func queuedInstall(buildRunning bool) *InstallProgress {
	title := "Waiting for other changes to finish"
	if buildRunning {
		title = "Waiting for Roostack's web proxy to finish building (first time only; a few minutes)"
	}
	return &InstallProgress{Steps: []InstallStep{{Key: stageQueued, Title: title}}, Log: activity.Apply}
}

// containerImages maps a service's container units to their images.
func containerImages(o *generate.Output) map[string]string {
	out := map[string]string{}
	for _, q := range o.Quadlets {
		if unit, ok := strings.CutSuffix(q.Name, ".container"); ok {
			out[unit+".service"] = quadletValue(q.Content, "Image")
		}
	}
	return out
}

// containerLabel names a container for people: "the database (Postgres)",
// "the cache (Valkey)", or the service itself.
func containerLabel(title, service, unit, image string, only bool) string {
	part := strings.TrimSuffix(strings.TrimPrefix(unit, "roostack-"+service+"-"), ".service")
	engine := imageName(image)
	if strings.HasPrefix(part, "db-") {
		if engine == "valkey" || engine == "redis" {
			return "the cache (" + upperFirst(engine) + ")"
		}
		return "the database (" + upperFirst(engine) + ")"
	}
	if only {
		return title
	}
	return title + " (" + part + ")"
}

// imageName is an image's name without registry, tag or digest:
// docker.io/library/postgres:17-alpine@sha256:… → postgres.
func imageName(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndexByte(image, '/'); i >= 0 {
		image = image[i+1:]
	}
	image, _, _ = strings.Cut(image, ":")
	return image
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}
