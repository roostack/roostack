package manager

import (
	"context"
	"testing"

	"roostack.cloud/roostack/internal/store"
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
