package node

import (
	"context"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/osupdate"
)

// A server's maintenance jobs (SPEC.md §3.2, §6.9): each node backs up
// its own services and keeps its own OS up to date. The jobs share the
// server's maintenance coordinator, so they never overlap.

// Backups is a server's nightly backup.
type Backups interface {
	Status(ctx context.Context) (backup.Status, error)
	RunNow(ctx context.Context) error
}

// Restores restores a server's services from its backups.
type Restores interface {
	Snapshots(ctx context.Context, service string) ([]backup.Snapshot, error)
	Restore(ctx context.Context, service, snapshotID string) error
	// Pending says whether a restore is queued or running; the error
	// says the server couldn't be asked.
	Pending(ctx context.Context) (bool, error)
	// Outcome is how the service's last restore or put back here ended,
	// for the controller to hold it stopped if its databases didn't load
	// (SPEC.md §6.9, "A restore that didn't finish").
	Outcome(ctx context.Context, service string) (backup.RestoreOutcome, error)
	// PutBack puts a copy a restore here kept back in place of the data
	// (SPEC.md §8.4), in the background (Mover.PutBackKept lists them).
	PutBack(ctx context.Context, service, path string) error
}

// Updates is a server's OS updates and restarts.
type Updates interface {
	Last(ctx context.Context) (*osupdate.Result, error)
	// Pending says whether an update run is queued or running; the error
	// says the server couldn't be asked.
	Pending(ctx context.Context) (bool, error)
	Submit(ctx context.Context) error
	RestartStatus(ctx context.Context) (osupdate.RestartStatus, error)
	RestartNow(ctx context.Context) error
	// HostChanges are changes to the host Roostack didn't make (SPEC.md §3.2).
	HostChanges(ctx context.Context) (osupdate.HostCheck, error)
	AcknowledgeHostChanges(ctx context.Context) error
	// Series is Roostack's own release series on this server and any
	// newer one; AcceptSeries is the Owner's go-ahead to move to it
	// (SPEC.md §7).
	Series(ctx context.Context) (osupdate.Series, error)
	AcceptSeries(ctx context.Context, series string) error
}

// SeriesGoneMsg is said when the release series the Owner accepted is no
// longer the one on offer (osupdate.ErrNoSuchSeries).
const SeriesGoneMsg = "That Roostack release isn't on offer any more. Reload the page to see what is."

// Maintenance is this server's maintenance jobs. A nil job isn't
// available (e.g. backups aren't set up yet).
type Maintenance struct {
	Backups  Backups
	Restores Restores
	Updates  Updates
	// Coord is the server's maintenance coordinator, which updates to
	// services hold while they switch (update.go).
	Coord *maintenance.Coordinator
}

// Backups is this server's backup job, or nil.
func (n *Local) Backups() Backups { return n.Jobs.Backups }

// Restores is this server's restores, or nil.
func (n *Local) Restores() Restores { return n.Jobs.Restores }

// Updates is this server's OS updates, or nil.
func (n *Local) Updates() Updates { return n.Jobs.Updates }
