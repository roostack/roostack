package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Media libraries are kept per server: added, changed by name, removed.
func TestMediaLibraries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	films := MediaLibrary{ServerID: "n-1", Name: "films", Title: "Films", Drive: "1111-2222", DriveName: "MEDIA", Sub: "Films",
		OwnerUID: 1000, OwnerGID: 1000, OwnerName: "sam"}
	for _, l := range []MediaLibrary{films, {ServerID: "n-1", Name: "music", Title: "Music", ShareType: "smb", ShareServer: "nas", SharePath: "music", ShareUser: "sam"},
		{ServerID: "n-2", Name: "films", Title: "Films", Folder: "/mnt/films"}} {
		if err := s.SetMediaLibrary(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	films.Title, films.OwnerUID, films.OwnerGID, films.OwnerName = "Our films", 0, 0, ""
	if err := s.SetMediaLibrary(ctx, films); err != nil {
		t.Fatal(err)
	}
	libs, err := s.MediaLibraries(ctx)
	if err != nil || len(libs) != 3 {
		t.Fatalf("%+v %v", libs, err)
	}
	if l := libs[1]; l.ServerID != "n-1" || l.Title != "Our films" || l.OwnerUID != 0 || l.Sub != "Films" || l.Created.IsZero() {
		t.Errorf("changed: %+v", l)
	}
	if l := libs[0]; l.Name != "music" || l.ShareType != "smb" || l.ShareServer != "nas" || l.SharePath != "music" || l.ShareUser != "sam" {
		t.Errorf("a share: %+v", l)
	}
	if err := s.DeleteMediaLibrary(ctx, "n-1", "films"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMediaLibrary(ctx, "n-1", "films"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting again: %v", err)
	}
	if libs, _ := s.MediaLibraries(ctx); len(libs) != 2 {
		t.Errorf("after deleting: %+v", libs)
	}
}
