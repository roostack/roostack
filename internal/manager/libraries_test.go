package manager

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

type fakeLibraryStore struct{ libs []store.MediaLibrary }

func (f *fakeLibraryStore) MediaLibraries(context.Context) ([]store.MediaLibrary, error) {
	return f.libs, nil
}
func (f *fakeLibraryStore) SetMediaLibrary(_ context.Context, l store.MediaLibrary) error {
	f.libs = append(f.libs, l)
	return nil
}
func (f *fakeLibraryStore) DeleteMediaLibrary(context.Context, string, string) error { return nil }

// Each server gets its own libraries, as its agent mounts them.
func TestLibrariesFor(t *testing.T) {
	m := &Manager{Libraries: &fakeLibraryStore{libs: []store.MediaLibrary{
		{ServerID: "n-1", Name: "films", Drive: "1111-2222", Sub: "Films", OwnerUID: 1000, OwnerGID: 1000},
		{ServerID: "n-2", Name: "music", Folder: "/srv/music"},
	}}}
	got := m.librariesFor(context.Background(), "n-1")
	if len(got) != 1 || got[0].Name != "films" || got[0].Sub != "Films" || got[0].AsOwner == nil || got[0].AsOwner.UID != 1000 {
		t.Errorf("n-1: %+v", got)
	}
	if got := m.librariesFor(context.Background(), "n-2"); len(got) != 1 || got[0].Folder != "/srv/music" || got[0].AsOwner != nil {
		t.Errorf("n-2: %+v", got)
	}
}

// player is a package that reads media libraries, for the catalog.
const player = `schema: roostack/v1alpha1
name: player
title: Player
description: Plays media
version: "1"
about: { maker: Players Inc, licence: closed, website: "https://player.example.com", icon: icon.png, categories: [media] }
requires: { roostack: "0.1" }
storage:
  config: { title: Its database, class: critical }
containers:
  app:
    image: example/player:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    memory: 100 MB
    mounts:
      - { storage: config, path: /config }
      - { libraries: true, path: /media }
`

// A library can be shown at a place of its own inside a service (SPEC.md
// §8.4), checked as the instance checks it, and kept when the libraries
// change without paths given; a library it stops reading loses its path.
func TestServiceLibraryPaths(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	dir := m.Lab.Paths.CatalogPackageDir("player")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "1.yaml"), []byte(player), 0o644)
	icon, _ := os.ReadFile(filepath.Join(filepath.Dir(pkgtest.File("immich")), "icon.png"))
	os.WriteFile(filepath.Join(dir, "icon.png"), icon, 0o644)
	m.Libraries = &fakeLibraryStore{libs: []store.MediaLibrary{
		{ServerID: m.Lab.NodeID, Name: "films", Title: "Films"}, {ServerID: m.Lab.NodeID, Name: "tv", Title: "TV"}}}
	if err := m.Install(ctx, "player", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.SetServiceLibraries(ctx, "player", []string{"films", "tv"}, map[string]string{"films": "/movies/", "tv": " "}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	c, err := m.ServiceLibraries(ctx, "player")
	if err != nil || !maps.Equal(c.Paths, map[string]string{"films": "/movies"}) || c.Inside != "/media" || !slices.Equal(c.Binds, []string{"films"}) {
		t.Errorf("paths %+v %v", c, err)
	}
	err = m.SetServiceLibraries(ctx, "player", []string{"films"}, map[string]string{"films": "/config/films"})
	if msg, ok := err.(*usererr.Error); !ok || msg.Msg != "Films can't be at /config/films inside Player: it overlaps /config, where Player already has a folder." {
		t.Errorf("a path over its own folder: %v", err)
	}
	if err := m.SetServiceLibraries(ctx, "player", []string{"films"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if c, _ := m.ServiceLibraries(ctx, "player"); c.Paths["films"] != "/movies" || len(c.Chosen) != 1 {
		t.Errorf("paths kept: %+v", c)
	}
	if err := m.SetServiceLibraries(ctx, "player", []string{"tv"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if c, _ := m.ServiceLibraries(ctx, "player"); len(c.Paths) != 0 {
		t.Errorf("a library it doesn't read keeps its path: %+v", c)
	}
}
