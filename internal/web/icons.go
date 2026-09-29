package web

import (
	"fmt"
	"html/template"
)

// Icons: small line drawings (24×24, drawn for Roostack) placed before a
// label to make lists quick to scan. They take the text's colour and are
// hidden from screen readers: the label beside each one says it all.
var icons = map[string]string{
	// Servers and their facts.
	"server":      `<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>`,
	"services":    `<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>`,
	"changes":     `<path d="M20 11a8 8 0 0 0-14.5-4.5L3 9"/><path d="M3 4v5h5"/><path d="M4 13a8 8 0 0 0 14.5 4.5L21 15"/><path d="M21 20v-5h-5"/>`,
	"disk":        `<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>`,
	"location":    `<path d="M12 21s-7-6.2-7-11.5a7 7 0 0 1 14 0C19 14.8 12 21 12 21z"/><circle cx="12" cy="9.5" r="2.5"/>`,
	"id":          `<path d="M9 3 7 21M17 3l-2 18M4 8.5h17M3 15.5h17"/>`,
	"certificate": `<path d="M12 3 5 6v5c0 4.5 3 8.3 7 10 4-1.7 7-5.5 7-10V6l-7-3z"/><path d="m9 12 2 2 4-4"/>`,
	// A link that opens in a new window (the new-window component).
	"help":       `<circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.4 2.3c-.6.3-.9.8-.9 1.4v.3"/><path d="M12 16.8h.01"/>`,
	"new-window": `<path d="M14 4h6v6"/><path d="M20 4l-9 9"/><path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>`,
	// Roostack's own services.
	"proxy":    `<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>`,
	"store":    `<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9"/><path d="M10 13h4"/>`,
	"roostack": `<path d="M3 11 12 4l9 7"/><path d="M5 10v10h14V10"/><path d="M10 20v-5h4v5"/>`,
	"network":  `<circle cx="5" cy="12" r="2.5"/><circle cx="19" cy="5" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="m7.2 10.9 9.6-4.8M7.2 13.1l9.6 4.8"/>`,
}

// icon draws one of icons; an unknown name is an error, so a typo fails
// the page (and its tests) rather than showing nothing.
func icon(name string) (template.HTML, error) {
	paths, ok := icons[name]
	if !ok {
		return "", fmt.Errorf("icon: no icon %q", name)
	}
	return template.HTML(`<svg class="icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" ` +
		`stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">` + paths + `</svg>`), nil
}

// builtinIcon is the icon for one of Roostack's own services (node.BuiltIn
// kinds: proxy, store, roostack, network).
func builtinIcon(kind string) string {
	if _, ok := icons[kind]; ok {
		return kind
	}
	return "roostack"
}
