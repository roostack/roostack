package web

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Components are called from templates as functions (docs/DESIGN.md):
//
//	{{notice "warn" "Backups are only stored on this server." "link=/backups" "linkText=Improve backups"}}
//	{{status .Service.Status}}
//	{{badge "New" "highlight"}}
//	{{back "/services" "Services"}}
//	{{confirm "/users/3/remove" "Remove dad?" "Remove dad" "quiet danger"}}
//	{{activity .BackupActivity}}
//
// Their markup lives in templates/components.html; each function checks
// its arguments and runs that template, so HTML escaping still happens
// there. A wrong argument (e.g. an unknown kind) is an error, which
// becomes an error page and fails tests rather than rendering quietly.

var componentFuncs = template.FuncMap{
	"notice":      notice,
	"status":      status,
	"badge":       badge,
	"back":        back,
	"help":        helpLink,
	"confirm":     confirmButton,
	"activity":    activityBlock,
	"icon":        icon,
	"server":      serverChip,
	"builtinIcon": builtinIcon,
}

func init() {
	for name, fn := range componentFuncs {
		templateFuncs[name] = fn
	}
}

// components is the parsed components.html, loaded on first use (after
// templateFuncs, which it needs, is complete).
var components = sync.OnceValues(func() (*template.Template, error) {
	return template.New("components").Funcs(templateFuncs).ParseFS(templateFS, "templates/components.html")
})

func renderComponent(name string, data any) (template.HTML, error) {
	t, err := components()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// options parses trailing "name=value" arguments, allowing only known names.
func options(component string, args []string, allowed ...string) (map[string]string, error) {
	opts := map[string]string{}
	for _, a := range args {
		name, value, ok := strings.Cut(a, "=")
		if !ok || !slices.Contains(allowed, name) {
			return nil, fmt.Errorf("%s: unknown option %q (allowed: %s)", component, a, strings.Join(allowed, ", "))
		}
		opts[name] = value
	}
	return opts, nil
}

// notice: kind is "info", "ok", "warn" or "error"; options link= and
// linkText= add a link after the text.
func notice(kind, text string, opts ...string) (template.HTML, error) {
	if !slices.Contains([]string{"info", "ok", "warn", "error"}, kind) {
		return "", fmt.Errorf("notice: kind %q must be info, ok, warn or error", kind)
	}
	o, err := options("notice", opts, "link", "linkText")
	if err != nil {
		return "", err
	}
	if kind == "info" {
		kind = ""
	}
	return renderComponent("notice", map[string]any{"Kind": kind, "Text": text, "Link": o["link"], "LinkText": o["linkText"]})
}

// status is a service's state as a pill.
func status(state string) (template.HTML, error) {
	return renderComponent("status", state)
}

// badge labels something; variant "highlight" uses the brand highlight.
func badge(text string, variant ...string) (template.HTML, error) {
	highlight := false
	switch {
	case len(variant) == 0:
	case len(variant) == 1 && variant[0] == "highlight":
		highlight = true
	default:
		return "", fmt.Errorf("badge: variant %q must be \"highlight\" or nothing", variant)
	}
	return renderComponent("badge", map[string]any{"Text": text, "Highlight": highlight})
}

// back links to the parent page.
func back(href, text string) (template.HTML, error) {
	return renderComponent("back", map[string]any{"Href": href, "Text": text})
}

// helpLink links to the help page about this one (SPEC.md §6.3.1), in a
// new window. page is its path in the help without slashes at the ends,
// optionally with a #section ("settings/people#roles"; "" is the help's
// home), and title is that page's or section's title. A test checks every
// page and section named in the templates exists in help/docs.
func helpLink(page, title string) (template.HTML, error) {
	if strings.HasPrefix(page, "/") || strings.Contains(page, "..") || title == "" {
		return "", fmt.Errorf("help: %q must be a help path like \"backups/places\", with a title", page)
	}
	path, section, _ := strings.Cut(page, "#")
	href := "/help/"
	if path != "" {
		href += strings.TrimSuffix(path, "/") + "/"
	}
	if section != "" {
		href += "#" + section
	}
	return renderComponent("help", map[string]any{"Href": href, "Title": title})
}

// dialogKind is the dialog's own button: a full button even when the one
// opening it is quiet (a link-like "Delete" next to a list item).
func dialogKind(kind string) string {
	var ks []string
	for _, k := range strings.Fields(kind) {
		if k != "quiet" {
			ks = append(ks, k)
		}
	}
	return strings.Join(ks, " ")
}

// confirmID names a confirm dialog's question, for its label: the same
// on every render, so pages (and the design gallery) don't change.
func confirmID(action, question string, fields []string) string {
	h := fnv.New64a()
	h.Write([]byte(action + "\x00" + question + "\x00" + strings.Join(fields, "\x00")))
	return "confirm-" + strconv.FormatUint(h.Sum64(), 36)
}

// confirmButton posts to action after a dialog asks question (a <dialog>,
// styled like the rest of Roostack, rather than the browser's own). kind is
// the button's class ("danger", "secondary", "quiet danger", or ""); each
// trailing "name=value" becomes a hidden form field.
func confirmButton(action, question, text, kind string, fields ...string) (template.HTML, error) {
	for _, k := range strings.Fields(kind) {
		if !slices.Contains([]string{"danger", "secondary", "quiet"}, k) {
			return "", fmt.Errorf("confirm: button kind %q must use danger, secondary or quiet", kind)
		}
	}
	type field struct{ Name, Value string }
	var fs []field
	for _, f := range fields {
		name, value, ok := strings.Cut(f, "=")
		if !ok || name == "" {
			return "", fmt.Errorf("confirm: hidden field %q must be name=value", f)
		}
		fs = append(fs, field{name, value})
	}
	return renderComponent("confirm", map[string]any{"Action": action, "Question": question, "Text": text,
		"Kind": kind, "DialogKind": dialogKind(kind), "Fields": fs, "ID": confirmID(action, question, fields)})
}

// activityBlock shows a host operation; nothing if there is none.
func activityBlock(v *activityView) (template.HTML, error) {
	if v == nil {
		return "", nil
	}
	return renderComponent("activity", v)
}

// serverChip shows a server's name as one of the lab's servers: a chip
// with the server icon ("On [nuc]").
func serverChip(name string) (template.HTML, error) {
	return renderComponent("server", name)
}
