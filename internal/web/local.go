package web

import (
	"errors"
	"io"
	"net/http"
	"net/url"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
)

// The Owner's own packages ("Add your own", SPEC.md §8.3.4 and §8.3.6):
// package files they add on the catalog page, never reviewed. Only the
// Owner adds and removes them (decided by the Owner, 2026-10-14).

// localUploadMax is the most an "Add your own" form can send: a package
// file, a logo and the form around them.
const localUploadMax = manager.LocalPackageMaxBytes + pkgspec.IconMaxBytes + 64<<10

func (s *Server) localAddPage(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "local-add", map[string]any{"User": currentUser(r), "Dir": s.Manager.LocalPackagesDir()})
}

func (s *Server) localAddSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "local-add", map[string]any{"User": currentUser(r), "Dir": s.Manager.LocalPackagesDir(), "Error": msg})
	}
	r.Body = http.MaxBytesReader(w, r.Body, localUploadMax)
	if err := r.ParseMultipartForm(localUploadMax); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail("Those files are too large: a package file is at most 1 MB, and its logo at most 100 KB.")
			return
		}
		http.Error(w, "Bad form.", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	data, err := uploaded(r, "package")
	if err != nil {
		s.serverError(w, err)
		return
	}
	icon, err := uploaded(r, "icon")
	if err != nil {
		s.serverError(w, err)
		return
	}
	p, err := s.Manager.AddLocalPackage(data, icon)
	if msg, ok := userMessage(err); ok {
		fail(msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", "added a package of their own: "+p.Name+" "+p.Version)
	redirect(w, r, "/catalog?added="+url.QueryEscape(p.Name))
}

// uploaded is the file sent in a form field, or nil if none was chosen.
func uploaded(r *http.Request, field string) ([]byte, error) {
	f, _, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if len(data) == 0 {
		return nil, err // an empty file input sends no bytes
	}
	return data, err
}

func (s *Server) localRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	err := s.Manager.RemoveLocalPackage(name)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", "removed a package of their own: "+name)
	redirect(w, r, "/catalog")
}
