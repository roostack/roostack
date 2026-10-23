package web

import (
	"bytes"
	"context"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
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
// under static/ (and on the page).
var statusPageFiles = []string{"css/tokens.css", "css/themes/teal.css", "css/app.css", "css/status.css", "fonts/space-grotesk-latin.woff2"}

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
		err = s.writeStatusFiles(page)
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
	data := map[string]any{"LabName": s.labName(), "Rows": rows, "Updated": now.Format("15:04")}
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
// they've changed (a new Roostack's stylesheet), each replaced whole so
// Caddy never serves half of one.
func (s *Server) writeStatusFiles(page []byte) error {
	files := map[string][]byte{"index.html": page}
	for _, name := range statusPageFiles {
		b, err := fs.ReadFile(staticFS, "static/"+name)
		if err != nil {
			return err
		}
		files[name] = b
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
