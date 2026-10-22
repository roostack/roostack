package node

import (
	"context"
	"net/http"
	"net/url"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/usererr"
)

// Media is what the controller's pages ask a server about its media
// libraries (SPEC.md §8.1, "Media libraries"): its drives, the folders a
// library could be, and its libraries' state.
type Media interface {
	Drives(ctx context.Context) ([]agent.Drive, error)
	LibraryFolders(ctx context.Context, l agent.Library) (agent.FolderList, error)
	Libraries(ctx context.Context) ([]agent.LibraryStatus, error)
	CheckShare(ctx context.Context, s agent.Share) (agent.FolderList, error)
	SaveShareLogin(ctx context.Context, name string, s agent.Share) error
}

// Media is this server's media libraries, through its agent; nil without
// one.
func (n *Local) Media() Media {
	if n.Libraries == nil {
		return nil
	}
	return n.Libraries
}

// Media asks another server about its media libraries.
func (r *Remote) Media() Media { return remoteMedia{r} }

type remoteMedia struct{ r *Remote }

func (x remoteMedia) Drives(ctx context.Context) ([]agent.Drive, error) {
	var d []agent.Drive
	err := x.r.call(ctx, http.MethodGet, "/v1/media/drives", nil, &d)
	return d, err
}

func (x remoteMedia) LibraryFolders(ctx context.Context, l agent.Library) (agent.FolderList, error) {
	var list agent.FolderList
	err := x.r.call(ctx, http.MethodPost, "/v1/media/folders", l, &list)
	return list, err
}

func (x remoteMedia) Libraries(ctx context.Context) ([]agent.LibraryStatus, error) {
	var libs []agent.LibraryStatus
	err := x.r.call(ctx, http.MethodGet, "/v1/media/libraries", nil, &libs)
	return libs, err
}

func (x remoteMedia) CheckShare(ctx context.Context, s agent.Share) (agent.FolderList, error) {
	var list agent.FolderList
	err := x.r.call(ctx, http.MethodPost, "/v1/media/shares/check", s, &list)
	return list, err
}

func (x remoteMedia) SaveShareLogin(ctx context.Context, name string, s agent.Share) error {
	return x.r.call(ctx, http.MethodPut, "/v1/media/shares/"+url.PathEscape(name)+"/login", s, nil)
}

// mediaRoutes serves Media on the lab API.
func (s *Server) mediaRoutes(handle func(string, func(r *http.Request) (any, error)), decode func(*http.Request, any) error) {
	media := func() (Media, error) {
		if m := s.Node.Media(); m != nil {
			return m, nil
		}
		return nil, ErrNotFound
	}
	handle("GET /v1/media/drives", func(r *http.Request) (any, error) {
		m, err := media()
		if err != nil {
			return nil, err
		}
		return m.Drives(r.Context())
	})
	handle("POST /v1/media/folders", func(r *http.Request) (any, error) {
		m, err := media()
		if err != nil {
			return nil, err
		}
		var l agent.Library
		if err := decode(r, &l); err != nil {
			return nil, err
		}
		list, err := m.LibraryFolders(r.Context(), l)
		if err != nil {
			return nil, &usererr.Error{Msg: err.Error()}
		}
		return list, nil
	})
	handle("POST /v1/media/shares/check", func(r *http.Request) (any, error) {
		m, err := media()
		if err != nil {
			return nil, err
		}
		var sh agent.Share
		if err := decode(r, &sh); err != nil {
			return nil, err
		}
		list, err := m.CheckShare(r.Context(), sh)
		if err != nil {
			return nil, &usererr.Error{Msg: err.Error()}
		}
		return list, nil
	})
	handle("PUT /v1/media/shares/{name}/login", func(r *http.Request) (any, error) {
		m, err := media()
		if err != nil {
			return nil, err
		}
		var sh agent.Share
		if err := decode(r, &sh); err != nil {
			return nil, err
		}
		if err := m.SaveShareLogin(r.Context(), r.PathValue("name"), sh); err != nil {
			return nil, &usererr.Error{Msg: err.Error()}
		}
		return nil, nil
	})
	handle("GET /v1/media/libraries", func(r *http.Request) (any, error) {
		m, err := media()
		if err != nil {
			return nil, err
		}
		return m.Libraries(r.Context())
	})
}
