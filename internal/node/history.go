package node

import (
	"context"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/store"
)

// History (SPEC.md §6.14): each server keeps the logs of the work it did
// (applies, backups, updates, restores, ...) in its own database, and the
// controller reads them for Settings → History, and deletes old ones.

// HistoryStore keeps events (internal/store).
type HistoryStore interface {
	AddEvent(ctx context.Context, e store.Event) error
	Events(ctx context.Context, before time.Time, kind string, limit int) ([]store.Event, error)
	EventByID(ctx context.Context, id int64) (store.Event, error)
	CullHistory(ctx context.Context, before time.Time) (int64, error)
}

// History is a server's history (Local and Remote).
type History interface {
	History(ctx context.Context, before time.Time, kind string, limit int) ([]store.Event, error)
	HistoryEvent(ctx context.Context, id int64) (store.Event, error)
	// CullHistory deletes events finished before before.
	CullHistory(ctx context.Context, before time.Time) (int64, error)
}

// maxEventOutput caps an event's log (the activity log keeps its last 2000
// lines; this bounds very long lines too).
const maxEventOutput = 256 << 10

// KeepHistory makes every finished log (except routine ones) an event.
func (n *Local) KeepHistory() {
	if n.Activity == nil || n.Events == nil {
		return
	}
	n.Activity.Finished = func(kind string, s activity.Snapshot) {
		out := strings.Join(s.Lines, "\n")
		if s.Dropped > 0 {
			out = "… " + strconv.Itoa(s.Dropped) + " earlier lines not kept\n" + out
		}
		if len(out) > maxEventOutput {
			out = "…" + out[len(out)-maxEventOutput:]
		}
		// Kinds like backup-setup.<server> are one kind for history.
		kind, _, _ = strings.Cut(kind, ".")
		e := store.Event{Started: s.Started, Finished: s.Finished, Kind: kind, Title: s.Title, Status: s.Status, Error: s.Err, Output: out}
		if err := n.Events.AddEvent(context.Background(), e); err != nil && n.Log != nil {
			n.Log.Error("keeping a log as history", "kind", kind, "err", err)
		}
	}
}

func (n *Local) History(ctx context.Context, before time.Time, kind string, limit int) ([]store.Event, error) {
	if n.Events == nil {
		return nil, nil
	}
	return n.Events.Events(ctx, before, kind, limit)
}

func (n *Local) HistoryEvent(ctx context.Context, id int64) (store.Event, error) {
	if n.Events == nil {
		return store.Event{}, ErrNotFound
	}
	e, err := n.Events.EventByID(ctx, id)
	if err == store.ErrNotFound {
		return e, ErrNotFound
	}
	return e, err
}

func (n *Local) CullHistory(ctx context.Context, before time.Time) (int64, error) {
	if n.Events == nil {
		return 0, nil
	}
	return n.Events.CullHistory(ctx, before)
}
