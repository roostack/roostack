package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
)

// Installing a service, step by step (SPEC.md §6.3.3): the service's page,
// the Services list and Home show node.InstallProgress while it lasts.

// longStep is how long a step runs before the page says it's taking longer
// than usual (and whether it's still moving).
const longStep = 5 * time.Minute

func init() {
	templateFuncs["took"] = tookText
	templateFuncs["add1"] = func(i int) int { return i + 1 }
	templateFuncs["lowerFirst"] = func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToLower(s[:1]) + s[1:]
	}
	// slowStep: the install's current step has run for longer than usual.
	templateFuncs["slowStep"] = func(p *node.InstallProgress) bool {
		s := p.Step()
		return !s.Started.IsZero() && time.Since(s.Started) > longStep
	}
}

// tookText is how long something took, or has taken so far (to is zero):
// "14 s", "3 min", "1 h 5 min".
func tookText(from, to time.Time) string {
	if from.IsZero() {
		return ""
	}
	if to.IsZero() {
		to = time.Now()
	}
	d := to.Sub(from).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// installSummaryView is what installing a service would set up, shown
// above the Install button and updated as the form changes (SPEC.md
// §8.3.4). The figures come from the manager (generate.Summarize, and the
// server's memory); this only words them.
type installSummaryView struct {
	*manager.InstallSummary
	Package *pkgspec.Package
	// TooBig: it expects more memory than is free on its server right now
	// (a warning; installing still works).
	TooBig bool
	// Err is why there's no summary yet: something on the form to fix.
	Err string
	// BackedUp and Remade list what it keeps, in words: what's backed
	// up, and what the app makes again if it's lost.
	BackedUp, Remade string
	// Internet: the lab is on the internet, so private means only
	// through an SSH tunnel. NoAddress: the lab has no address yet.
	Internet, NoAddress bool
	// Params are the form fields the summary is asked for with: every
	// one but the passwords, which it doesn't need (htmx's hx-params).
	Params string
}

// installSummary works out the summary for an install request.
func (s *Server) installSummary(p *pkgspec.Package, req manager.InstallRequest) *installSummaryView {
	v := &installSummaryView{Package: p, Internet: s.onInternet(), NoAddress: s.Domain == "", Params: "*"}
	var secrets []string
	for _, st := range p.Settings {
		if st.Type == pkgspec.SettingSecret && !st.Generate {
			secrets = append(secrets, "setting."+st.Key)
		}
	}
	if len(secrets) > 0 {
		v.Params = "not " + strings.Join(secrets, ",")
	}
	sum, err := s.Manager.InstallSummary(p.Name, req)
	if msg, ok := userMessage(err); ok {
		v.Err = msg
		return v
	}
	if err != nil {
		s.Log.Error("install summary", "package", p.Name, "err", err)
		v.Err = "Roostack couldn't work it out: the log has the details."
		return v
	}
	v.InstallSummary = sum
	v.TooBig = sum.Server.Available > 0 && sum.Memory > sum.Server.Available
	var backedUp, remade []string
	databases := 0
	for _, k := range sum.Kept {
		name := cmp.Or(k.Title, k.Name)
		switch {
		case k.Database != "" && k.BackedUp:
			databases++
		case k.Database == "":
			if k.BackedUp {
				backedUp = append(backedUp, name)
			} else if k.Replaceable {
				remade = append(remade, name)
			}
		}
	}
	switch {
	case databases == 1:
		backedUp = append(backedUp, "its database")
	case databases > 1:
		backedUp = append(backedUp, "its databases")
	}
	v.BackedUp = listText(backedUp, false)
	v.Remade = listText(remade, true)
	return v
}

// listText joins phrases into one: "a, b and c". A phrase with "and" of
// its own gets a comma before the last "and", so the list stays clear
// ("Your photos and videos, and its database"). lower starts every phrase
// in lower case, for a list after a colon; otherwise only the first keeps
// its capital.
func listText(items []string, lower bool) string {
	if len(items) == 0 {
		return ""
	}
	out := slices.Clone(items)
	for i := range out {
		if lower || i > 0 {
			out[i] = lowerFirstWord(out[i])
		}
	}
	if len(out) == 1 {
		return out[0]
	}
	last := " and "
	if slices.ContainsFunc(out, func(s string) bool { return strings.Contains(s, " and ") }) {
		last = ", and "
	}
	return strings.Join(out[:len(out)-1], ", ") + last + out[len(out)-1]
}

// lowerFirstWord lowers a phrase's first letter, unless its first word
// looks like a name (another capital in it, such as "PostgreSQL").
func lowerFirstWord(s string) string {
	word, _, _ := strings.Cut(s, " ")
	if len(word) < 2 || strings.ToLower(word[1:]) != word[1:] {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// installSummaryPart is the summary again, for the install form as it
// stands (htmx, as the form changes).
func (s *Server) installSummaryPart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pkg(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form.", http.StatusBadRequest)
		return
	}
	s.renderPart(w, "install", "install-summary", s.installSummary(p, s.installRequest(r, r.PostForm, p)))
}

// packageFile shows a catalog package's file as written, for anyone who
// wants to read exactly what it asks for before installing (SPEC.md
// §8.3.4).
func (s *Server) packageFile(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.Manager.PackageFile(r.PathValue("name"))
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// packageIcon serves a catalog package's logo.
func (s *Server) packageIcon(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.Manager.PackageIcon(r.PathValue("name"))
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(data)
}

// projectCredit is who makes an app, its licence and its links (about:
// in its package, SPEC.md §8.3.6), for the project-credit component.
type projectCredit struct {
	Title   string
	About   pkgspec.About
	IconURL string
}

// newProjectCredit is a catalog package's credit, or nil if its package
// says nothing about its project.
func newProjectCredit(p *pkgspec.Package) *projectCredit {
	a := p.About
	if a.Maker == "" && a.Licence == "" && a.Website == "" && a.Source == "" && a.Support == "" {
		return nil
	}
	c := &projectCredit{Title: p.Title, About: a}
	if a.Icon != "" {
		c.IconURL = "/install/" + p.Name + "/icon.png"
	}
	return c
}

// licenceName is how a licence is shown: its SPDX identifier, with
// "-only" left out and "-or-later" (or +) in words: "AGPL-3.0",
// "GPL-3.0 or later".
func licenceName(spdx string) string {
	s := strings.ReplaceAll(spdx, "-only", "")
	s = strings.ReplaceAll(s, "-or-later", " or later")
	if strings.HasSuffix(s, "+") {
		s = strings.TrimSuffix(s, "+") + " or later"
	}
	return s
}

// licenceURL is where a licence's text is, on spdx.org: only for a single
// licence (not "MIT OR Apache-2.0", nor the project's own LicenseRef-).
func licenceURL(spdx string) string {
	if spdx == pkgspec.LicenceClosed || strings.Contains(spdx, " ") || strings.HasPrefix(spdx, "LicenseRef-") {
		return ""
	}
	return "https://spdx.org/licenses/" + strings.TrimSuffix(spdx, "+") + ".html"
}

func init() {
	templateFuncs["licenceName"] = licenceName
	templateFuncs["licenceURL"] = licenceURL
	templateFuncs["databaseName"] = databaseName
	templateFuncs["pathBase"] = path.Base
	templateFuncs["pathDir"] = path.Dir
}

// databaseName is a database type's name, as its makers write it.
func databaseName(kind string) string {
	switch kind {
	case pkgspec.DBPostgres:
		return "PostgreSQL"
	case pkgspec.DBRedis:
		return "Redis"
	case pkgspec.DBMySQL:
		return "MySQL"
	case pkgspec.DBMariaDB:
		return "MariaDB"
	case pkgspec.DBSQLite:
		return "SQLite"
	}
	return kind
}
