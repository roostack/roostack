package node

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labnet"
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

// LeftData asks the server over the lab API.
func (r *Remote) LeftData(ctx context.Context, name string) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodGet, "/v1/services/"+url.PathEscape(name)+"/left-data", nil, &out)
	// A server from before answers the route it doesn't know 404.
	if re := (*labnet.RemoteError)(nil); errors.As(err, &re) && re.Status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	return out, err
}
