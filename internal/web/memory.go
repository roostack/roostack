package web

import (
	"errors"
	"net/http"
	"strings"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/units"
)

// A service's memory (SPEC.md §6.1, "Memory"): its page says when the
// kernel is holding it back at its limit, and its settings page says what
// the limit is and why, and lets the Owner or an Admin choose another.

// memoryView is the Memory card on a service's settings page.
type memoryView struct {
	*manager.ServiceMemory
	// Use is its latest reading (nil before the first): what it uses, its
	// limit as the kernel has it, and whether it was held back.
	Use *node.ServiceUse
	// Mode is the choice shown ("auto", "limit", or "none"), Size the
	// figure in the box, and Error what's wrong with what was entered.
	Mode, Size, Error string
	// Cap is what all the services on its server may use together.
	Cap int64
	// Name and Title are the service's.
	Name, Title string
}

// Placeholder is the size suggested in the box: twice what its package
// expects, as Roostack would choose.
func (v *memoryView) Placeholder() string {
	if v.Figure == 0 {
		return "1 GB"
	}
	return units.Bytes(2 * v.Figure)
}

// serviceUse is a service's latest reading, from whichever server runs it
// (nil before the first).
func (s *Server) serviceUse(name string) *node.ServiceUse {
	if s.Vitals == nil {
		return nil
	}
	for _, sn := range s.Vitals.Snapshots() {
		for _, u := range sn.Reading.Services {
			if u.Service == name {
				return &u
			}
		}
	}
	return nil
}

// memoryCard is the Memory card's view; mode and size are what was
// entered, when the form came back with an error.
func (s *Server) memoryCard(r *http.Request, name, mode, size, msg string) *memoryView {
	m, err := s.Manager.ServiceMemory(r.Context(), name)
	if err != nil {
		if !errors.Is(err, manager.ErrNotFound) {
			s.Log.Warn("a service's memory", "service", name, "err", err)
		}
		return nil
	}
	v := &memoryView{ServiceMemory: m, Use: s.serviceUse(name), Mode: mode, Size: size, Error: msg,
		Cap: generate.ServicesMemory(m.Total), Name: name, Title: name}
	if v.Mode == "" {
		switch m.Choice {
		case "":
			v.Mode = "auto"
		case instance.MemoryNoLimit:
			v.Mode = "none"
		default:
			v.Mode, v.Size = "limit", m.Choice
		}
	}
	return v
}

// memorySubmit records the choice of a service's memory limit.
func (s *Server) memorySubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	mode, size := r.FormValue("memory"), strings.TrimSpace(r.FormValue("memory_size"))
	limit := ""
	switch mode {
	case "none":
		limit = instance.MemoryNoLimit
	case "limit":
		limit = size
		if limit == "" {
			limit = "?" // refused below, with the sentence
		}
		// "800MB" and "1.5gb" as well as "800 MB".
		limit = strings.ToUpper(strings.ReplaceAll(limit, " ", ""))
		if i := strings.IndexAny(limit, "MG"); i > 0 {
			limit = limit[:i] + " " + limit[i:]
		}
	}
	err := s.Manager.SetMemoryLimit(r.Context(), name, limit)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := usererr.Message(err); ok {
		s.renderServiceWith(w, r, map[string]any{"EditSettings": true, "Memory": s.memoryCard(r, name, mode, size, msg)}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	detail := name + "'s memory limit is now chosen by Roostack"
	switch {
	case limit == instance.MemoryNoLimit:
		detail = name + " may now use whatever memory it needs"
	case limit != "":
		detail = name + "'s memory limit is now " + limit
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", detail)
	redirect(w, r, "/services/"+name+"/settings#memory")
}
