package web

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
)

// The design gallery shows every token and component, in light and dark
// side by side (docs/DESIGN.md). It is a page in the UI (/design) and a
// standalone file, docs/design/gallery.html, generated from the same
// templates (go test ./internal/web -run TestGalleryFile -update-gallery).

// galleryExample is one component, written as a template snippet so the
// gallery renders the real components.
type galleryExample struct {
	Name, Description, Source string
}

type gallerySection struct {
	ID, Title, Intro string
	Examples         []galleryExample
}

// Purpose tokens shown as swatches (gallery.css has a class for each).
var galleryTokens = []struct{ Name, Use string }{
	{"bg", "Page background"}, {"surface", "Cards"}, {"surface-2", "Inputs, code, insets"},
	{"fg", "Text"}, {"muted", "Secondary text"}, {"border", "Borders"}, {"border-strong", "Input borders"},
	{"accent", "Actions and links"}, {"accent-soft", "Info backgrounds"},
	{"highlight", "Brand mark, where you are, focus"}, {"highlight-soft", "Highlight backgrounds"},
	{"ok", "Healthy"}, {"warn", "Needs attention soon"}, {"bad", "Problem"},
	{"ok-on-fg", "Online lights in the brand mark (on text-coloured shapes)"},
}

var gallerySections = []gallerySection{
	{ID: "brand", Title: "Brand mark", Intro: "Roostack's mark, a hen on a server, drawn in the theme's colours: text colour on a soft-highlight tile, with a highlight comb. It's generated from assets/brand/generate.py; don't edit it by hand.",
		Examples: []galleryExample{
			{"Top bar", "brandMark inside .brand, before the name. Decorative (hidden from screen readers): the name says what it is.", `
<a class="brand" href="#">{{brandMark}}Roostack</a>`},
			{"Top bar with the lab's name", "Once the lab has a name, the mark grows to sit beside both lines.", `
<a class="brand" href="#">{{brandMark}}<span class="brand-stack"><span class="brand-eyebrow">Roostack</span><span class="brand-lab">The Smiths' lab</span></span></a>`},
		}},
	{ID: "buttons", Title: "Buttons", Intro: "One primary button per form or card; others secondary or quiet.",
		Examples: []galleryExample{
			{"Primary, secondary, danger, quiet", "Primary does the main thing. Danger removes or deletes. Quiet is for small or optional actions.", `
<div class="actions-row">
  <button>Back up now</button>
  <button class="secondary">Cancel</button>
  <button class="danger">Remove</button>
  <button class="quiet">Set this up later</button>
</div>`},
			{"Link styled as a button", "For navigation that looks like an action.", `
<a class="button" href="#">Add a service</a>
<a class="button secondary" href="#">Open backups</a>`},
			{"Confirm", "confirm ACTION QUESTION TEXT KIND: a button that asks the browser to confirm first. Use it for anything hard to undo. KIND is the button's class (danger, secondary, quiet, or \"\"). The question can contain any text (it's passed as a data attribute, never into script).", `
{{confirm "/users/3/remove" "Remove dad? They will no longer be able to log in." "Remove dad" "quiet danger"}}`},
			{"Confirm with a hidden field", "Trailing name=value arguments become hidden form fields.", `
{{confirm "/backups/restore/forgejo/kept/remove" "Delete this copy permanently?" "Delete" "quiet danger" "path=/srv/roostack/services/forgejo/data.before-restore-20260926-130602"}}`},
			{"Confirm a form", "confirmSubmit QUESTION TEXT KIND: the submit button of a form that has fields of its own (checkboxes, choices), which confirm can't carry. The form's own checks (required fields) run first; the dialog's button submits the form. KIND is the dialog button's class (danger or \"\").", `
<form method="post" action="#">
  <label class="check"><input type="checkbox" name="open" value="/admin"><span>Make <code>/admin</code> public too</span></label>
  {{confirmSubmit "Make passwords.lab.example.com public? Anyone on the internet will be able to reach it." "Make public" ""}}
</form>`},
		}},
	{ID: "notices", Title: "Notices", Intro: "Short messages in a page. Info is the default; the colour says how much it matters.",
		Examples: []galleryExample{
			{"Info", "notice KIND TEXT, where KIND is info, ok, warn or error. Info: something's happening, or worth knowing.", `
{{notice "info" "Updating DNS records and checking them…"}}`},
			{"Success", "Something finished as asked.", `
{{notice "ok" "Password changed."}}`},
			{"Warning", "Should be dealt with soon. Options link= and linkText= add a link.", `
{{notice "warn" "Backups are only stored on this server." "link=/backups" "linkText=Improve backups"}}`},
			{"Error", "Something is broken.", `
{{notice "error" "The last backup didn't finish: the disk is full."}}`},
		}},
	{ID: "status", Title: "Status and badges", Intro: "Status says how a service is doing; badges label things. The highlight badge is for the brand's own voice (\"New\"), sparingly.",
		Examples: []galleryExample{
			{"Service status", "Pass the service's state.", `
{{status "running"}}
{{status "installing"}}
{{status "starting"}}
{{status "failed"}}
{{status "stopped"}}
{{status "waiting"}}`},
			{"Badges", "", `
{{badge "Preview"}}
{{badge "New" "highlight"}}`},
			{"Server", "server NAME: one of the lab's servers, named in running text (\"On nuc\"), so it's clear it means a server.", `
<p class="hint">On {{server "nuc"}} · backed up today 02:14</p>`},
			{"Icons", "icon NAME: a small line drawing before a label, to make a list quick to scan. It takes the text's colour and is hidden from screen readers, so the label must say it all. Names: server, services, changes, disk, location, id, certificate, proxy, store, roostack, network, manage, new-window, help (icons.go).", `
<p class="with-icon">{{icon "server"}}Role</p>
<p class="with-icon">{{icon "manage"}}Manage</p>
<p class="with-icon">{{icon "disk"}}Disk</p>
<p class="with-icon">{{icon "store"}}Backup store</p>
<p class="with-icon">{{icon "network"}}Network link</p>`},
			{"Opens in a new window", "new-window: ends every link with target=\"_blank\" rel=\"noopener\" (the help, the design system, services' addresses, other sites), so people know Roostack stays open behind it. An icon after the text, and \"(opens in a new window)\" for screen readers. A test checks every such link has it.", `
<p><a href="#" target="_blank" rel="noopener">Help{{template "new-window"}}</a> ·
<a href="#" target="_blank" rel="noopener">https://passwords.smith.family{{template "new-window"}}</a></p>`},
			{"Page help", "help PAGE TITLE: the help page about this one, in a new window, top right of a page-top row above the title (with the back link or setup step on the left). PAGE is its path in the help, optionally with a #section; a test checks each exists.", `
<div class="page-top">{{back "#" "Settings"}}{{help "settings/people" "People and access"}}</div>
<h1>People</h1>`},
		}},
	{ID: "cards", Title: "Cards and facts", Intro: "A card holds one topic. A heading row puts its title and main link together; facts list label and value pairs (stacked on phones).",
		Examples: []galleryExample{
			{"Card with heading and facts", "", `
<section class="card">
  <div class="heading">
    <h2>Domain and certificates</h2>
    <a href="#">Change…</a>
  </div>
  <dl class="facts">
    <dt>Domain</dt>       <dd>lab.example.com</dd>
    <dt>DNS provider</dt> <dd>Cloudflare</dd>
    <dt>Services</dt>     <dd>Addresses like <code>photos.lab.example.com</code></dd>
  </dl>
</section>`},
			{"Lined facts with icons", "facts-lined separates the rows with lines, for cards with many facts, several to a page (Settings → Servers).", `
<section class="card">
  <h2>nuc</h2>
  <dl class="facts facts-lined">
    <dt>{{icon "server"}}Role</dt>            <dd>Runs the lab</dd>
    <dt>{{icon "location"}}Where</dt>          <dd>At home</dd>
    <dt>{{icon "certificate"}}Certificate</dt> <dd>Expires 28 Sep 2027</dd>
  </dl>
</section>`},
			{"Back link and technical details", "Technical details stay collapsed for most people.", `
{{back "/services" "Services"}}
<details class="technical">
  <summary>Technical details</summary>
  <dl class="facts">
    <dt>Version</dt> <dd>15.0.9-roostack.1</dd>
  </dl>
</details>`},
			{"Project credit", "template \"project-credit\" with a projectCredit (from about: in a package): who makes the app, its licence (linked to its text) and its links, with its logo. Under the description where people choose to install it.", `
{{template "project-credit" .Credit}}`},
		}},
	{ID: "forms", Title: "Forms", Intro: "Labels wrap their inputs; hints sit underneath in small text. Errors appear above the form.",
		Examples: []galleryExample{
			{"Text field with hint and error", "", `
<form>
  <p class="error" role="alert">Choose a username of letters and digits.</p>
  <label>Username
    <input value="d@d" autocomplete="off">
    <small>Up to 32 letters, digits, dots or dashes.</small>
  </label>
</form>`},
			{"Choices", "", `
<form>
  <fieldset>
    <legend>What can they do?</legend>
    <label class="check">
      <input type="radio" name="role" checked>
      <span>Member <small>Sees the services and their links.</small></span>
    </label>
    <label class="check">
      <input type="radio" name="role">
      <span>Admin <small>Also installs and changes services.</small></span>
    </label>
  </fieldset>
  <label class="check">
    <input type="checkbox" checked>
    <span>Allow anyone to create an account</span>
  </label>
</form>`},
			{"Address and inline choice", "", `
<form>
  <label>Web address
    <span class="with-suffix">
      <input value="git">
      <span>.lab.example.com</span>
    </span>
  </label>
  <label class="check">
    <input type="radio" checked>
    <span>Restart automatically at <select class="inline"><option>04:00</option></select></span>
  </label>
</form>`},
		}},
	{ID: "lists", Title: "Lists", Intro: "Rows with borders for lists of things you can act on.",
		Examples: []galleryExample{
			{"Rows", "", `
<ul class="rows">
  <li class="row-split">
    <span><strong>mum</strong><br><small class="hint">Owner · two-factor on</small></span>
    <a href="#">Change…</a>
  </li>
  <li class="row-split">
    <span><strong>dad</strong><br><small class="hint">Member · two-factor off</small></span>
    <a href="#">Change…</a>
  </li>
</ul>`},
			{"Needs attention", "The home page's list of things to deal with. Items are problems, warnings, or (no class) information.", `
<div class="attention">
  <ul>
    <li class="item item-warning">
      <span>Backups are only stored on this server.</span>
      <a href="#">Improve backups</a>
    </li>
    <li class="item item-problem">
      <span>Forgejo didn't finish setting up.</span>
      <a href="#">See what went wrong</a>
    </li>
    <li class="item">
      <span>Finishing setting up Immich…</span>
      <a href="#">View</a>
    </li>
  </ul>
</div>`},
		}},
	{ID: "space", Title: "Disk space", Intro: "A disk's use as one bar of coloured parts, with a key that names each part and its size (Settings → Servers, Storage for others). The chart colours (--chart-1 to 3) are the same in every theme, so the parts stay apart for colour-blind readers; status colours only outline the bar when space runs low.",
		Examples: []galleryExample{
			{"Space bar and key", "Widths are data-width (percent), set by app.js: the Content-Security-Policy blocks style attributes.", `
<p><strong>55.0 GB free</strong> of 100.0 GB</p>
<div class="space-bar" role="img" aria-label="40.0 GB used, 60.0 GB free of 100.0 GB"><span class="space-services" data-width="12" title="Services' data: 12.0 GB"></span><span class="space-backups" data-width="3" title="Backups of your lab's servers: 3.0 GB"></span><span class="space-guests" data-width="1" title="Backups of other labs: 1.0 GB"></span><span class="space-other" data-width="24" title="The system and everything else: 24.0 GB"></span><span class="space-promised" data-width="9" title="Promised to other labs, not used yet: 9.0 GB"></span><span class="space-reserved" data-width="5" title="Reserved by the system: 5.0 GB"></span></div>
<dl class="space-list">
  <dt>In use</dt><dd>40.0 GB</dd>
  <dt class="sub"><span class="swatch space-services" aria-hidden="true"></span>Services' data</dt><dd>12.0 GB</dd>
  <dt class="sub"><span class="swatch space-backups" aria-hidden="true"></span>Backups of your lab's servers</dt><dd>3.0 GB</dd>
  <dt class="sub"><span class="swatch space-guests" aria-hidden="true"></span>Backups of other labs</dt><dd>1.0 GB</dd>
  <dt class="sub"><span class="swatch space-other" aria-hidden="true"></span>The system and everything else</dt><dd>24.0 GB</dd>
  <dt><span class="swatch space-promised" aria-hidden="true"></span>Promised to other labs</dt><dd>10.0 GB, of which 9.0 GB isn't used yet</dd>
  <dt><span class="swatch space-free" aria-hidden="true"></span>Free</dt><dd>55.0 GB</dd>
  <dt><span class="swatch space-reserved" aria-hidden="true"></span>Reserved by the system</dt><dd>5.0 GB, kept back by the filesystem so the system keeps working if the disk fills up</dd>
</dl>`},
		}},
	{ID: "tables", Title: "Tables", Intro: "For short lists of technical facts, such as DNS records. Prefer rows (above) for things people act on.",
		Examples: []galleryExample{
			{"Records", "", `
<table class="records">
  <thead><tr><th>Name</th><th>Type</th><th>Points to</th><th>Check</th></tr></thead>
  <tbody>
    <tr>
      <td><code>*.lab.example.com</code></td><td>A</td><td><code>192.168.1.20</code></td>
      <td><span class="dot dot-ok" aria-hidden="true"></span>Works from home</td>
    </tr>
  </tbody>
</table>`},
		}},
	{ID: "settings", Title: "Settings navigation", Intro: "Settings is a grid of areas, each showing its state in a line; area pages list every area in a sidebar on desktop.",
		Examples: []galleryExample{
			{"Area cards", "The dot says how things are; the text says what. Areas with nothing to judge have no dot.", `
<ul class="area-grid">
  <li>
    <a class="card area-card" href="#">
      <span class="area-title">Remote access</span>
      <span class="area-summary"><span class="dot dot-ok" aria-hidden="true"></span>SSH from your home network only</span>
    </a>
  </li>
  <li>
    <a class="card area-card" href="#">
      <span class="area-title">Home network</span>
      <span class="area-summary"><span class="dot dot-problem" aria-hidden="true"></span>Devices at home are sent somewhere else</span>
    </a>
  </li>
  <li>
    <a class="card area-card" href="#">
      <span class="area-title">Time zone</span>
      <span class="area-summary">Australia/Melbourne · 14:05 now</span>
    </a>
  </li>
</ul>`},
			{"Sidebar", "Hidden on phones, where the page's back link leads to the overview. Pages get it by naming their area (withArea in Go); the layout adds it.", `
{{template "area-nav" .Settings}}`},
		}},
	{ID: "activity", Title: "Activity", Intro: "Operations on the host (OS updates, backups, applying changes, restores): a status line everyone can follow, with the full log under Details. While running, the status line and log refresh themselves.",
		Examples: []galleryExample{
			{"Running", "The handler looks up the latest run of a kind, e.g. data[\"UpdatesActivity\"] = s.activityFor(r, activity.OSUpdates), and the page passes it to activity (which shows nothing if there's none). Steps come from the operation's own log (activity.Log.Step).", `
{{activity .UpdatesActivity}}`},
			{"Finished", "", `
{{activity .FinishedActivity}}`},
			{"Failed", "", `
{{activity .FailedActivity}}`},
			{"Install steps", "A new service's install (node.InstallProgress, in ServiceView.Install): each step done, in progress (with how long so far) or to do. The service page shows every step in a notice; the Services list and Home show one line (install-line).", `
<div class="notice" role="status">
  <p><strong>Installing Forgejo.</strong> This usually takes a few minutes.</p>
  {{template "install-steps" .Install}}
</div>
{{template "install-line" .InstallService}}`},
		}},
	{ID: "statistics", Title: "Statistics", Intro: "Counts over time, such as visitors through Roostack Cloud's relay. Heights and widths go through data-height and data-width (app.js), as style attributes are blocked.",
		Examples: []galleryExample{
			{"Figures", "dl.stat-tiles: a few totals, each a dt and dd, with an optional dd.hint.", `
<dl class="stat-tiles">
  <div><dt>Visitors</dt><dd>1,284</dd></div>
  <div><dt>Data</dt><dd>3.2 GB</dd><dd class="hint">2.9 GB to visitors</dd></div>
</dl>`},
			{"Bar chart", "template \"bar-chart\" with a barChart: a caption and a column a period, its label under it (may be empty) and the exact figure on hover and for screen readers.", `
{{template "bar-chart" .Chart}}`},
			{"Ranked list", "template \"rank-list\" with []rankView: names, a bar for each one's share of the largest, the count; the rest (Other) muted.", `
{{template "rank-list" .Ranks}}`},
			{"Choice of period", "nav.period-choice: links, the current one with aria-current.", `
<nav class="period-choice" aria-label="How far back"><a href="#">Today</a><a href="#" aria-current="true">7 days</a><a href="#">30 days</a></nav>`},
		}},
	{ID: "secrets", Title: "Secrets and codes", Intro: "Values to copy, such as a temporary password: shown once, easy to select.",
		Examples: []galleryExample{
			{"Secret", "", `
<code class="secret">EXAMPLE-ONLY-NOT-REAL</code>`},
			{"Copy box", "copyBox TEXT: something people copy and send or paste elsewhere (an invite, a command), with a Copy button.", `
{{copyBox "sudo roostack node join EXAMPLE-ONLY-NOT-REAL"}}`},
			{"Output", "Command output in logs and results.", `
<pre class="output">==> Installing updates
Unpacking curl (8.14.1-2+deb13u5) ...
Setting up curl (8.14.1-2+deb13u5) ...</pre>`},
		}},
}

// renderedExample is an example with its rendered HTML.
type renderedExample struct {
	galleryExample
	HTML template.HTML
	// Component is true for template components ({{template ...}}),
	// false for plain HTML using CSS classes.
	Component bool
	// Rendered is the HTML a template component produces, formatted.
	Rendered string
}

// renderGallery writes the gallery page. static is where the CSS and
// scripts are ("/static/" in the UI, a relative path for the file).
func renderGallery(w io.Writer, static string, standalone bool) error {
	base, err := template.New("design.html").Funcs(templateFuncs).ParseFS(templateFS,
		"templates/components.html", "templates/parts.html", "templates/design.html")
	if err != nil {
		return err
	}
	sample := gallerySamples()
	// The credit's logo is a file beside its package; here, Roostack's own.
	sample["Credit"] = &projectCredit{Title: "Forgejo", IconURL: static + "apple-touch-icon.png", About: pkgspec.About{
		Maker: "a community project under Codeberg e.V., a non-profit", Licence: "GPL-3.0-or-later",
		Website: "https://forgejo.org", Source: "https://codeberg.org/forgejo/forgejo", Support: "https://liberapay.com/forgejo"}}
	var sections []map[string]any
	for _, sec := range gallerySections {
		var examples []renderedExample
		for _, ex := range sec.Examples {
			t, err := base.Clone()
			if err != nil {
				return err
			}
			ex.Source = strings.TrimSpace(ex.Source)
			if t, err = t.New("example").Parse(ex.Source); err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := t.ExecuteTemplate(&buf, "example", sample); err != nil {
				return err
			}
			examples = append(examples, renderedExample{galleryExample: ex, HTML: template.HTML(buf.String()),
				Component: strings.Contains(ex.Source, "{{"), Rendered: formatHTML(buf.String())})
		}
		sections = append(sections, map[string]any{"ID": sec.ID, "Title": sec.Title, "Intro": sec.Intro, "Examples": examples})
	}
	return base.ExecuteTemplate(w, "design.html", map[string]any{
		"Static": static, "Standalone": standalone, "Tokens": galleryTokens, "Sections": sections,
		"Spacing": []int{1, 2, 3, 4, 5, 6, 7},
		"TypeScale": []struct{ Name, Sample string }{{"sm", "Hints and small print"}, {"base", "Body text"},
			{"lg", "Card headings"}, {"xl", "Large text"}, {"2xl", "Page titles"}},
	})
}

// gallerySamples is example data for the activity examples.
func gallerySamples() map[string]any {
	// Today at 09:00: shown as "today 09:00" whatever the date, so the
	// generated gallery file doesn't change from day to day. (Not 02:00:
	// on the day daylight saving starts, that hour doesn't exist.)
	now := time.Now()
	at := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.Local)
	view := func(status, err string, done bool, lines ...string) *activityView {
		s := activity.Snapshot{Title: "OS updates", Status: status, Started: at, Err: err, Lines: lines}
		if done {
			s.Finished = at.Add(8 * time.Second)
		}
		return &activityView{Kind: "gallery", Snapshot: s, Example: true}
	}
	install := &node.InstallProgress{Steps: []node.InstallStep{
		{Title: "Downloading the database (Postgres)", Started: at, Finished: at.Add(10 * time.Second)},
		{Title: "Starting the database (Postgres)", Started: at.Add(10 * time.Second), Finished: at.Add(22 * time.Second)},
		{Title: "Downloading Forgejo"},
		{Title: "Starting Forgejo"},
		{Title: "Finishing setup (create the administrator account)"},
	}}
	return map[string]any{
		"Install": install,
		"Chart": barChart{Title: "Visitors each day", Bars: bars([]int64{40, 55, 48, 70, 95, 62, 35},
			[]string{"Tue", "Wed", "Thu", "Fri", "Sat", "Sun", "Today"}, []string{"40 visitors", "55 visitors", "48 visitors", "70 visitors", "95 visitors", "62 visitors", "35 visitors"})},
		"Ranks":            topCountries(map[string]int64{"AU": 1130, "NZ": 96, "GB": 41}, 1284, 4),
		"InstallService":   ServiceView{ServiceDetail: node.ServiceDetail{Install: install}},
		"Settings":         areaNav{Label: "Settings", Home: "/settings", HomeTitle: "All settings", Groups: settingsGroups(), Active: "updates"},
		"UpdatesActivity":  view("Installing updates", "", false, "==> Installing updates", "Unpacking curl (8.14.1-2+deb13u5) ..."),
		"FinishedActivity": view("Installed 3 updates: curl, libcurl4t64, tzdata.", "", true, "==> Installing updates", "==> Done"),
		"FailedActivity":   view("Installing updates", "exit status 100", true, "E: Could not get lock /var/lib/dpkg/lock-frontend", "==> Failed: exit status 100"),
	}
}

// galleryPage serves the gallery in the UI.
func (s *Server) galleryPage(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if err := renderGallery(&buf, "/static/", false); err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}
