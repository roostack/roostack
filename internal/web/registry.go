package web

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/registry"
)

// RegistryUpdater keeps the controller's catalog up to date with the
// service registry (registry.Updater; SPEC.md §8.3.3).
type RegistryUpdater interface {
	On() bool
	SetOn(on bool) error
	Check(ctx context.Context) error
	CheckIfOlder(ctx context.Context, age time.Duration) error
	Status() registry.Status
	Listing() (map[string]registry.Listed, error)
	URL() string
	Dir() string
}

// The catalog page checks the registry when it's opened, unless it did
// lately, and waits at most catalogWait for it: past that, it shows what
// the lab has.
const (
	catalogCheckAge = 15 * time.Minute
	catalogWait     = 10 * time.Second
)

// registryView is the registry's part of Settings → Updates and the
// catalog page.
type registryView struct {
	On     bool
	URL    string
	Dir    string // where the files are kept
	Status registry.Status
	// Stale: the newest list the lab has is over registry.StaleAfter old
	// (or there's none), after at least one try.
	Stale bool
	// CommitURL shows the registry commit the list was built from, when
	// it's the project's registry.
	CommitURL string
	Skipped   []skippedPackage
	// StopsOn: Roostack may stop a service the registry says is
	// dangerous (SPEC.md §8.3.4).
	StopsOn bool
}

type skippedPackage struct{ Name, Why string }

func (s *Server) registryView() *registryView {
	if s.Registry == nil {
		return nil
	}
	st := s.Registry.Status()
	v := &registryView{On: s.Registry.On(), URL: s.Registry.URL(), Dir: s.Registry.Dir(), Status: st,
		Stale: !st.Checked.IsZero() && st.Stale(time.Now())}
	if st.Commit != "" && v.URL == registry.DefaultURL {
		v.CommitURL = registry.SourceRepo + "/tree/" + st.Commit
	}
	v.StopsOn = s.Manager.StopsOn()
	for _, name := range slices.Sorted(maps.Keys(st.Skipped)) {
		v.Skipped = append(v.Skipped, skippedPackage{name, st.Skipped[name]})
	}
	return v
}

// registryAttention warns the Owner when the lab hasn't had a list from
// the registry for a while (the Owner, 2026-10-14: a warning, not a
// refusal): withdrawals since then are unknown to it.
func (s *Server) registryAttention(add func(level, text, link, linkText string)) {
	v := s.registryView()
	if v == nil || !v.On || !v.Stale {
		return
	}
	text := "Roostack can't get the list of services from the service registry, so you can't install services."
	if v.Status.Serial != 0 {
		text = "Roostack's list of services is from " + whenText(v.Status.Date) +
			": it hasn't been able to get a newer one, so it doesn't know of fixes or withdrawn versions since."
	}
	add(levelWarning, text, "/settings/updates#registry", "See why")
}

// registryOn says whether services can be installed from the registry:
// not while the Owner has turned fetching off.
func (s *Server) registryOn() bool { return s.Registry == nil || s.Registry.On() }

func (s *Server) registryCheckSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil {
		http.NotFound(w, r)
		return
	}
	// Recorded in the status shown on the page, whether it worked or not.
	if err := s.Registry.Check(r.Context()); err != nil {
		s.Log.Warn("checking the service registry", "err", err)
	}
	redirect(w, r, "/settings/updates#registry")
}

func (s *Server) registryOnSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil {
		http.NotFound(w, r)
		return
	}
	on := r.FormValue("on") == "1"
	if err := s.Registry.SetOn(on); err != nil {
		s.serverError(w, err)
		return
	}
	what := "turned off checking the service registry"
	if on {
		what = "turned on checking the service registry"
	}
	s.Store.Audit(r.Context(), currentUser(r), "updates", what)
	redirect(w, r, "/settings/updates#registry")
}
