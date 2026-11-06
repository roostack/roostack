package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/photoimport"
)

// fakeImages is Podman's images for a test: `podman images` lists them,
// and `podman image rm` removes one, noted in removed.
type fakeImages struct {
	mu      sync.Mutex
	list    []podmanImage
	removed []string
}

func (f *fakeImages) launch(ctx context.Context, _ []string, name string, args ...string) *exec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case name == "podman" && len(args) > 0 && args[0] == "images":
		b, _ := json.Marshal(f.list)
		return exec.CommandContext(ctx, "printf", "%s", string(b))
	case name == "podman" && len(args) > 3 && args[0] == "image" && args[1] == "rm":
		id := args[3]
		f.removed = append(f.removed, id)
		f.list = slices.DeleteFunc(f.list, func(i podmanImage) bool { return i.ID == id })
		return exec.CommandContext(ctx, "true")
	}
	return exec.CommandContext(ctx, "false")
}

// Images nothing uses are offered, named by what they were; one a Quadlet
// file names, one a container uses, and the photo tool aren't; one a
// service stopped using days ago is kept for going back for a week.
// The nightly clean-up deletes old versions of services only.
func TestCleanUp(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	lab := config.Lab{Paths: config.Paths{HomeDir: home}}
	quadlets := lab.Paths.QuadletDir()
	os.MkdirAll(quadlets, 0o755)
	os.WriteFile(filepath.Join(quadlets, "roostack-photos-server.container"),
		[]byte("[Container]\nImage=ghcr.io/immich-app/immich-server:v3.3.5@sha256:new\nLabel=io.roostack.service=photos\nLabel=io.roostack.component=server\n"), 0o644)
	fake := &fakeImages{list: []podmanImage{
		{ID: "sha256:new", Names: []string{"ghcr.io/immich-app/immich-server:v3.3.5"}, RepoDigests: []string{"ghcr.io/immich-app/immich-server@sha256:new"}, Size: 100},
		{ID: "sha256:old", Names: []string{"ghcr.io/immich-app/immich-server:v3.3.3"}, RepoDigests: []string{"ghcr.io/immich-app/immich-server@sha256:old"}, Size: 90},
		{ID: "sha256:prev", Names: []string{"ghcr.io/immich-app/immich-server:v3.3.4"}, RepoDigests: []string{"ghcr.io/immich-app/immich-server@sha256:prev"}, Size: 95},
		{ID: "sha256:pulled", Names: []string{"docker.io/library/nginx:1"}, Size: 10},
		{ID: "sha256:build", Size: 5},
		{ID: "sha256:running", Names: []string{"docker.io/library/alpine:3"}, Containers: 1, Size: 3},
		{ID: "sha256:tool", Names: []string{photoimport.ToolImage}, Size: 7},
	}}
	now := time.Now()
	seen := map[string]imageSeen{
		"ghcr.io/immich-app/immich-server:v3.3.3@sha256:old":  {Service: "photos", Version: "3.3.3-roostack.1", Component: "server", At: now.Add(-10 * 24 * time.Hour)},
		"ghcr.io/immich-app/immich-server:v3.3.4@sha256:prev": {Service: "photos", Version: "3.3.4-roostack.1", Component: "server", At: now.Add(-24 * time.Hour)},
	}
	b, _ := json.Marshal(seen)
	state := memKV{seenKey: string(b)}
	n := &Local{Lab: lab, Launch: fake.launch, State: state, Activity: activity.New()}
	ctx := context.Background()

	rep, err := n.CleanUpReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var old []string
	for _, i := range rep.Old {
		old = append(old, i.ID)
	}
	if !slices.Equal(old, []string{"sha256:old", "sha256:pulled", "sha256:build"}) {
		t.Errorf("offered %v; want the old version, the one pulled by hand, and the build's, biggest first", old)
	}
	if o := rep.Old[0]; o.Service != "photos" || o.Version != "3.3.3-roostack.1" || o.Kind != ImageService || o.Repository() != "ghcr.io/immich-app/immich-server" {
		t.Errorf("the old version: %+v", o)
	}
	if rep.Old[2].Kind != ImageOwn {
		t.Errorf("an image left from a build is %q, want Roostack's own", rep.Old[2].Kind)
	}
	if len(rep.Kept) != 1 || rep.Kept[0].ID != "sha256:prev" || rep.Kept[0].KeptUntil.Before(now.Add(5*24*time.Hour)) {
		t.Errorf("kept for going back: %+v", rep.Kept)
	}
	if !rep.AutoClean {
		t.Error("the nightly clean-up is off by default")
	}
	// The image in use was noted as named now.
	if s := n.imagesSeen(ctx)["ghcr.io/immich-app/immich-server:v3.3.5@sha256:new"]; s.Service != "photos" || s.Component != "server" || now.Sub(s.At) > time.Minute {
		t.Errorf("the image in use was noted as %+v", s)
	}

	// Only what's offered is deleted.
	res, err := n.RemoveImages(ctx, []string{"sha256:pulled", "sha256:prev", "sha256:new"})
	if err != nil || res.Removed != 1 || res.Freed != 10 || !slices.Equal(fake.removed, []string{"sha256:pulled"}) {
		t.Errorf("removed %+v %v, podman asked for %v", res, err, fake.removed)
	}
	// Each night: old versions of services only.
	n.CleanNightly()
	if !slices.Equal(fake.removed, []string{"sha256:pulled", "sha256:old"}) {
		t.Errorf("the nightly clean-up removed %v", fake.removed)
	}
	if s, ok := n.Activity.Latest("clean-up"); !ok || s.Err != "" || s.Finished.IsZero() || !slices.Contains(s.Lines, "Photos 3.3.3 (ghcr.io/immich-app/immich-server)") {
		t.Errorf("the nightly clean-up's log: %+v %v", s, ok)
	}
	// Turned off, it does nothing.
	n.SetAutoClean(ctx, false)
	fake.list = append(fake.list, podmanImage{ID: "sha256:older", Names: []string{"ghcr.io/x/y:1"}, Size: 1})
	n.CleanNightly()
	if len(fake.removed) != 2 {
		t.Errorf("turned off, it removed %v", fake.removed)
	}
}
