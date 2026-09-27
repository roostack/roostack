package web

import (
	"bytes"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"sync"
)

// Components are called from templates as functions (docs/DESIGN.md):
//
//	{{notice "warn" "Backups are only stored on this machine." "link=/backups" "linkText=Improve backups"}}
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
	"notice":   notice,
	"status":   status,
	"badge":    badge,
	"back":     back,
	"confirm":  confirmButton,
	"activity": activityBlock,
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

// confirmButton posts to action after the browser asks question. kind is
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
		"Kind": kind, "Fields": fs})
}

// activityBlock shows a host operation; nothing if there is none.
func activityBlock(v *activityView) (template.HTML, error) {
	if v == nil {
		return "", nil
	}
	return renderComponent("activity", v)
}
