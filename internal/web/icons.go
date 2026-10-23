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
	// Manage: a service's page, with its settings (sliders).
	"manage": `<path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1"/><circle cx="15" cy="6" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="18" r="2"/>`,
	"help":   `<circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.4 2.3c-.6.3-.9.8-.9 1.4v.3"/><path d="M12 16.8h.01"/>`,
	"at":     `<circle cx="12" cy="12" r="4"/><path d="M16 12v1.5a2.5 2.5 0 0 0 5 0V12a9 9 0 1 0-3.5 7.1"/>`,
	"clock":  `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
	// A link that opens in a new window (the new-window component).
	"new-window": `<path d="M14 4h6v6"/><path d="M20 4l-9 9"/><path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>`,
	// The sidebar (docs/DESIGN.md "Layout"): its main areas, the menu
	// button on phones, logging out, and the light/dark/auto toggle.
	"home-page": `<path d="M3 11 12 4l9 7"/><path d="M5 10v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-9"/><path d="M10 20v-5h4v5"/>`,
	"backups":   `<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"/>`,
	"menu":      `<path d="M4 7h16M4 12h16M4 17h16"/>`,
	// A pulse line (Monitoring, SPEC.md §6.15).
	"monitoring": `<path d="M3 12h4l3-8 4 16 3-8h4"/>`,
	"log-out":    `<path d="M15 4h4a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1h-4M10 17l5-5-5-5M15 12H4"/>`,
	"light":      `<circle cx="12" cy="12" r="4"/><path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4"/>`,
	"dark":       `<path d="M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5z"/>`,
	"auto":       `<circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 1 0 18z" fill="currentColor"/>`,
	// Settings' pages in the sidebar (settingsArea.Icon).
	"tag":      `<path d="M3 12V4a1 1 0 0 1 1-1h8l9 9-9 9z"/><circle cx="8" cy="8" r="1.5"/>`,
	"history":  `<path d="M3 12a9 9 0 1 0 3-6.7L3 8"/><path d="M3 3v5h5M12 7v5l3 2"/>`,
	"users":    `<circle cx="9" cy="8" r="3.5"/><path d="M3 20c0-3.3 2.7-6 6-6s6 2.7 6 6"/><circle cx="17" cy="9" r="2.5"/><path d="M16 14c2.8 0 5 2.2 5 5"/>`,
	"globe":    `<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3.5 3 14.5 0 18M12 3c-3 3.5-3 14.5 0 18"/>`,
	"cloud":    `<path d="M7 18a4.5 4.5 0 0 1-.5-9A6 6 0 0 1 18 8.5 4.75 4.75 0 0 1 17.5 18z"/>`,
	"terminal": `<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3M13 15h4"/>`,
	"sign-in":  `<rect x="4" y="11" width="16" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/><path d="M12 15v2"/>`,
	"warn":     `<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>`,
	"chevron":  `<path d="m10 7 5 5-5 5"/>`,
	"folder":   `<path d="M3 7.5A2.5 2.5 0 0 1 5.5 5H9l2 2.5h7.5A2.5 2.5 0 0 1 21 10v7.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 17.5z"/>`,
	"close":    `<path d="M6 6l12 12M18 6 6 18"/>`,
	"copy":     `<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3"/>`,
	"check":    `<path d="m5 12.5 4.5 4.5L19 7.5"/>`,
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	// Pausing public access (SPEC.md §6.16).
	"pause": `<rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/>`,
	// The package editor's (SPEC.md §8.3.7): importing a file, and a
	// suggestion. internal/editor/static/editor.js draws its own copies
	// (TestEditorIcons).
	"import": `<path d="M12 3v12M7 10l5 5 5-5"/><path d="M4 15v4a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-4"/>`,
	"spark":  `<path d="M12 3v4M12 17v4M3 12h4M17 12h4M6 6l2.5 2.5M15.5 15.5 18 18M6 18l2.5-2.5M15.5 8.5 18 6"/>`,
	// Roostack's own services.
	"proxy":    `<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>`,
	"store":    `<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9"/><path d="M10 13h4"/>`,
	"roostack": `<path d="M3 11 12 4l9 7"/><path d="M5 10v10h14V10"/><path d="M10 20v-5h4v5"/>`,
	"network":  `<circle cx="5" cy="12" r="2.5"/><circle cx="19" cy="5" r="2.5"/><circle cx="19" cy="19" r="2.5"/><path d="m7.2 10.9 9.6-4.8M7.2 13.1l9.6 4.8"/>`,
	// The catalog's categories (pkgspec.Categories, by key; network is
	// above), drawn as on the website: a service without a logo shows its
	// first category's.
	"photos":       `<rect x="3" y="5" width="18" height="14" rx="2"/><circle cx="9" cy="10" r="1.6"/><path d="m4 17 5-5 4 4 2.5-2.5L20 17"/>`,
	"media":        `<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 4v16M17 4v16M3 9h4M3 15h4M17 9h4M17 15h4"/>`,
	"documents":    `<path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>`,
	"productivity": `<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16M15 4v16"/><path d="M5.5 8h1.5M11 8h2M11 11h2M17 8h1.5"/>`,
	"home":         `<path d="M3 11 12 4l9 7"/><path d="M5 10v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-9"/><circle cx="12" cy="14.5" r="2.5"/>`,
	"passwords":    `<circle cx="8" cy="15" r="4"/><path d="m11 12 8-8M16 7l2 2M14 9l2 2"/>`,
	"development":  `<circle cx="6" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="8" r="2"/><path d="M6 7v10M18 10c0 4-6 3-11 7"/>`,
	"publishing":   `<path d="M4 20l4-1 11-11-3-3L5 16z"/><path d="M14 7l3 3M4 20h16"/>`,
}

// categoryIcon is the drawn icon for a service without a logo: its first
// category's, or the services icon.
func categoryIcon(categories []string) string {
	if len(categories) > 0 {
		if _, ok := icons[categories[0]]; ok {
			return categories[0]
		}
	}
	return "services"
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
