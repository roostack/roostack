package web

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"time"

	"github.com/mylab/mylab/internal/activity"
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
}

var gallerySections = []gallerySection{
	{ID: "buttons", Title: "Buttons", Intro: "One primary button per form or card; others secondary or quiet.",
		Examples: []galleryExample{
			{"Primary, secondary, danger, quiet", "Primary does the main thing. Danger removes or deletes. Quiet is for small or optional actions.",
				`<div class="actions-row"><button>Back up now</button><button class="secondary">Cancel</button><button class="danger">Remove</button><button class="quiet">Set this up later</button></div>`},
			{"Link styled as a button", "For navigation that looks like an action.",
				`<a class="button" href="#">Add a service</a> <a class="button secondary" href="#">Open backups</a>`},
			{"Confirm", "Asks the browser to confirm first. Use for anything hard to undo.",
				`{{template "confirm-button" (dict "Action" "#" "Confirm" "Remove dad? They will no longer be able to log in." "Text" "Remove dad" "Kind" "quiet danger")}}`},
		}},
	{ID: "notices", Title: "Notices", Intro: "Short messages in a page. Info is the default; the colour says how much it matters.",
		Examples: []galleryExample{
			{"Info", "Something's happening, or worth knowing.",
				`{{template "notice" (dict "Text" "Updating DNS records and checking them…")}}`},
			{"Success", "Something finished as asked.",
				`{{template "notice" (dict "Kind" "ok" "Text" "Password changed.")}}`},
			{"Warning", "Should be dealt with soon.",
				`{{template "notice" (dict "Kind" "warn" "Text" "Backups are only stored on this machine." "Link" "#" "LinkText" "Improve backups")}}`},
			{"Error", "Something is broken.",
				`{{template "notice" (dict "Kind" "error" "Text" "The last backup didn't finish: the disk is full.")}}`},
		}},
	{ID: "status", Title: "Status and badges", Intro: "Status says how a service is doing; badges label things. The highlight badge is for the brand's own voice (\"New\"), sparingly.",
		Examples: []galleryExample{
			{"Service status", "",
				`{{template "status" "running"}} {{template "status" "starting"}} {{template "status" "failed"}} {{template "status" "stopped"}}`},
			{"Badges", "",
				`{{template "badge" (dict "Text" "Preview")}} {{template "badge" (dict "Text" "New" "Highlight" true)}}`},
		}},
	{ID: "cards", Title: "Cards and facts", Intro: "A card holds one topic. A heading row puts its title and main link together; facts list label and value pairs (stacked on phones).",
		Examples: []galleryExample{
			{"Card with heading and facts", "",
				`<section class="card"><div class="heading"><h2>Domain and certificates</h2><a href="#">Change…</a></div>
<dl class="facts"><dt>Domain</dt><dd>lab.example.com</dd><dt>DNS provider</dt><dd>Cloudflare</dd><dt>Services</dt><dd>Addresses like <code>photos.lab.example.com</code></dd></dl></section>`},
			{"Back link and technical details", "Technical details stay collapsed for most people.",
				`{{template "back" (dict "Href" "#" "Text" "Services")}}
<details class="technical"><summary>Technical details</summary><dl class="facts"><dt>Version</dt><dd>15.0.9-mylab.1</dd></dl></details>`},
		}},
	{ID: "forms", Title: "Forms", Intro: "Labels wrap their inputs; hints sit underneath in small text. Errors appear above the form.",
		Examples: []galleryExample{
			{"Text field with hint and error", "",
				`<form><p class="error" role="alert">Choose a username of letters and digits.</p>
<label>Username <input value="d@d" autocomplete="off"><small>Up to 32 letters, digits, dots or dashes.</small></label></form>`},
			{"Choices", "",
				`<form><fieldset><legend>What can they do?</legend>
<label class="check"><input type="radio" name="r" checked><span>Member<small>Sees the services and their links.</small></span></label>
<label class="check"><input type="radio" name="r"><span>Admin<small>Also installs and changes services.</small></span></label></fieldset>
<label class="check"><input type="checkbox" checked><span>Allow anyone to create an account</span></label></form>`},
			{"Address and inline choice", "",
				`<form><label>Web address <span class="with-suffix"><input value="git"><span>.lab.example.com</span></span></label>
<label class="check"><input type="radio" checked><span>Restart automatically at <select class="inline"><option>04:00</option></select></span></label></form>`},
		}},
	{ID: "lists", Title: "Lists", Intro: "Rows with borders for lists of things you can act on.",
		Examples: []galleryExample{
			{"Rows", "",
				`<ul class="rows"><li class="row-split"><span><strong>mum</strong><br><small class="hint">Owner · two-factor on</small></span><a href="#">Change…</a></li>
<li class="row-split"><span><strong>dad</strong><br><small class="hint">Member · two-factor off</small></span><a href="#">Change…</a></li></ul>`},
			{"Needs attention", "The home page's list of things to deal with.",
				`<div class="attention"><ul><li class="item item-warning"><span>Backups are only stored on this machine.</span><a href="#">Improve backups</a></li>
<li class="item item-problem"><span>Forgejo didn't finish setting up.</span><a href="#">See what went wrong</a></li>
<li class="item"><span>Finishing setting up Immich…</span><a href="#">View</a></li></ul></div>`},
		}},
	{ID: "settings", Title: "Settings navigation", Intro: "Settings is a grid of areas, each showing its state in a line; area pages list every area in a sidebar on desktop.",
		Examples: []galleryExample{
			{"Area cards", "The dot says how things are; the text says what. Areas with nothing to judge have no dot.",
				`<ul class="area-grid"><li><a class="card area-card" href="#"><span class="area-title">Remote access</span><span class="area-summary"><span class="dot dot-ok" aria-hidden="true"></span>SSH from your home network only</span></a></li>
<li><a class="card area-card" href="#"><span class="area-title">Home network</span><span class="area-summary"><span class="dot dot-problem" aria-hidden="true"></span>Devices at home are sent somewhere else</span></a></li>
<li><a class="card area-card" href="#"><span class="area-title">Time zone</span><span class="area-summary">Australia/Melbourne · 14:05 now</span></a></li></ul>`},
			{"Sidebar", "Hidden on phones, where the page's back link leads to the overview.",
				`{{template "settings-nav" .Settings}}`},
		}},
	{ID: "activity", Title: "Activity", Intro: "Operations on the host: a status line everyone can follow, with the full log under Details.",
		Examples: []galleryExample{
			{"Running", "", `{{template "activity" (dict "Activity" .Running)}}`},
			{"Finished", "", `{{template "activity" (dict "Activity" .Finished)}}`},
			{"Failed", "", `{{template "activity" (dict "Activity" .Failed)}}`},
		}},
	{ID: "secrets", Title: "Secrets and codes", Intro: "Values to copy, such as a temporary password: shown once, easy to select.",
		Examples: []galleryExample{
			{"Secret", "", `<code class="secret">EXAMPLE-ONLY-NOT-REAL</code>`},
			{"Output", "Command output in logs and results.", `<pre class="output">==> Installing updates
Unpacking curl (8.14.1-2+deb13u5) ...
Setting up curl (8.14.1-2+deb13u5) ...</pre>`},
		}},
}

// renderedExample is an example with its rendered HTML.
type renderedExample struct {
	galleryExample
	HTML template.HTML
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
	var sections []map[string]any
	for _, sec := range gallerySections {
		var examples []renderedExample
		for _, ex := range sec.Examples {
			t, err := base.Clone()
			if err != nil {
				return err
			}
			if t, err = t.New("example").Parse(ex.Source); err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := t.ExecuteTemplate(&buf, "example", sample); err != nil {
				return err
			}
			examples = append(examples, renderedExample{ex, template.HTML(buf.String())})
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
	at := time.Date(2026, 9, 27, 2, 0, 0, 0, time.Local)
	view := func(status, err string, done bool, lines ...string) *activityView {
		s := activity.Snapshot{Title: "OS updates", Status: status, Started: at, Err: err, Lines: lines}
		if done {
			s.Finished = at.Add(8 * time.Second)
		}
		return &activityView{Kind: "gallery", Snapshot: s, Example: true}
	}
	return map[string]any{
		"Settings": map[string]any{"SettingsGroups": settingsGroups(), "SettingsArea": "updates"},
		"Running":  view("Installing updates", "", false, "==> Installing updates", "Unpacking curl (8.14.1-2+deb13u5) ..."),
		"Finished": view("Installed 3 updates: curl, libcurl4t64, tzdata.", "", true, "==> Installing updates", "==> Done"),
		"Failed":   view("Installing updates", "exit status 100", true, "E: Could not get lock /var/lib/dpkg/lock-frontend", "==> Failed: exit status 100"),
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
