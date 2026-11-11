package web

import (
	"bytes"
	"context"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/words"
)

// The public status page (SPEC.md §6.15): Roostack writes it every minute,
// after each round of checks, as plain files Caddy serves at
// status.<domain>: index.html, and the design system's stylesheets and
// font it links to. Nothing on it runs code, and it shows only the checks
// the Owner ticked: each one's name, state and days, never an address or
// what went wrong.

// statusPageFiles are the static files the page links to, by their path
// under static/ (and on the page), besides its theme's (statusTheme).
var statusPageFiles = []string{"css/tokens.css", "css/app.css", "css/status.css", "fonts/space-grotesk-latin.woff2"}

// statusTheme is the theme's stylesheet the page links to: the lab's
// (Name and look, since 2026-11-06; Teal before).
func statusTheme(palette string) string { return "css/themes/" + palette + ".css" }

// statusLogo is a logo file's name on the page (logo-wide.svg): the lab's
// logo (Name and look), copied beside the page.
func statusLogo(part, kind string) string { return "logo-" + part + "." + kind }

var statusPageTemplate = sync.OnceValues(func() (*template.Template, error) {
	return template.New("status-page.html").Funcs(templateFuncs()).ParseFS(templateFS, "templates/status-page.html")
})

// statusRow is one check on the status page.
type statusRow struct {
	Name, State, Uptime string
	Days                []monitor.Day
}

// WriteStatusPage writes the public status page while it's on (the
// monitor calls it after each round).
func (s *Server) WriteStatusPage(ctx context.Context) {
	if s.StatusPageDir == "" || s.Monitor == nil || !s.statusPageOn() {
		return
	}
	page, err := s.renderStatusPage(ctx, time.Now())
	if err == nil {
		err = s.writeStatusFiles(page, s.signInLook(ctx))
	}
	if err != nil {
		s.Log.Error("writing the status page", "err", err)
	}
}

// renderStatusPage makes the page's HTML.
func (s *Server) renderStatusPage(ctx context.Context, now time.Time) ([]byte, error) {
	statuses := s.Monitor.Statuses()
	shown := s.statusShown(ctx, statuses)
	var rows []statusRow
	var down []string
	for _, st := range statuses {
		if !shown[st.Key] || (st.Kind != monitor.KindService && st.Kind != monitor.KindOwn) {
			continue
		}
		row := statusRow{Name: st.Name, State: st.State}
		if st.PublicPaused {
			// What its visitors from outside get (SPEC.md §6.16).
			row.State = monitor.StatePaused
		}
		h, err := s.Monitor.History(ctx, st.Key, 90, false)
		if err != nil {
			return nil, err
		}
		row.Days = h.Days
		if t := h.Quarter.Text(); t != "" {
			row.Uptime = t
		}
		rows = append(rows, row)
		if row.State == monitor.StateDown {
			down = append(down, st.Name)
		}
	}
	look := s.signInLook(ctx)
	data := map[string]any{"LabName": s.labName(), "Rows": rows, "Updated": now.Format("15:04"), "Look": look,
		"Theme": statusTheme(look.Palette)}
	for _, part := range []string{"mark", "wide", "dark"} {
		if look.Has(part) {
			data["Logo"+part] = statusLogo(part, look.Files[part])
		}
	}
	switch {
	case len(down) == 0:
	case len(down) == len(rows):
		data["Down"] = "Nothing is working at the moment."
	case len(down) == 1:
		data["Down"] = down[0] + " isn't working at the moment. Everything else is."
	default:
		data["Down"] = words.And(down) + " aren't working at the moment. Everything else is."
	}
	t, err := statusPageTemplate()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeStatusFiles writes the page, and the files it links to where
// they've changed (a new Roostack's stylesheet, the lab's logo), each
// replaced whole so Caddy never serves half of one.
func (s *Server) writeStatusFiles(page []byte, look signInLook) error {
	files := map[string][]byte{"index.html": page}
	for _, part := range []string{"mark", "wide", "dark"} {
		if kind := look.Files[part]; kind != "" {
			b, err := os.ReadFile(s.lookPath(part, kind))
			if err != nil {
				return err
			}
			files[statusLogo(part, kind)] = b
		}
	}
	for _, name := range append(slices.Clone(statusPageFiles), statusTheme(look.Palette)) {
		b, err := fs.ReadFile(staticFS, "static/"+name)
		if err != nil {
			return err
		}
		files[name] = b
	}
	// A logo that's been deleted or replaced by another type must leave
	// the public folder with it.
	old, _ := filepath.Glob(filepath.Join(s.StatusPageDir, statusLogo("*", "*")))
	for _, path := range old {
		if _, keep := files[filepath.Base(path)]; !keep {
			os.Remove(path)
		}
	}
	for name, b := range files {
		path := filepath.Join(s.StatusPageDir, name)
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := fsutil.WriteFile(path, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
