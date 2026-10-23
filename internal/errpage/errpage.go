// Package errpage holds Roostack's error scenes (the small mark's hen and
// server with props, drawn by assets/errors/generate.py) and the error
// pages Caddy shows for the lab's services (SPEC.md §6.3.4).
package errpage

import (
	"fmt"
	"html"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/caddy"
)

// Scene returns a scene's SVG with its wash's ID made unique by id (SVG IDs
// are page-wide, and each scene must use its own), or "" if there's no such
// scene. Its colours are es-* classes, which the page's CSS sets.
func Scene(name, id string) string {
	svg, ok := scenes[name]
	if !ok {
		return ""
	}
	return strings.ReplaceAll(svg, "@ID@", "es-wash-"+id)
}

// Names lists the scenes (for the design gallery).
func Names() []string {
	var out []string
	for name := range scenes {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Page is what an error page says.
type Page struct {
	Status int
	// Scene names the picture; Headline is the joke, Text the plain
	// explanation (SPEC.md §6.3.4: a joke headline, plain words underneath).
	Scene, Headline, Text string
}

// The services' pages, by the status Caddy answers with (403's are below).
// "{service}" is the service's title, filled in by Caddy (its route sets
// it).
var servicePages = []Page{
	{404, "vacant", "Nobody roosts here.", "No service uses this address. It may have been removed, or the link is wrong."},
	{429, "crowded", "Too many chickens.", "Too many requests from your address, so it's slowed down for a while. Try again in a minute."},
	{502, "asleep", "{service} is asleep on the job.",
		"It isn't answering right now. It may be stopped or still starting after a restart: try again in a few minutes."},
	{503, "hatching", "Still hatching.", "{service} is starting up. This usually takes a minute or two: this page will try again by itself."},
	{504, "slow", "Taking its sweet time.", "{service} took too long to answer. It may be busy (a big upload or an update): try again in a moment."},
	{0, "scrambled", "That one got scrambled.", "Something went wrong reaching {service}. Trying again often works."},
}

// The refused pages: CrowdSec's bouncer answers both a banned address and
// a request AppSec matched with a 403 (SPEC.md §6.10 item 4), so 403.html
// shows exploitPage when the route had passed the request on to AppSec
// (caddy.WAFVar), and bannedPage otherwise. Neither repeats anything from
// the request, nor has "Try again": the same request is refused again.
var (
	bannedPage = Page{403, "shooed", "Shooed out of the henhouse.",
		"Requests from your address are being turned away for a while because it was seen probing or attacking websites, this one or others. " +
			"If that wasn't you, it may have been someone else on the same network: try again later or from another connection."}
	exploitPage = Page{403, "shooed", "That one ruffled some feathers.",
		"This request was turned away before it reached {service}, because it looked like an attack. " +
			"If you were only using {service} as usual, tell whoever runs it: the check sometimes gets it wrong, and they can fix it."}
)

// CaddyPages are the files of the services' error pages, by file name, for
// /etc/caddy/errors (Caddy's handle_errors serves <status>.html, or
// error.html). They're Caddy templates: the service's title, whether it's
// private, its name and the error's details come from the route's vars
// (caddy.Route). Only a private service's page names the lab, links to
// Roostack and has the details; a public visitor needn't learn either.
// labName and ui (https://roost.<domain>) are baked in.
func CaddyPages(labName, ui string) map[string]string {
	out := map[string]string{}
	for _, p := range servicePages {
		name := "error.html"
		if p.Status != 0 {
			name = fmt.Sprintf("%d.html", p.Status)
		}
		out[name] = caddyPage(p, labName, ui)
	}
	out["403.html"] = refusedPage()
	return out
}

// Caddy template pieces. Values from requests are escaped with html; the
// service's title is the route's (caddy.Route checks it).
const (
	tplService = `{{$s := placeholder "http.vars.roostack_title"}}{{if $s}}{{$s | html}}{{else}}This service{{end}}`
	tplPrivate = `{{if eq (placeholder "http.vars.roostack_private") "yes"}}`
)

// fill escapes a page's words and puts the service's title in.
func fill(s string) string { return strings.ReplaceAll(html.EscapeString(s), "{service}", tplService) }

func caddyPage(p Page, labName, ui string) string {
	refresh := ""
	if p.Status == 503 {
		refresh = `<meta http-equiv="refresh" content="10">`
	}
	lab := html.EscapeString(labName)
	return pageHead(refresh, fill(p.Headline)) + `<body><main>` + Scene(p.Scene, "page") +
		`<p class="code">{{placeholder "http.error.status_code"}} · {{.Host | html}}</p>` +
		`<h1>` + fill(p.Headline) + `</h1><p>` + fill(p.Text) + `</p>` +
		`<p><a class="button" href="">Try again</a>` +
		tplPrivate + ` <a class="link" href="` + html.EscapeString(ui) + `{{with placeholder "http.vars.roostack_name"}}{{if ne . "roostack"}}/services/{{. | html}}{{end}}{{end}}">Check it in Roostack</a>{{end}}</p>` +
		tplPrivate + `<details><summary>Details</summary><p><code>{{placeholder "http.error.status_code"}} {{placeholder "http.error.status_text"}}` +
		` · {{placeholder "http.error.message" | html}} · {{now | date "2 Jan 2006 15:04"}}</code></p></details>` +
		`<p class="foot">Served by Roostack in ` + lab + `</p>{{end}}` +
		`</main></body></html>` + "\n"
}

// refusedPage is 403.html: bannedPage or exploitPage (above). Only public
// routes refuse, so like a public service's other pages it never names the
// lab, links to Roostack or has the details.
func refusedPage() string {
	waf := `{{if eq (placeholder "http.vars.` + caddy.WAFVar + `") "yes"}}`
	either := func(f func(Page) string) string {
		return waf + f(exploitPage) + `{{else}}` + f(bannedPage) + `{{end}}`
	}
	return pageHead("", either(func(p Page) string { return fill(p.Headline) })) + `<body><main>` +
		either(func(p Page) string { return Scene(p.Scene, "page") }) +
		`<p class="code">{{placeholder "http.error.status_code"}}</p>` +
		either(func(p Page) string { return `<h1>` + fill(p.Headline) + `</h1><p>` + fill(p.Text) + `</p>` }) +
		`</main></body></html>` + "\n"
}

// pageHead is the start of a page, up to its body.
func pageHead(extra, title string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		extra + `<meta name="robots" content="noindex"><title>` + title + `</title><style>` + sceneCSS + pageCSS + `</style></head>`
}

// pageCSS lays a service's error page out in the default theme's colours,
// light or dark with the visitor's device.
const pageCSS = `*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px 16px;` +
	`font:16px/1.5 system-ui,sans-serif;background:#ffffff;color:#1b2426}main{max-width:440px;text-align:center}` +
	`.es-scene{width:100%;max-width:320px;height:auto;overflow:visible}h1{font-size:1.5rem;line-height:1.25;margin:4px 0 8px}` +
	`.code{color:#56676a;font-size:.85rem;margin:0}.button{display:inline-block;background:#7c3aed;color:#fff;padding:8px 16px;` +
	`border-radius:8px;font-weight:600;text-decoration:none}.link{color:#7c3aed;margin-left:12px}details{text-align:left;font-size:.85rem;` +
	`margin-top:16px;color:#56676a}code{font-size:.8rem;overflow-wrap:anywhere}.foot{color:#56676a;font-size:.8rem;margin-top:20px}` +
	`@media (prefers-color-scheme: dark){body{background:#1a2021;color:#e5eceb}.code,details,.foot{color:#98a8a8}` +
	`.button{background:#c4b5fd;color:#1a2021}.link{color:#c4b5fd}}`
