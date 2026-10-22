package node

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"roostack.cloud/roostack/internal/instance"
)

// LeftData is a server that can say whether a service's data from before
// is still on it (removing a service keeps its data), for the install page
// to say it will be used (SPEC.md §8.1, "hooks").
type LeftData interface {
	// LeftData lists the service's data folders that have something in
	// them (none: it would start afresh).
	LeftData(ctx context.Context, name string) ([]string, error)
}

var (
	_ LeftData = (*Local)(nil)
	_ LeftData = (*Remote)(nil)
)

// LeftData looks in the service's folders on this server.
func (n *Local) LeftData(_ context.Context, name string) ([]string, error) {
	if !instance.ValidName(name) {
		return nil, ErrNotFound
	}
	var out []string
	for _, dir := range []string{filepath.Join(n.Lab.Paths.ServicesDir, name), filepath.Join(n.Lab.Paths.MediaDir, name)} {
		f, err := os.Open(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_, err = f.Readdirnames(1)
		f.Close()
		if err == nil {
			out = append(out, dir)
		} else if !errors.Is(err, io.EOF) {
			return nil, err
		}
	}
	return out, nil
}
