package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/store"
)

// Finished logs become history, with their output; routine ones (nothing
// changed) don't, unless they failed; old ones can be deleted.
func TestKeepHistory(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n := &Local{Activity: activity.New(), Events: st}
	n.KeepHistory()
	ctx := context.Background()

	a := n.Activity.Start(activity.Apply, "Applying changes")
	a.Printf("Why: Installing forgejo.")
	a.Step("Started forgejo")
	a.Finish(nil)
	r := n.Activity.Start(activity.Apply, "Applying changes")
	r.Step("Nothing to change")
	r.Routine()
	r.Finish(nil)
	f := n.Activity.Start(activity.BackupSetupFor("n-1"), "Setting up backups")
	f.Routine()
	f.Finish(errors.New("boom"))

	var events []store.Event
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if events, _ = n.History(ctx, time.Time{}, "", 10); len(events) == 2 {
			break
		}
	}
	if len(events) != 2 {
		t.Fatalf("events: %+v", events)
	}
	kinds := map[string]store.Event{}
	for _, e := range events {
		kinds[e.Kind] = e
	}
	if e := kinds[activity.BackupSetup]; e.Error != "boom" {
		t.Errorf("a failed routine run is kept, as one kind: %+v", kinds)
	}
	e, err := n.HistoryEvent(ctx, kinds[activity.Apply].ID)
	if err != nil || e.Status != "Started forgejo" || e.Output != "Why: Installing forgejo.\n==> Started forgejo\n==> Done" {
		t.Errorf("apply: %+v %v", e, err)
	}
	if n, err := n.CullHistory(ctx, time.Now().Add(time.Minute)); err != nil || n != 2 {
		t.Errorf("culled %d, %v", n, err)
	}
}
