package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/usererr"
)

// A service's extra environment variables (SPEC.md §8.1, "Extra
// environment variables"): the Environment variables card on its Settings
// page, for the Owner. Each variable is edited in its row, or all of a
// container's as text, which is checked before it's saved, asking which
// new values are secrets.

// envCard is the card's data.
type envCard struct {
	Service, Title, Docs, File string
	Containers                 []string
	// Multi: more than one container, so each row says which.
	Multi bool
	Rows  []envRow
	// Managed are the variables the package sets, by container.
	Managed []envNames
	// Texts are each container's box, from the variables or as submitted.
	Texts []envTextBox
	// AsText opens the text view.
	AsText bool
	// Error is why the last change was refused; AddName, AddContainer,
	// and AddSecret refill the add row (never its value).
	Error, AddName, AddContainer string
	AddSecret                    bool
	// Check is the check before saving the text, when it's shown.
	Check *envCheck
}

type envRow struct {
	Container, Name, Value string
	Secret                 bool
	// Shown is a secret's value the Owner asked to see.
	Shown string
}

type envNames struct {
	Container string
	Names     []string
}

type envTextBox struct{ Container, Text string }

// envCheck is the check before saving the text.
type envCheck struct {
	Changes  []envChange
	Problems []string
	// Unchanged counts what stays as it is; KeptSecrets names the secrets
	// among them, which keep their values.
	Unchanged   int
	KeptSecrets []string
	Texts       []envTextBox
	Multi       bool
}

type envChange struct {
	// Kind is Added, Changed, or Removed.
	Kind, Container, Name, Value, Was string
	// Secret is whether it will be a secret (ticked); Guessed, that
	// Roostack ticked it from its name.
	Secret, Guessed bool
}

// isOwner says whether the person signed in may change what only the
// Owner may (requireOwner).
func isOwner(r *http.Request) bool {
	u := currentUser(r)
	return u != nil && auth.Can(u.Role, auth.ManageHost)
}

// envCardFor is a service's card, for the Owner (nil otherwise).
func (s *Server) envCardFor(r *http.Request, name string) *envCard {
	if !isOwner(r) {
		return nil
	}
	se, err := s.Manager.ServiceEnv(name)
	if err != nil {
		return nil
	}
	c := &envCard{Service: name, Title: se.Title, Docs: se.Docs, File: se.File, Containers: se.Containers, Multi: len(se.Containers) > 1}
	for _, e := range se.Vars {
		c.Rows = append(c.Rows, envRow{Container: e.Container, Name: e.Name, Value: e.Value, Secret: e.Secret})
	}
	for _, ct := range se.Containers {
		c.Managed = append(c.Managed, envNames{Container: ct, Names: se.Managed[ct]})
		c.Texts = append(c.Texts, envTextBox{Container: ct, Text: envText(se.Vars, ct)})
	}
	return c
}

// renderEnv shows the Settings page with the card changed by edit.
func (s *Server) renderEnv(w http.ResponseWriter, r *http.Request, failed bool, edit func(*envCard)) {
	c := s.envCardFor(r, r.PathValue("name"))
	if c == nil {
		http.NotFound(w, r)
		return
	}
	edit(c)
	w.Header().Set("Cache-Control", "no-store") // a value shown, or typed
	s.renderServiceWith(w, r, map[string]any{"EditSettings": true, "Env": c, "EnvOpen": true}, failed)
}

// envSaved goes back to the card after a change.
func envSaved(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, "/services/"+r.PathValue("name")+"/settings#advanced")
}

// envSubmit changes one variable from its row: add, set (a new value), or
// remove.
func (s *Server) envSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	se, err := s.Manager.ServiceEnv(name)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	container := cmp.Or(r.FormValue("container"), se.Containers[0])
	vname := r.FormValue("var")
	value := r.FormValue("value")
	vars := slices.Clone(se.Vars)
	values := map[string]string{}
	at := slices.IndexFunc(vars, func(e instance.EnvVar) bool { return e.Container == container && e.Name == vname })
	what := ""
	switch r.FormValue("do") {
	case "add":
		if at >= 0 {
			s.envRefused(w, r, fmt.Sprintf("%s is already there: change it in its row.", vname))
			return
		}
		e := instance.EnvVar{Container: container, Name: vname, Value: value, Secret: r.FormValue("secret") == "on"}
		if e.Secret {
			e.Value, values[manager.EnvKey(container, vname)] = "", value
		}
		vars = append(vars, e)
		what = "added " + vname
	case "set":
		if at < 0 {
			http.NotFound(w, r)
			return
		}
		if vars[at].Secret {
			values[manager.EnvKey(container, vname)] = value
		} else {
			vars[at].Value = value
		}
		what = "changed " + vname
	case "remove":
		if at < 0 {
			http.NotFound(w, r)
			return
		}
		vars = slices.Delete(vars, at, at+1)
		what = "removed " + vname
	default:
		http.Error(w, "unknown change", http.StatusBadRequest)
		return
	}
	if err := s.Manager.SetEnv(r.Context(), name, vars, values); err != nil {
		if msg, ok := usererr.Message(err); ok {
			s.envRefused(w, r, msg)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "environment variables", name+": "+what)
	envSaved(w, r)
}

// envRefused shows the card with why a row's change was refused, keeping
// the name it was for but never the value.
func (s *Server) envRefused(w http.ResponseWriter, r *http.Request, msg string) {
	s.renderEnv(w, r, true, func(c *envCard) {
		c.Error = msg
		if r.FormValue("do") == "add" {
			c.AddName, c.AddContainer, c.AddSecret = r.FormValue("var"), r.FormValue("container"), r.FormValue("secret") == "on"
		}
	})
}

// envShowSubmit shows a secret's value to the Owner, and records that.
func (s *Server) envShowSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil {
		http.NotFound(w, r)
		return
	}
	name, container, vname := r.PathValue("name"), r.FormValue("container"), r.FormValue("var")
	v, err := n.RevealSecret(r.Context(), name, node.EnvRevealKey(container, vname))
	if errors.Is(err, manager.ErrNotFound) || errors.Is(err, node.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := usererr.Message(err); ok {
		s.renderEnv(w, r, false, func(c *envCard) { c.Error = msg })
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "secret shown", name+" "+vname)
	s.renderEnv(w, r, false, func(c *envCard) {
		for i := range c.Rows {
			if c.Rows[i].Container == container && c.Rows[i].Name == vname {
				c.Rows[i].Shown = v
			}
		}
	})
}

// envTexts are the submitted boxes, by container.
func envTexts(r *http.Request, containers []string) []envTextBox {
	var out []envTextBox
	for _, c := range containers {
		out = append(out, envTextBox{Container: c, Text: r.FormValue("text." + c)})
	}
	return out
}

// envFromText works out what saving the text would do: the variables it
// leaves, the new secret values, and the check to show. ticks are the
// Secret tick boxes as submitted (nil: Roostack's guesses).
func envFromText(se manager.ServiceEnv, texts []envTextBox, ticks map[string]bool) ([]instance.EnvVar, map[string]string, *envCheck) {
	check := &envCheck{Texts: texts, Multi: len(se.Containers) > 1}
	old := map[string]instance.EnvVar{}
	for _, e := range se.Vars {
		old[manager.EnvKey(e.Container, e.Name)] = e
	}
	var vars []instance.EnvVar
	values := map[string]string{}
	given := map[string]bool{}
	for _, box := range texts {
		lines, problems := parseEnvText(box.Text)
		prefix := ""
		if check.Multi {
			prefix = box.Container + ", "
		}
		for _, p := range problems {
			check.Problems = append(check.Problems, prefix+p)
		}
		for _, l := range lines {
			key := manager.EnvKey(box.Container, l.Name)
			given[key] = true
			was, had := old[key]
			if slices.Contains(se.Managed[box.Container], l.Name) {
				check.Problems = append(check.Problems, fmt.Sprintf("%sLine %d: %s's package sets %s itself, so it can't be added here. Remove that line to save the rest.",
					prefix, l.Line, se.Title, l.Name))
				continue
			}
			if l.Hidden {
				if !had || !was.Secret {
					check.Problems = append(check.Problems, fmt.Sprintf("%sLine %d: %s shows %s, but it has no saved value to keep: type its value instead.",
						prefix, l.Line, l.Name, envHidden))
					continue
				}
				vars = append(vars, was)
				check.Unchanged++
				check.KeptSecrets = append(check.KeptSecrets, l.Name)
				continue
			}
			if len(l.Value) > instance.MaxEnvValue {
				check.Problems = append(check.Problems, fmt.Sprintf("%sLine %d: %s's value is longer than 4 KB.", prefix, l.Line, l.Name))
				continue
			}
			secret, guessed := false, false
			if ticks != nil {
				secret = ticks[key]
			} else {
				guessed = looksSecret(l.Name) && !(had && !was.Secret)
				secret = guessed || (had && was.Secret)
			}
			if had && !was.Secret && !secret && was.Value == l.Value {
				vars = append(vars, was)
				check.Unchanged++
				continue
			}
			kind, wasText := "Added", ""
			if had {
				kind = "Changed"
				if !was.Secret {
					wasText = was.Value
				}
			}
			check.Changes = append(check.Changes, envChange{Kind: kind, Container: box.Container, Name: l.Name, Value: l.Value, Was: wasText,
				Secret: secret, Guessed: guessed && secret})
			e := instance.EnvVar{Container: box.Container, Name: l.Name, Secret: secret}
			if secret {
				values[key] = l.Value
			} else {
				e.Value = l.Value
			}
			vars = append(vars, e)
		}
	}
	for _, e := range se.Vars {
		if !given[manager.EnvKey(e.Container, e.Name)] {
			check.Changes = append(check.Changes, envChange{Kind: "Removed", Container: e.Container, Name: e.Name, Value: e.Value, Secret: e.Secret})
		}
	}
	if len(vars) > instance.MaxEnv {
		check.Problems = append(check.Problems, fmt.Sprintf("That's %d variables, and a service can have at most %d.", len(vars), instance.MaxEnv))
	}
	return vars, values, check
}

// envCheckSubmit shows what saving the text will do, before anything
// changes.
func (s *Server) envCheckSubmit(w http.ResponseWriter, r *http.Request) {
	se, err := s.Manager.ServiceEnv(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _, check := envFromText(se, envTexts(r, se.Containers), nil)
	s.renderEnv(w, r, false, func(c *envCard) { c.AsText, c.Check, c.Texts = true, check, check.Texts })
}

// envTextSubmit saves the text as checked, with the Secret tick boxes as
// left; or goes back to the text, as it was typed.
func (s *Server) envTextSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	se, err := s.Manager.ServiceEnv(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	texts := envTexts(r, se.Containers)
	if r.FormValue("back") != "" {
		s.renderEnv(w, r, false, func(c *envCard) { c.AsText, c.Texts = true, texts })
		return
	}
	ticks := map[string]bool{}
	r.ParseForm()
	for _, k := range r.PostForm["secret"] {
		ticks[k] = true
	}
	vars, values, check := envFromText(se, texts, ticks)
	if len(check.Problems) > 0 {
		s.renderEnv(w, r, true, func(c *envCard) { c.AsText, c.Check, c.Texts = true, check, texts })
		return
	}
	if err := s.Manager.SetEnv(r.Context(), name, vars, values); err != nil {
		if msg, ok := usererr.Message(err); ok {
			check.Problems = append(check.Problems, msg)
			s.renderEnv(w, r, true, func(c *envCard) { c.AsText, c.Check, c.Texts = true, check, texts })
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "environment variables", fmt.Sprintf("%s: saved as text, %d changed", name, len(check.Changes)))
	envSaved(w, r)
}
