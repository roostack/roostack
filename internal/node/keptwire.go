package node

import (
	"context"
	"net/http"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/kept"
)

// Kept copies (Mover.KeptCopies, kept.go) on the lab API.
//
// Until 2026-10-23 a server had two lists, each with its own routes: the
// folders moves and imports set aside (/v1/kept), and per service, those a
// restore on it set aside (/v1/restores/{service}/kept). Servers in a lab
// can briefly run different versions, so this server still answers the
// old routes, for a controller from before then, and the controller still
// uses them for a server from before then, which doesn't offer the new
// ones (WireStatus.Kept). Both go once no server can be that old (TASKS.md).

// KeptCopies, PutBackKept and RemoveKept use the kept-copies routes of a
// server that offers them, and otherwise the old ones, which list only
// whole folders, one by one.
func (r *Remote) KeptCopies(ctx context.Context) ([]kept.Copy, error) {
	if r.keptRoutes() {
		var out []kept.Copy
		err := r.call(ctx, http.MethodGet, "/v1/kept-copies", nil, &out)
		return out, err
	}
	var old []oldKeptFolder
	if err := r.call(ctx, http.MethodGet, "/v1/kept", nil, &old); err != nil {
		return nil, err
	}
	return oldKeptCopies(old), nil
}

func (r *Remote) PutBackKept(ctx context.Context, path string) error {
	if r.keptRoutes() {
		return r.call(ctx, http.MethodPost, "/v1/kept-copies/put-back", pathRequest{Path: path}, nil)
	}
	return r.call(ctx, http.MethodPost, "/v1/kept/put-back", pathRequest{Path: path}, nil)
}

func (r *Remote) RemoveKept(ctx context.Context, path string) error {
	if r.keptRoutes() {
		return r.call(ctx, http.MethodPost, "/v1/kept-copies/remove", pathRequest{Path: path}, nil)
	}
	// The old route deletes one folder: each of the copy's, then.
	copies, err := r.KeptCopies(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(copies, func(c kept.Copy) bool { return c.Has(path) })
	if i < 0 {
		return ErrNotFound
	}
	for _, p := range copies[i].Paths {
		if err := r.call(ctx, http.MethodPost, "/v1/kept/remove", pathRequest{Path: p}, nil); err != nil {
			return err
		}
	}
	return nil
}

// keptRoutes says whether the server has the kept-copies routes.
func (r *Remote) keptRoutes() bool {
	st, err := r.wireStatus()
	return err == nil && st.Kept
}

// keptRoutes serves kept copies on the lab API, and the old routes.
func keptRoutes(move func(pattern string, fn func(Mover, *http.Request) (any, error))) {
	move("GET /v1/kept-copies", func(mv Mover, r *http.Request) (any, error) { return mv.KeptCopies(r.Context()) })
	move("POST /v1/kept-copies/put-back", func(mv Mover, r *http.Request) (any, error) {
		req, err := body[pathRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, mv.PutBackKept(r.Context(), req.Path)
	})
	move("POST /v1/kept-copies/remove", func(mv Mover, r *http.Request) (any, error) {
		req, err := body[pathRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, mv.RemoveKept(r.Context(), req.Path)
	})

	// The old routes. Removing a folder removes its whole copy: an old
	// controller lists the copy's folders one by one, so its others will
	// have gone when its page shows again.
	move("GET /v1/kept", func(mv Mover, r *http.Request) (any, error) {
		copies, err := mv.KeptCopies(r.Context())
		return oldKeptFolders(copies), err
	})
	move("POST /v1/kept/put-back", func(mv Mover, r *http.Request) (any, error) {
		req, err := body[pathRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, mv.PutBackKept(r.Context(), req.Path)
	})
	remove := func(mv Mover, r *http.Request) (any, error) {
		req, err := body[pathRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, mv.RemoveKept(r.Context(), req.Path)
	}
	move("POST /v1/kept/remove", remove)
	move("GET /v1/restores/{service}/kept", func(mv Mover, r *http.Request) (any, error) {
		copies, err := mv.KeptCopies(r.Context())
		return oldRestoreCopies(copies, svcOf(r)), err
	})
	move("POST /v1/restores/{service}/kept/remove", remove)
}

// oldKeptFolder is a whole kept folder as the old /v1/kept listed it.
type oldKeptFolder struct {
	Path, Service                                           string
	MovedAway, Imported, FailedImport, Replaced, FailedMove bool
	Time                                                    time.Time
	Size                                                    int64
}

// oldRestoreCopy is a folder a restore kept, as the old
// /v1/restores/{service}/kept listed it.
type oldRestoreCopy struct {
	Path, Of string
	Failed   bool
	Time     time.Time
}

// oldKeptFolders lists the whole copies' folders, one by one (each with
// its copy's size, for an old controller: no folder has its own).
func oldKeptFolders(copies []kept.Copy) []oldKeptFolder {
	out := []oldKeptFolder{}
	for _, c := range copies {
		if !c.Whole {
			continue
		}
		for _, p := range c.Paths {
			out = append(out, oldKeptFolder{Path: p, Service: c.Service, MovedAway: c.Why == kept.MovedAway,
				Imported: c.Why == kept.BeforeImport, FailedImport: c.Why == kept.FailedImport,
				Replaced: c.Why == kept.BeforePutBack, FailedMove: c.Why == kept.FailedMove, Time: c.Time, Size: -1})
		}
	}
	return out
}

// oldRestoreCopies lists a service's copies a restore here kept, folder by
// folder.
func oldRestoreCopies(copies []kept.Copy, service string) []oldRestoreCopy {
	out := []oldRestoreCopy{}
	for _, c := range copies {
		if c.Whole || c.Service != service {
			continue
		}
		for _, p := range c.Paths {
			f, _ := kept.Parse(p)
			out = append(out, oldRestoreCopy{Path: p, Of: f.Of, Failed: c.Why == kept.FailedRestore, Time: c.Time})
		}
	}
	return out
}

// oldKeptCopies gathers an old server's kept folders into copies.
func oldKeptCopies(old []oldKeptFolder) []kept.Copy {
	var folders []kept.Folder
	size := map[string]int64{}
	for _, o := range old {
		if f, ok := kept.Parse(o.Path); ok {
			f.Whole = true
			folders = append(folders, f)
			size[f.Path] = o.Size
		}
	}
	copies := kept.Group(folders)
	for i, c := range copies {
		for _, p := range c.Paths {
			if size[p] >= 0 {
				copies[i].Size = max(copies[i].Size, 0) + size[p]
			}
		}
	}
	return copies
}
