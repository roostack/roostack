package registry

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

// Every is how often the controller checks the registry (SPEC.md §8.3.3):
// often enough that a withdrawal reaches labs within hours, and the
// background check, not the Owner opening the catalog, is what keeps a lab
// up to date. Each lab adds up to Jitter, so labs don't all ask at once.
const (
	Every  = 4 * time.Hour
	Jitter = 30 * time.Minute
)

// Updater runs the Fetcher on the controller: when Roostack starts, every
// few hours, and when asked; not at all while the Owner has turned
// fetching off.
type Updater struct {
	Fetcher *Fetcher
	// Save records the Owner's choice in lab.yaml (registry.disabled).
	Save func(off bool) error
	Log  *slog.Logger

	mu  sync.Mutex
	off bool
}

// NewUpdater starts with the lab's setting.
func NewUpdater(f *Fetcher, off bool, save func(off bool) error, log *slog.Logger) *Updater {
	return &Updater{Fetcher: f, Save: save, Log: log, off: off}
}

// Run checks now, then every few hours until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	for {
		if u.On() {
			if err := u.Fetcher.Fetch(ctx); err != nil && ctx.Err() == nil {
				u.Log.Warn("checking the service registry", "url", u.Fetcher.URL, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(Every + rand.N(Jitter)):
		}
	}
}

// On says whether fetching is on.
func (u *Updater) On() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return !u.off
}

// SetOn turns fetching on or off, saving the choice. Turned on, it checks
// straight away, in the background.
func (u *Updater) SetOn(on bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.Save(!on); err != nil {
		return err
	}
	u.off = !on
	if on {
		go u.Fetcher.Fetch(context.Background())
	}
	return nil
}

// Check fetches now, if fetching is on.
func (u *Updater) Check(ctx context.Context) error {
	if !u.On() {
		return nil
	}
	return u.Fetcher.Fetch(ctx)
}

// CheckIfOlder fetches if fetching is on and the last try was more than
// age ago: the catalog page, when it's opened.
func (u *Updater) CheckIfOlder(ctx context.Context, age time.Duration) error {
	if !u.On() {
		return nil
	}
	return u.Fetcher.FetchIfOlder(ctx, age)
}

// Status is the record of the last fetches.
func (u *Updater) Status() Status { return u.Fetcher.Status() }

// URL is the registry's address.
func (u *Updater) URL() string { return u.Fetcher.URL }

// Dir is where the fetched files are kept.
func (u *Updater) Dir() string { return u.Fetcher.Dir }
