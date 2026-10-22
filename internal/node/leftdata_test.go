package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"roostack.cloud/roostack/internal/config"
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
