package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
)

// A service's data from before is its folders with something in them; an
// empty or missing folder isn't, and a name with a path in it is refused.
func TestLeftData(t *testing.T) {
	root := t.TempDir()
	lab := config.Lab{Paths: config.Paths{ServicesDir: filepath.Join(root, "services"), MediaDir: filepath.Join(root, "media")}}
	n := &Local{Lab: lab}
	ctx := context.Background()
	if dirs, err := n.LeftDataDirs(ctx, "forgejo"); err != nil || len(dirs) != 0 {
		t.Errorf("nothing there: %q %v", dirs, err)
	}
	os.MkdirAll(filepath.Join(root, "services", "forgejo", "data"), 0o755)
	os.MkdirAll(filepath.Join(root, "media", "forgejo"), 0o755)
	if dirs, err := n.LeftDataDirs(ctx, "forgejo"); err != nil || !slices.Equal(dirs, []string{filepath.Join(root, "services", "forgejo")}) {
		t.Errorf("its data, and an empty media folder: %q %v", dirs, err)
	}
	if _, err := n.LeftDataDirs(ctx, "../services"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a path for a name: %v", err)
	}
}

// A removed service's data is listed with its folders; deleting it
// removes them and its secrets, but not another service's whose name
// starts with it, and a service installed here is refused.
func TestLeftServices(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lab := config.Lab{Paths: config.Paths{ServicesDir: filepath.Join(root, "services"), MediaDir: filepath.Join(root, "media"), HomeDir: filepath.Join(root, "home")}}
	secrets := &memSecrets{m: map[string][]byte{"roostack-immich-db": nil, "roostack-immich-sso-client-secret": nil,
		"roostack-immich-2-db": nil, "roostack-vaultwarden-admin": nil}}
	n := &Local{Lab: lab, Launch: direct, Secrets: secrets}
	n.assignment = Assignment{Services: []state.Service{{Instance: &instance.Instance{Package: "immich", Name: "immich-2"}},
		{Instance: &instance.Instance{Package: "vaultwarden"}}}}
	for _, d := range []string{"services/immich/db", "media/immich/library", "services/immich-2/db", "services/vaultwarden/data",
		"services/immich.before-restore-20261022-141205/db", "services/empty"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	for _, f := range []string{"services/immich/db/x", "media/immich/library/photo.jpg", "services/immich-2/db/x", "services/vaultwarden/data/x"} {
		os.WriteFile(filepath.Join(root, f), []byte("data"), 0o644)
	}
	ctx := context.Background()
	left, err := n.LeftServices(ctx)
	if err != nil || len(left) != 1 || left[0].Name != "immich" ||
		!slices.Equal(left[0].Dirs, []string{filepath.Join(root, "services", "immich"), filepath.Join(root, "media", "immich")}) {
		t.Fatalf("left = %+v, %v; want only immich, with both folders", left, err)
	}
	if err := n.DeleteLeftData(ctx, "vaultwarden"); err == nil {
		t.Error("deleting an installed service's data")
	}
	if err := n.DeleteLeftData(ctx, "empty"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty folder: %v, want not found", err)
	}
	// Taken off the server, but the apply hasn't stopped it yet: immich-2's
	// file (another service) doesn't count.
	quadlets := lab.Paths.QuadletDir()
	os.MkdirAll(quadlets, 0o755)
	os.WriteFile(filepath.Join(quadlets, "roostack-immich-2-server.container"), []byte("[Container]\nLabel=io.roostack.service=immich-2\n"), 0o644)
	os.WriteFile(filepath.Join(quadlets, "roostack-immich-server.container"), []byte("[Container]\nLabel=io.roostack.service=immich\n"), 0o644)
	if err := n.DeleteLeftData(ctx, "immich"); err == nil {
		t.Error("deleted the data of a service still stopping")
	}
	os.Remove(filepath.Join(quadlets, "roostack-immich-server.container"))
	if err := n.DeleteLeftData(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"services/immich", "media/immich"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "services/immich.before-restore-20261022-141205")); err != nil {
		t.Error("the kept copy went too")
	}
	got, _ := secrets.List(ctx)
	if got["roostack-immich-db"] || got["roostack-immich-sso-client-secret"] || !got["roostack-immich-2-db"] || !got["roostack-vaultwarden-admin"] {
		t.Errorf("secrets left: %v", got)
	}
}
