package node

import (
	"context"
	"net/http"
	"net/url"

	"roostack.cloud/roostack/internal/importer"
)

// Bringing data into another server's services (import.go), over the lab
// API.

// Imports asks another server to bring data into its services.
func (r *Remote) Imports() Imports {
	if st, err := r.wireStatus(); err != nil || !st.Imports {
		return nil
	}
	return remoteImports{r}
}

type remoteImports struct{ r *Remote }

func importPath(service, rest string) string { return "/v1/imports/" + url.PathEscape(service) + rest }

// importSetupRequest is the old setup to keep (nil forgets it).
type importSetupRequest struct {
	Setup *importer.Setup `json:"setup"`
}

func (x remoteImports) Import(ctx context.Context, service string) (ImportView, error) {
	var v ImportView
	err := x.r.call(ctx, http.MethodGet, importPath(service, ""), nil, &v)
	return v, err
}

func (x remoteImports) SetImportSource(ctx context.Context, service string, src ImportSource) error {
	return x.r.call(ctx, http.MethodPut, importPath(service, "/source"), src, nil)
}

func (x remoteImports) SetImportSetup(ctx context.Context, service string, setup *importer.Setup) error {
	return x.r.call(ctx, http.MethodPut, importPath(service, "/setup"), importSetupRequest{Setup: setup}, nil)
}

func (x remoteImports) SetImportPrefixes(ctx context.Context, service string, prefixes []importer.Prefix) error {
	return x.r.call(ctx, http.MethodPut, importPath(service, "/prefixes"), prefixes, nil)
}

func (x remoteImports) SetImportChoices(ctx context.Context, service string, ch importer.Choices) error {
	return x.r.call(ctx, http.MethodPut, importPath(service, "/choices"), ch, nil)
}

func (x remoteImports) BrowseImport(ctx context.Context, service, rel string) (ImportListing, error) {
	var l ImportListing
	err := x.r.call(ctx, http.MethodGet, importPath(service, "/browse?path="+url.QueryEscape(rel)), nil, &l)
	return l, err
}

func (x remoteImports) CheckImport(ctx context.Context, service string) error {
	return x.r.call(ctx, http.MethodPost, importPath(service, "/check"), struct{}{}, nil)
}

func (x remoteImports) CopyImport(ctx context.Context, service string) error {
	return x.r.call(ctx, http.MethodPost, importPath(service, "/copy"), struct{}{}, nil)
}

func (x remoteImports) StopImportCopy(ctx context.Context, service string) error {
	return x.r.call(ctx, http.MethodPost, importPath(service, "/copy/stop"), struct{}{}, nil)
}

func (x remoteImports) SwitchImport(ctx context.Context, service, step string) error {
	return x.r.call(ctx, http.MethodPost, importPath(service, "/switch/"+url.PathEscape(step)), struct{}{}, nil)
}

func (x remoteImports) CancelImport(ctx context.Context, service string) error {
	return x.r.call(ctx, http.MethodDelete, importPath(service, ""), nil, nil)
}

// importRoutes serves Imports on the lab API.
func (s *Server) importRoutes(handle handleFunc) {
	with := capRoute(handle, offered(s.Node.Imports))
	with("GET /v1/imports/{service}", func(x Imports, r *http.Request) (any, error) { return x.Import(r.Context(), svcOf(r)) })
	with("DELETE /v1/imports/{service}", func(x Imports, r *http.Request) (any, error) {
		return nil, x.CancelImport(r.Context(), svcOf(r))
	})
	with("PUT /v1/imports/{service}/source", func(x Imports, r *http.Request) (any, error) {
		src, err := body[ImportSource](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetImportSource(r.Context(), svcOf(r), src)
	})
	with("PUT /v1/imports/{service}/setup", func(x Imports, r *http.Request) (any, error) {
		req, err := body[importSetupRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetImportSetup(r.Context(), svcOf(r), req.Setup)
	})
	with("PUT /v1/imports/{service}/prefixes", func(x Imports, r *http.Request) (any, error) {
		prefixes, err := body[[]importer.Prefix](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetImportPrefixes(r.Context(), svcOf(r), prefixes)
	})
	with("PUT /v1/imports/{service}/choices", func(x Imports, r *http.Request) (any, error) {
		ch, err := body[importer.Choices](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetImportChoices(r.Context(), svcOf(r), ch)
	})
	with("GET /v1/imports/{service}/browse", func(x Imports, r *http.Request) (any, error) {
		return x.BrowseImport(r.Context(), svcOf(r), r.URL.Query().Get("path"))
	})
	with("POST /v1/imports/{service}/check", func(x Imports, r *http.Request) (any, error) {
		return nil, x.CheckImport(r.Context(), svcOf(r))
	})
	with("POST /v1/imports/{service}/copy", func(x Imports, r *http.Request) (any, error) {
		return nil, x.CopyImport(r.Context(), svcOf(r))
	})
	with("POST /v1/imports/{service}/copy/stop", func(x Imports, r *http.Request) (any, error) {
		return nil, x.StopImportCopy(r.Context(), svcOf(r))
	})
	with("POST /v1/imports/{service}/switch/{step}", func(x Imports, r *http.Request) (any, error) {
		return nil, x.SwitchImport(r.Context(), svcOf(r), r.PathValue("step"))
	})
}
