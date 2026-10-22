package web

import (
	"bytes"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/monitor"
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
	{ID: "layout", Title: "Layout", Intro: "Console glass (2026-10-14): a sidebar floating beside the page, the page's header on the page, cards on a faint moving aurora (it stops for reduced motion). The layout (templates/layout.html) draws all of it; a page gives its title, content and, if it wants, its breadcrumbs and help. Below 900px the sidebar is a drawer behind the menu button.",
		Examples: []galleryExample{
			{"Sidebar", "The lab's name, the main areas (Settings last, for those who can change settings), then Help, Light/Dark/Auto and the person. A main area's own pages hang under it while you're there (.nav-children of .nav-sub, each with a 16 px icon): under Services each service (its logo, .nav-logo, or its drawn icon) and Add a service; under Settings every settings page (settingsArea.Icon). Beside Servers, how many need attention (.nav-count, a warning pill). Where you are has aria-current=\"page\" (its main area \"true\"): the highlight tint with a faint highlight edge. The surface at 70% with a blur: status shows here only as a pill on its own tint, never as bare text (the contrast test checks text, muted text and links on it).", `
<aside class="sidebar gallery-sidebar" aria-label="Roostack (example)">
  <a class="sidebar-lab" href="#">{{brandMark}}<span class="sidebar-lab-name"><span>Roostack</span><strong>The Smiths' lab</strong></span></a>
  <nav class="nav-group" aria-label="Your lab (example)">
    <a class="nav-item" href="#">{{icon "home-page"}}Home</a>
    <a class="nav-item" href="#" aria-current="true">{{icon "services"}}Services</a>
    <div class="nav-children"><a class="nav-sub" href="#" aria-current="page">{{icon "photos"}}Immich</a><a class="nav-sub" href="#">{{icon "passwords"}}Vaultwarden</a><a class="nav-sub" href="#">{{icon "plus"}}Add a service…</a></div>
    <a class="nav-item" href="#">{{icon "backups"}}Backups</a>
    <a class="nav-item" href="#">{{icon "server"}}Servers<span class="nav-count" title="1 needs attention">1</span></a>
    <a class="nav-item" href="#">{{icon "manage"}}Settings</a>
  </nav>
  <div class="sidebar-foot">
    <div class="sidebar-tools"><a href="#">{{icon "help"}}Help</a><button type="button" class="theme-toggle">{{icon "dark"}}Dark</button></div>
    <div class="who"><a href="#" class="who-link"><span class="avatar" aria-hidden="true">T</span><span class="who-name"><strong>tom</strong><span>Owner</span></span></a>
      <button type="button" class="icon-button" title="Log out">{{icon "log-out"}}<span class="sr-only">Log out</span></button></div>
  </div>
</aside>`},
			{"Page header", "Breadcrumbs (each step a link, the page itself last, in bold) and the page's help. By default: the main area, then the page's title; a page can give its own as {{\"{{\"}}define \"crumbs\"{{\"}}\"}}. On phones the menu button comes first.", `
<header class="page-header gallery-page-header">
  <nav class="crumbs" aria-label="Where you are (example)"><a href="#">Services</a><a href="#">Immich</a><strong aria-current="page">Move</strong></nav>
  {{help "servers/move" "Move a service"}}
</header>`},
		}},
	{ID: "page-parts", Title: "Page parts", Intro: "The parts of a Console glass page: its heading, a 12-column grid of cards, figures, tables for lists people scan, and the technical view of a service.",
		Examples: []galleryExample{
			{"Page heading", ".page-head: the title, a .lede under it, and .page-head-actions on the right (under it on phones). .gradient-text at most once a page.", `
<div class="page-head">
  <div><h1>Good evening. <span class="gradient-text">Everything's running.</span></h1>
    <p class="lede">3 services on 1 server. Backed up last night at 02:00.</p></div>
  <div class="page-head-actions"><a class="button" href="#">Add a service</a></div>
</div>`},
			{"Grid and card heading", ".grid with .span-3 to .span-8 children (the rest span all 12); one column below 900px. .card-head: a card's title and a .more link. .section-heading between groups of cards.", `
<div class="grid">
  <section class="card span-8"><div class="card-head"><h2>Services</h2><a class="more" href="#">All services</a></div><p>Eight columns.</p></section>
  <section class="card span-4"><div class="card-head"><h2>Backups</h2></div><p>Four.</p></section>
</div>
<h2 class="section-heading">Your whole lab</h2>`},
			{"Figures strip", ".figures: a few cards, each a .figure-label, a .figure-value (a unit in small) and a .figure-note (.warn or .bad when it needs attention). Two to a row on phones.", `
<ul class="figures">
  <li class="card"><span class="figure-label">Services</span><span class="figure-value">6 <small>running</small></span><span class="figure-note">1 updating</span></li>
  <li class="card"><span class="figure-label">Servers</span><span class="figure-value">3 <small>online</small></span><span class="figure-note warn">1 needs a restart</span></li>
  <li class="card"><span class="figure-label">Last backup</span><span class="figure-value">02:00</span><span class="figure-note">Every server</span></li>
  <li class="card"><span class="figure-label">Disk space</span><span class="figure-value">1.6 TB <small>of 2.6 TB used</small></span><span class="figure-note">Most on nuc</span></li>
</ul>`},
			{"Big figure", ".big-figure: the one number a card is about (the last backup's time; whenParts splits a time into its day and clock time).", `
<span class="big-figure">02:00<small>today</small></span>`},
			{"Page heading for one thing", ".page-head-title: its logo beside the title, a .title-line with its status beside the h1 (a service's page).", `
<div class="page-head-title">{{logo "" "photos"}}<div><div class="title-line"><h1>Immich</h1>{{status "running"}}</div><p class="lede">2.1.0 · Photo and video backup</p></div></div>`},
			{"Meter", ".meter: how full something is, a bar its width from data-width (app.js); .meter-warning or .meter-problem when it runs low. With the figure in small text under it (a server's disks in a table).", `
<div class="meter"><span data-width="35"></span></div>
<div class="meter meter-warning"><span data-width="92"></span></div>`},
			{"Recent activity", "ul.activity-list: a dot (dot-ok, dot-problem), what happened with where in small text, and when.", `
<ul class="activity-list">
  <li><span class="dot dot-ok" aria-hidden="true"></span><span class="activity-what"><a href="#">Backup</a><small>nuc · 1 new file, 19.2 MB added</small></span><time>today 02:00</time></li>
  <li><span class="dot dot-problem" aria-hidden="true"></span><span class="activity-what"><a href="#">OS updates</a><small>vps · a package didn't install</small></span><time>today 01:00</time></li>
</ul>`},
			{"Table list", "table.list in a .table-wrap, as a card's last part (it reaches the card's edges). .list-name: a logo, the name and a line under it. Links are quiet (underlined on hover); the row's action is a .table-action. .hide-phone on columns that matter less; .num for figures; the last column is the row's action.", `
<section class="card">
  <div class="card-head"><h2>Services</h2></div>
  <div class="table-wrap"><table class="list">
    <thead><tr><th>Service</th><th>Status</th><th class="hide-phone">Runs on</th><th class="hide-phone">Backed up</th><th><span class="sr-only">Actions</span></th></tr></thead>
    <tbody>
      <tr><td><div class="list-name">{{logo "" "photos"}}<div><a href="#">Immich</a><small>immich</small></div></div></td><td>{{status "running"}}</td><td class="hide-phone">nuc</td><td class="hide-phone num">02:00</td><td><a class="table-action" href="#">Manage</a></td></tr>
      <tr><td><div class="list-name">{{logo "" "passwords"}}<div><a href="#">Vaultwarden</a><small>vaultwarden</small></div></div></td><td>{{status "installing"}}</td><td class="hide-phone">nuc</td><td class="hide-phone num">Not yet</td><td><a class="table-action" href="#">Manage</a></td></tr>
    </tbody>
  </table></div>
</section>`},
			{"Friendly/Technical switch", ".view-switch in a .views part: a checkbox that flips its .friendly-only and .technical-only children (CSS only). Without :has, the friendly view shows.", `
<div class="views">
  <label class="view-switch"><input type="checkbox"><span>Friendly</span><span>Technical</span><span class="sr-only"> view</span></label>
  <p class="friendly-only">Immich keeps your photos, at photos.smith.family.</p>
  <p class="technical-only">It runs as roostack-immich-server, from a Quadlet file.</p>
</div>`},
			{"Code panel", ".code-panel: a .code-bar saying where the file lives (or the command), then the file. .code-comment and .code-section colour a little of it.", `
<figure class="code-panel">
  <figcaption class="code-bar">{{icon "documents"}}~/.config/containers/systemd/roostack-immich-server.container</figcaption>
<pre><code><span class="code-comment"># Generated by Roostack. Do not edit: changes will be overwritten.</span>
<span class="code-section">[Container]</span>
ContainerName=roostack-immich-server
Network=roostack-immich.network</code></pre>
</figure>`},
		}},
	{ID: "brand", Title: "Brand mark", Intro: "Roostack's mark, a hen on a server, drawn in the theme's colours: text colour on a soft-highlight tile, with a highlight comb. It's generated from assets/brand/generate.py; don't edit it by hand.",
		Examples: []galleryExample{
			{"Without a login", "brandMark inside .brand, before the name (sign-in, first-run setup). Decorative (hidden from screen readers): the name says what it is.", `
<a class="brand" href="#">{{brandMark}}Roostack</a>`},
			{"In the sidebar", "At the top of the sidebar: a small \"Roostack\" above the lab's name (\"Your lab\" until it has one).", `
<a class="sidebar-lab" href="#">{{brandMark}}<span class="sidebar-lab-name"><span>Roostack</span><strong>The Smiths' lab</strong></span></a>`},
		}},
	{ID: "error-scenes", Title: "Error scenes", Intro: "Pictures for error pages: the mark's hen and server, without the tile, with props and a joke. A joke headline goes with each, and plain words underneath. Generated from assets/errors/generate.py; Caddy's pages for the services use them too (internal/errpage).",
		Examples: []galleryExample{
			{"Roostack's pages", "errorScene NAME ID, in .es-scene: 404 flown, 403 pecking, 500 scrambled. ID makes the scene's SVG ID unique on the page.", `
<div class="error-scenes">{{errorScene "flown" "g1"}}{{errorScene "pecking" "g2"}}{{errorScene "scrambled" "g3"}}</div>`},
			{"Services' pages (Caddy)", "404 vacant, 403 shooed (refused by CrowdSec: a banned address, or a request that looked like an attack), 429 crowded, 502 asleep, 503 hatching, 504 slow.", `
<div class="error-scenes">{{errorScene "vacant" "g4"}}{{errorScene "shooed" "g12"}}{{errorScene "crowded" "g5"}}{{errorScene "asleep" "g6"}}{{errorScene "hatching" "g7"}}{{errorScene "slow" "g8"}}</div>`},
			{"Roostack Cloud", "The hen on a cloud, for Cloud's own pages (copied from assets/errors).", `
<div class="error-scenes">{{errorScene "cloud-asleep" "g9"}}{{errorScene "cloud-empty" "g10"}}{{errorScene "cloud-unplugged" "g11"}}</div>`},
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
	{ID: "notices", Title: "Notices", Intro: "Short messages in a page: a tinted card with its kind's icon (drawn by CSS, so every .notice has one) and a faint edge in its colour. Info is the default; the colour says how much it matters.",
		Examples: []galleryExample{
			{"Info", "notice KIND TEXT, where KIND is info, ok, warn or error. Info: something's happening, or worth knowing.", `
{{notice "info" "Updating DNS records and checking them…"}}`},
			{"Success", "Something finished as asked.", `
{{notice "ok" "Password changed."}}`},
			{"Warning", "Should be dealt with soon. Options link= and linkText= add a link.", `
{{notice "warn" "Backups are only stored on this server." "link=/backups" "linkText=Improve backups"}}`},
			{"Error", "Something is broken.", `
{{notice "error" "The last backup didn't finish: the disk is full."}}`},
			{"With a list", "noticeList KIND TEXT ITEMS: a notice whose text leads into a list (each item plain text, which may be long and technical: it wraps anywhere). For several things to read before deciding, such as what a package of your own asks for.", `
{{noticeList "warn" "It asks for things packages from the service registry can't have:" .PolicyProblems}}`},
		}},
	{ID: "status", Title: "Status and badges", Intro: "Status says how a service is doing; badges label things. The highlight badge is for the brand's own voice (\"New\"), sparingly.",
		Examples: []galleryExample{
			{"Service status", "Pass the service's state. A pill: a dot and a word on the status tint (solid, so it reads on glass); work in progress pulses its dot.", `
{{status "running"}}
{{status "installing"}}
{{status "updating"}}
{{status "starting"}}
{{status "failed"}}
{{status "stopped"}}
{{status "waiting"}}
{{status "registry-stop"}}
{{status "awaiting-data"}}
{{status "held"}}
{{status "switching"}}`},
			{"Badges", "", `
{{badge "Preview"}}
{{badge "New" "highlight"}}
{{badge "Installed" "ok"}}
{{badge "Community" "warn"}}
{{badge "Your own" "warn"}}`},
			{"Tier badge", "template \"tier-badge\" with anything that has .Local and .Tier (a catalog entry, an installed service): who looks after its package. Beside the title, inside the heading.", `
<h2>Forgejo {{template "tier-badge" .TierOfficial}}</h2>
<h2>Paperless-ngx {{template "tier-badge" .TierCommunity}}</h2>
<h2>My app {{template "tier-badge" .TierLocal}}</h2>`},
			{"Logo", "logo SRC ICON: a service's logo on a light tile, the same in both modes, since projects draw their logos for light backgrounds; without one (SRC \"\"), the drawn ICON on the same tile. Decorative: the name beside it says what it is.", `
{{logo .LogoURL ""}}
{{logo "" "photos"}}
{{logo "" "development"}}`},
			{"Server", "server NAME: one of the lab's servers, named in running text (\"On nuc\"), so it's clear it means a server.", `
<p class="hint">On {{server "nuc"}} · backed up today 02:14</p>`},
			{"Icons", "icon NAME: a small line drawing before a label, to make a list quick to scan. It takes the text's colour and is hidden from screen readers, so the label must say it all. Names: server, services, changes, disk, location, id, certificate, proxy, store, roostack, network, manage, new-window, help, and the catalog's categories (photos, media, documents, productivity, home, passwords, development, publishing; icons.go).", `
<p class="with-icon">{{icon "server"}}Role</p>
<p class="with-icon">{{icon "manage"}}Manage</p>
<p class="with-icon">{{icon "disk"}}Disk</p>
<p class="with-icon">{{icon "store"}}Backup store</p>
<p class="with-icon">{{icon "network"}}Network link</p>`},
			{"Opens in a new window", "new-window: ends every link with target=\"_blank\" rel=\"noopener\" (the help, the design system, services' addresses, other sites), so people know Roostack stays open behind it. An icon after the text, and \"(opens in a new window)\" for screen readers. A test checks every such link has it.", `
<p><a href="#" target="_blank" rel="noopener">Help{{template "new-window"}}</a> ·
<a href="#" target="_blank" rel="noopener">https://passwords.smith.family{{template "new-window"}}</a></p>`},
			{"Page help", "help PAGE TITLE: the help page about this one, in a new window, at the right of the page header. A page gives it as {{\"{{\"}}define \"page-help\"{{\"}}\"}}. PAGE is its path in the help, optionally with a #section; a test checks each exists.", `
{{help "settings/people" "People and access"}}`},
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
			{"Catalog entry", "li.card.offer in ul.services: a logo, the name with its badges, the description and categories (.offer-categories), and the action (.offer-action); .offer-later in place of the button when it can't be installed. On phones the action moves under the text.", `
<ul class="services">
  <li class="card offer">{{logo .LogoURL ""}}<div class="offer-text"><h2>Forgejo</h2>{{badge "Official"}}<p class="hint">Hosts your own Git repositories.</p><p class="offer-categories">Developer tools</p></div>
    <div class="offer-action"><a class="button" href="#">Install</a></div></li>
  <li class="card offer">{{logo "" "documents"}}<div class="offer-text"><h2>Paperless-ngx</h2>{{badge "Community" "warn"}}<p class="hint">Scans, reads and files your paper documents.</p><p class="offer-categories">Documents and notes</p></div>
    <div class="offer-action"><p class="offer-later">Needs a newer Roostack: 0.9 or newer. <a href="#">Details</a></p></div></li>
  <li class="card offer">{{logo "" "services"}}<div class="offer-text"><h2>My app</h2>{{badge "Your own" "warn"}}<p class="hint">A package you added yourself.</p></div>
    <div class="offer-action">{{confirm "#" "Remove My app from the list?" "Remove" "quiet danger"}}<a class="button" href="#">Install</a></div></li>
</ul>`},
			{"Filter chips", "div.filter-chips of buttons, the chosen one aria-pressed=\"true\": narrows a list to one group (the catalog's categories).", `
<div class="filter-chips" role="group" aria-label="Categories"><button type="button" aria-pressed="true">All</button><button type="button" aria-pressed="false">Photos and videos</button><button type="button" aria-pressed="false">Developer tools</button></div>`},
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
	{ID: "choosing", Title: "Choosing", Intro: "Controls for choices that a plain list of inputs makes long: a small exclusive choice as a row of buttons, related fields side by side, and a folder chosen in a dialog.",
		Examples: []galleryExample{
			{"Segmented control", "segmented NAME LABEL SELECTED (choice VALUE TEXT [ICON])… [\"size=small\"]: two to four exclusive choices as radios drawn as one row of buttons; on phones a full-size one fills the width, the icon above the words. Inside a .seg-group, the chosen choice's .seg-panel (data-seg, counting from 1) shows, by CSS alone, so it works without scripts; a .seg-hint first in a panel says what that choice is. For choices that each bring their own fields (where a media library is, SMB or NFS); a choice whose consequences need a sentence each stays radios with notes.", `
<form>
  <div class="seg-group">
    {{segmented "where" "Where it is" "drive" (choice "drive" "Drive" "disk") (choice "folder" "Folder on nuc" "server") (choice "share" "Network share" "network")}}
    <p class="seg-panel hint seg-hint" data-seg="1">A USB drive or second disk. Roostack mounts it read-only: nothing on it changes.</p>
    <p class="seg-panel hint seg-hint" data-seg="2">A folder already on nuc, under /mnt, /media or /srv.</p>
    <p class="seg-panel hint seg-hint" data-seg="3">A folder shared by a NAS or another computer.</p>
  </div>
  {{segmented "share_type" "Kind of share" "smb" (choice "smb" "SMB") (choice "nfs" "NFS") "size=small"}}
</form>`},
			{"Fields side by side", ".field-row: two fields to a row (each at least 14rem), one under another when there isn't room; .field-row.wide-first for a field and a short one (a server and its port). input.name-input for a name, rather than the card's whole width.", `
<form>
  <div class="field-row wide-first">
    <label>SMTP server <input value="smtp.example.com"></label>
    <label>Port <input type="number" value="587"></label>
  </div>
  <div class="field-row">
    <label>Username <input value="sam"><small>Empty for a share anyone on your network can open.</small></label>
    <label>Password <input type="password"><small>Kept on nuc, readable only by its system.</small></label>
  </div>
  <label>Name <input class="name-input" value="Films"></label>
</form>`},
			{"Form in a dialog", "Adding one thing to a list (a check, someone, an invite, a server): a secondary button, a link with data-dialog=\"ID\" and an href that opens the page with ?add=…, opens dialog.form-dialog ID: .dialog-head (the heading and a close link), the form, .dialog-actions (a data-close Cancel link, then its button). The page renders it open when its form came back with a mistake, or from ?add=…; app.js makes an open one modal, and without scripts it shows in the page as a card. The first field has autofocus. A form that needs its own page's work (the drives a server has) is loaded as it opens: the link also has data-load=\"SELECTOR\" and the dialog is template \"add-dialog\" (dialogFor ID TITLE BACK), an empty .dialog-body that app.js fills with SELECTOR from the link's page; the form sends itself with htmx (a mistake comes back in the dialog, success is HX-Redirect). Here, open in the page as without scripts.", `
<dialog class="dialog form-dialog" open aria-labelledby="gallery-dialog-title">
  <div class="dialog-head"><h2 id="gallery-dialog-title">Add a check</h2><a class="icon-button" href="#" data-close>{{icon "close"}}<span class="sr-only">Close</span></a></div>
  <form>
    <div class="field-row"><label>Name <input value="Printer"></label><label>Address <input value="printer.local:631"></label></div>
    <label class="check"><input type="checkbox" class="switch" role="switch" checked><span>Tell me when it stops answering</span></label>
    <div class="dialog-actions"><a class="button secondary" href="#" data-close>Cancel</a><button type="button">Add check</button></div>
  </form>
</dialog>`},
			{"Form sections and Details", "h2.form-section: a small heading inside a long form (the install form's Name and address, Where it runs, its settings), with hr.form-rule between sections. details.form-more: a quiet Details line under the fields, for one rarely changed (a service's name for folders and backups).", `
<form>
  <h2 class="form-section">Name and address</h2>
  <div class="field-row"><label>Title <input value="Forgejo"></label><label>Web address <span class="with-suffix"><input value="git"><span>.lab.example.com</span></span></label></div>
  <details class="form-more"><summary>Its name for folders and backups: <code>forgejo</code></summary>
    <label>Name <span class="with-prefix"><span>forgejo-</span><input value=""></span></label></details>
  <hr class="form-rule">
  <h2 class="form-section">Forgejo's settings</h2>
  <label class="check"><input type="checkbox" class="switch" role="switch"><span>Allow anyone to create an account</span></label>
</form>`},
			{"Input with its button", "form.inline-form: an input then its button (Rename, Save), the button as tall as the input and on its line; a dropdown in a sentence (select.inline) before a button is as tall as an input.", `
<form class="inline-form"><label class="sr-only" for="gallery-rename">Title</label><input id="gallery-rename" value="Forgejo"><button type="button" class="secondary">Rename</button></form>
<form class="inline-form"><label>Keep history for <select class="inline"><option>90 days</option></select></label><button type="button" class="secondary">Save</button></form>`},
			{"Switch", "label.check with input.switch role=\"switch\": an on/off setting that saves with its form (still a checkbox without styles). Ticking items from a list, or confirming something (\"I understand\"), stays a checkbox.", `
<form>
  <label class="check"><input type="checkbox" class="switch" role="switch" checked><span>Offer "Keep me signed in" on the login page<small>For someone's own device.</small></span></label>
  <label class="check"><input type="checkbox" class="switch" role="switch"><span>Allow anyone to create an account</span></label>
</form>`},
			{"Folder picker", "The folder chosen as a value (.folder-choice: the folder icon, the place and what it holds, then a secondary button), which opens the picker: template \"folder-picker\" with a folderPicker, the folders opened so far as crumbs, the folders here (each opening on a click, with what it holds), what this one holds, Cancel and Use. A link with data-picker=\"ID\" shows it in pickerDialog ID PLACE (app.js loads the link's page into the dialog, and Use replaces the page's PLACE); without scripts the same link is a page.", `
<div class="folder-choice">{{icon "folder"}}<span class="path"><strong>MEDIA › Films</strong><small>3 folders, 1 file</small></span><a class="button secondary" href="#">Choose a folder…</a></div>
<div class="gallery-picker">{{template "folder-picker" .Picker}}</div>`},
		}},
	{ID: "steps", Title: "Steps and checks", Intro: "For work done in steps on one page (bringing a service's data in, SPEC.md §8.4; the Owner's choice of mock-ups, docs/mockups/bring-your-own-data): numbered step cards, a check's results, a path and where it is now, a long copy's progress, and what to do elsewhere.",
		Examples: []galleryExample{
			{"Steps of a page", "section.card.step-card with an h2 holding span.step-num (the step's number): the step open now. .step-done: a step done, its number a tick, one line (p.step-summary, a small line under it) and a Change link (.more). .step-todo: a step not reached yet, only its heading, muted. .step-result: the last step, done, holding the result. Each step ends with Continue, unless its button acts within it. In .steps-layout, the steps (.steps) go beside .steps-aside and .steps-help, the aside first below 1100 px.", `
<section class="card step-card step-done">
  <div class="card-head"><h2><span class="step-num">{{icon "check"}}</span>Your files</h2><a class="more" href="#">Change</a></div>
  <p class="step-summary">T7 Shield › immich<small>Read-only</small></p>
</section>
<section class="card step-card">
  <div class="card-head"><h2><span class="step-num">2</span>Your old setup <small class="hint">optional</small></h2></div>
  <p class="hint">What it ran as before tells Roostack which of your folders is which.</p>
  <div class="actions-row"><button type="button">Continue</button></div>
</section>
<section class="card step-card step-todo"><div class="card-head"><h2><span class="step-num">3</span>What goes where</h2></div></section>`},
			{"A check's results", "ul.check-list of li.check-ok, .check-warn, .check-bad (in the way: nothing goes on until it's fixed) or .check-info, each an icon (check, warn, close, spark) and a div: its first sentence in bold, then more, sometimes a copyBox or a ul.code-list.", `
<ul class="check-list">
  <li class="check-ok">{{icon "check"}}<div><strong>Your photos and videos: 118,402 files, 41.2 GB.</strong> All readable.</div></li>
  <li class="check-warn">{{icon "warn"}}<div><strong>An older version.</strong> The old one ran 1.131.3; this one is 3.3.3.</div></li>
  <li class="check-bad">{{icon "close"}}<div><strong>312 files Roostack can't read, in library:</strong> make them readable on your copy, then check again:
    {{copyBox "sudo chmod -R a+rX immich"}}</div></li>
  <li class="check-info">{{icon "spark"}}<div><strong>Smart search models: not brought.</strong> Immich makes them again.</div></li>
</ul>`},
			{"A path and where it is now", ".path-row: a label and an input (the path), an arrow, a .field-label and a .folder-choice (where it is now), the two lined up on one row; one under another on phones.", `
<form>
  <div class="path-row">
    <label class="path-old-label" for="g-old">On the old machine</label>
    <span class="field-label path-now-label">Is now</span>
    <input class="path-old" id="g-old" value="/opt/immich">
    <span class="path-arrow" aria-hidden="true">→</span>
    <div class="folder-choice path-now">{{icon "folder"}}<span class="path"><strong>T7 Shield › immich</strong></span><a class="button secondary small" href="#">Choose…</a></div>
  </div>
</form>`},
			{"Pasted text", "A textarea: monospace, as tall as it's given (rows), for text pasted from elsewhere (docker inspect's output, a Compose file).", `
<form><label>What it printed <textarea rows="3" spellcheck="false">[ { "Name": "/immich_server", … } ]</textarea></label></form>`},
			{"Progress of a long copy", ".progress: a .meter at full width (its span's width through data-width) and the figures under it, in an activity status line's card.", `
<p class="activity-status running"><span class="spinner" aria-hidden="true"></span> Copying: Your photos and videos… <small>started 14:10</small></p>
<div class="progress"><div class="meter"><span data-width="37"></span></div><p>18.4 GB of 49.6 GB · 52,310 of 355,516 files · <strong>about 25 minutes left</strong></p></div>`},
			{"A call to action", "section.card.cta at the top of a service's page, under its heading: .cta-icon, .cta-text (an h2 and a line, or a .progress) and .cta-actions. Slim, not a card of its own design (the Owner, 2026-10-21: \"weird for one service to have a very large and completely different UI right at the top\"). .cta-warn when something stopped. Migrating photos into Immich: before anyone has, while one runs, when one stopped.", `
<section class="card cta">
  <span class="cta-icon">{{icon "import"}}</span>
  <div class="cta-text"><h2>Moving from Google Photos or iCloud?</h2>
    <p>Migrate your photos and videos into Immich, with their dates, places and albums. Roostack walks you through it.</p></div>
  <div class="cta-actions"><a class="button" href="#">Migrate photos…</a><button type="button" class="quiet">Hide</button></div>
</section>
<section class="card cta cta-warn">
  <span class="cta-icon">{{icon "warn"}}</span>
  <div class="cta-text"><h2>Anna's migration stopped</h2><p>When the server restarted. 16,210 are in Immich; carrying on skips them.</p></div>
  <div class="cta-actions"><button type="button">Carry on</button></div>
</section>`},
			{"An empty list's note", "p.locations-empty: a dashed box where a list will be, saying there's nothing yet and how to add one (the Owner's words, 2026-10-21). .locations holds the list once there is one: folder choices with Remove.", `
<p class="locations-empty">You haven't defined any additional locations that people can migrate photos from. Press <strong>Add a location</strong> below to add one.</p>
<div class="locations"><div class="folder-choice location">{{icon "network"}}<span class="path"><strong>Family NAS › Takeouts</strong><small>A share on nas.home, with your login</small></span>
  <button type="button" class="secondary small">Remove</button></div></div>`},
			{"Permissions", "ul.perm-list: code names in columns, as another app names them (an Immich key's permissions to tick). Usually inside details.form-more.", `
<ul class="perm-list"><li><code>asset.read</code></li><li><code>asset.upload</code></li><li><code>album.create</code></li><li><code>albumAsset.create</code></li><li><code>stack.create</code></li><li><code>tag.create</code></li></ul>`},
			{"What to do elsewhere, and what Roostack will do", "ol.todo-list: steps for the person to take on another machine, each with its command in a copyBox. ul.plain.will-list: what Roostack will do, an icon each.", `
<ol class="todo-list">
  <li><strong>Stop the old Immich, and keep it stopped.</strong> {{copyBox "docker compose stop immich-server"}}</li>
</ol>
<ul class="plain will-list">
  <li>{{icon "import"}}Copies what changed since the last copy, once more.</li>
  <li>{{icon "check"}}Starts Immich, and waits for it to answer.</li>
</ul>`},
			{"A table list whose rows stack on phones", "table.list.stack-phone: each cell may have a smaller second line (small); tr.row-muted for what isn't brought; on phones each row is a block. A middle cell that may be long is td.wrap.", `
<div class="table-wrap"><table class="list stack-phone">
  <thead><tr><th>Immich keeps</th><th>Brought from</th><th><span class="sr-only">Change</span></th></tr></thead>
  <tbody>
    <tr><td><strong>Your photos and videos</strong><small><code>/data</code> · backed up</small></td><td class="wrap">T7 Shield › immich › library<small>Was <code>/opt/immich/library</code></small></td><td><a href="#">Change…</a></td></tr>
    <tr class="row-muted"><td><strong>Smart search models</strong><small><code>/cache</code></small></td><td class="wrap">Not brought<small>Immich makes them again.</small></td><td><a href="#">Choose…</a></td></tr>
  </tbody>
</table></div>`},
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
			{"Needs attention", "The home page's list of things to deal with (template \"attention\"): each a notice (problem: notice-error, warning: notice-warn, or information), its first sentence in bold and the rest under it, and what to do as a small secondary button.", `
<div class="attention">
  <ul>
    <li class="item notice notice-warn">
      <span class="item-text"><strong>Backups are only stored on this server.</strong><span>Add a copy somewhere else to protect against theft, fire or a failed disk.</span></span>
      <a class="button secondary small" href="#">Improve backups</a>
    </li>
    <li class="item notice notice-error">
      <span class="item-text"><strong>Forgejo didn't finish setting up.</strong></span>
      <a class="button secondary small" href="#">See what went wrong</a>
    </li>
    <li class="item notice">
      <span class="item-text"><strong>Finishing setting up Immich…</strong></span>
      <a class="button secondary small" href="#">View</a>
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
	{ID: "settings", Title: "Settings navigation", Intro: "Settings is a grid of areas, each showing its state in a line; the sidebar lists its pages (all of them while you're in Settings).",
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
			{"Your account's tabs", "Your account's pages, as tabs at the top of each (Settings' pages are in the sidebar instead). The layout adds them for a page with an areaNav whose Tabs is set.", `
{{template "area-nav" .Account}}`},
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
	{ID: "monitoring", Title: "Monitoring", Intro: "Whether a check answers, and how it did each day (SPEC.md §6.15).",
		Examples: []galleryExample{
			{"Check state", "checkState with a check's state: up, down, paused (its service stopped on purpose, being updated…) or not checked yet.", `
{{checkState "up"}} {{checkState "down"}} {{checkState "paused"}} {{checkState "unknown"}}`},
			{"Uptime bar", "uptimeBar with a check's days (monitor.History.Days), oldest first: up whenever checked, down a little, down an hour or more, not checked. Each says how its day went on hover. .bar-ends labels the two ends. size=mini is the small one in lists.", `
{{uptimeBar .UptimeDays}}
<div class="bar-ends hint"><span>30 days ago</span><span>Today</span></div>
{{uptimeBar .UptimeDays "size=mini"}}`},
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
	{ID: "sign-in", Title: "Single sign-on", Intro: "The lab's sign-in page for services and its settings (SPEC.md §5.2; docs/mockups/sso). The sign-in page itself is signin.html, a page of its own in the lab's look.",
		Examples: []galleryExample{
			{"Theme choice", "div.theme-swatches of label.theme-swatch: its data-palette sets its theme's colours, shown by three .theme-swatch-dots (background, accent, highlight), with a hidden radio and the theme's name; the chosen one is outlined.", `
<div class="theme-swatches">
  <label class="theme-swatch" data-palette="teal"><input type="radio" name="g-palette" value="teal" checked><span class="theme-swatch-dots" aria-hidden="true"><i class="s-bg"></i><i class="s-ac"></i><i class="s-hl"></i></span><span>Teal and violet</span></label>
  <label class="theme-swatch" data-palette="nord"><input type="radio" name="g-palette" value="nord"><span class="theme-swatch-dots" aria-hidden="true"><i class="s-bg"></i><i class="s-ac"></i><i class="s-hl"></i></span><span>Nord</span></label>
  <label class="theme-swatch" data-palette="gruvbox"><input type="radio" name="g-palette" value="gruvbox"><span class="theme-swatch-dots" aria-hidden="true"><i class="s-bg"></i><i class="s-ac"></i><i class="s-hl"></i></span><span>Gruvbox</span></label>
</div>`},
			{"Services someone can use", "ul.signin-apps: each a link with its logo tile, title and address, on the sign-in page.", `
<ul class="signin-apps">
  <li><a href="#"><span class="logo-tile">{{icon "services"}}</span><span><strong>Mealie</strong><small>recipes.smith.family</small></span></a></li>
</ul>`},
			{"A group", "ul.group-list of details: the group's name and what it can use, then its form under Change….", `
<ul class="group-list"><li><details><summary><span class="group-name"><strong>Family</strong><small class="hint">5 people · Mealie</small></span><span class="more">Change…</span></summary><p class="hint">The group's form.</p></details></li></ul>`},
		}},
	{ID: "secrets", Title: "Secrets and codes", Intro: "Values to copy, such as a temporary password: shown once, easy to select.",
		Examples: []galleryExample{
			{"Secret", "", `
<code class="secret">EXAMPLE-ONLY-NOT-REAL</code>`},
			{"Copy box", "copyBox TEXT: something people copy and send or paste elsewhere (an invite, a command), with a copy icon at its top right that turns into a tick for a moment once copied (the Owner, 2026-10-20: a button beside it took space and wrapped under it on phones).", `
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
	// DarkHTML is the copy in the dark panel, its error scenes' SVG IDs
	// made its own: with the light copy's, the dark scene would use the
	// light one's gradient (an ID belongs to the first element with it).
	DarkHTML template.HTML
	// Component is true for template components ({{template ...}}),
	// false for plain HTML using CSS classes.
	Component bool
	// Rendered is the HTML a template component produces, formatted.
	Rendered string
}

// renderGallery writes the gallery page. static is where the CSS and
// scripts are ("/static/" in the UI, a relative path for the file).
func renderGallery(w io.Writer, static string, standalone bool) error {
	// The uptime bar names its days relative to the gallery's own day, not
	// the clock's, so the generated file is the same whatever day it's made.
	uptime := func(days []monitor.Day, opts ...string) (template.HTML, error) {
		return uptimeBarAt(galleryToday, days, opts...)
	}
	base, err := template.New("design.html").Funcs(templateFuncs).Funcs(template.FuncMap{"uptimeBar": uptime}).ParseFS(templateFS,
		"templates/components.html", "templates/parts.html", "templates/design.html")
	if err != nil {
		return err
	}
	sample := gallerySamples()
	// The credit's logo is a file beside its package; here, Roostack's own.
	sample["Credit"] = &projectCredit{Title: "Forgejo", IconURL: static + "apple-touch-icon.png", About: pkgspec.About{
		Maker: "a community project under Codeberg e.V., a non-profit", Licence: "GPL-3.0-or-later",
		Website: "https://forgejo.org", Source: "https://codeberg.org/forgejo/forgejo", Support: "https://liberapay.com/forgejo"}}
	sample["LogoURL"] = static + "apple-touch-icon.png"
	sample["TierOfficial"] = map[string]any{"Tier": "official", "Local": false}
	sample["TierCommunity"] = map[string]any{"Tier": "community", "Local": false}
	sample["TierLocal"] = map[string]any{"Tier": "", "Local": true}
	sample["PolicyProblems"] = []string{
		"containers.web.image: pin the image by digest (name:tag@sha256:…), so every lab runs exactly the image that was reviewed.",
		"containers.web.quadlet: Container.Volume=/home/me/scans:/consume isn't allowed: a folder from the server or a named volume is outside the service and its backups; use storage: and mounts: (an anonymous Volume=/path is allowed).",
	}
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
				DarkHTML:  template.HTML(strings.ReplaceAll(buf.String(), "es-wash-", "es-wash-dark-")),
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
		"Install":    install,
		"UptimeDays": galleryUptime(),
		"Chart": barChart{Title: "Visitors each day", Bars: bars([]int64{40, 55, 48, 70, 95, 62, 35},
			[]string{"Tue", "Wed", "Thu", "Fri", "Sat", "Sun", "Today"}, []string{"40 visitors", "55 visitors", "48 visitors", "70 visitors", "95 visitors", "62 visitors", "35 visitors"})},
		"Ranks":            topCountries(map[string]int64{"AU": 1130, "NZ": 96, "GB": 41}, 1284, 4),
		"InstallService":   ServiceView{ServiceDetail: node.ServiceDetail{Install: install}},
		"Account":          areaNav{Label: "Your account", Groups: []settingsGroup{{Areas: accountAreas}}, Active: "appearance", Tabs: true},
		"UpdatesActivity":  view("Installing updates", "", false, "==> Installing updates", "Unpacking curl (8.14.1-2+deb13u5) ..."),
		"FinishedActivity": view("Installed 3 updates: curl, libcurl4t64, tzdata.", "", true, "==> Installing updates", "==> Done"),
		"FailedActivity":   view("Installing updates", "exit status 100", true, "E: Could not get lock /var/lib/dpkg/lock-frontend", "==> Failed: exit status 100"),
		"Picker": folderPicker{Title: "Choose a folder on MEDIA", Crumbs: []mediaCrumb{{"MEDIA", "#"}, {"Films", ""}},
			Folders: []pickerFolder{{Name: "Blade Runner (1982)", Href: "#", Files: 3}, {Name: "Paddington 2 (2017)", Href: "#", Files: 2},
				{Name: "Extras", Href: "#", Folders: 4, Files: 1}},
			Label: "MEDIA › Films", Counts: "3 folders, 1 file", UseHref: "#", UseText: "Use Films", CancelHref: "#"},
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

// galleryToday is "today" for the gallery's uptime bars: a fixed day, so
// the gallery file doesn't change from day to day.
var galleryToday = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// galleryUptime is 30 days of a check that started 5 days in, was down a
// few minutes twice and an hour once, ending on galleryToday.
func galleryUptime() []monitor.Day {
	days := make([]monitor.Day, 30)
	for i := range days {
		d := monitor.Day{Date: galleryToday.AddDate(0, 0, i-29), Checks: 1440, Ups: 1440, State: monitor.DayOK}
		switch {
		case i < 5:
			d = monitor.Day{Date: d.Date, State: monitor.DayNone}
		case i == 12 || i == 25:
			d.Ups, d.State = 1436, monitor.DayWarn
		case i == 20:
			d.Ups, d.State = 1350, monitor.DayBad
		}
		days[i] = d
	}
	return days
}
