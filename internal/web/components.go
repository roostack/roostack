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
	"time"

	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/words"
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

// components is the parsed components.html. It's parsed on first use
// rather than as the variable's value: its functions (templateFuncs)
// render components, which would make that value depend on itself.
var components struct {
	once sync.Once
	t    *template.Template
	err  error
}

// componentTemplates is components.html, parsed once.
func componentTemplates() (*template.Template, error) {
	components.once.Do(func() {
		components.t, components.err = template.New("components").Funcs(templateFuncs()).ParseFS(templateFS, "templates/components.html")
	})
	return components.t, components.err
}

func renderComponent(name string, data any) (template.HTML, error) {
	t, err := componentTemplates()
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

// noticeList is a notice whose text leads into a list of items, such as
// what a package of the Owner's own asks for (SPEC.md §8.3.4). KIND as for
// notice; nothing shows without items.
func noticeList(kind, text string, items []string) (template.HTML, error) {
	if !slices.Contains([]string{"info", "ok", "warn", "error"}, kind) {
		return "", fmt.Errorf("noticeList: kind %q must be info, ok, warn or error", kind)
	}
	if len(items) == 0 {
		return "", nil
	}
	if kind == "info" {
		kind = ""
	}
	return renderComponent("notice-list", map[string]any{"Kind": kind, "Text": text, "Items": items})
}

// status is a service's state as a pill.
func status(state string) (template.HTML, error) {
	return renderComponent("status", state)
}

// uptimeBar shows a check's days, oldest first (SPEC.md §6.15): a bar per
// day, up, down a little, down for an hour or more, or not checked, each
// saying how it went on hover. The option size=mini makes the small one
// in lists.
func uptimeBar(days []monitor.Day, opts ...string) (template.HTML, error) {
	return uptimeBarAt(time.Now(), days, opts...)
}

// uptimeBarAt is uptimeBar with its days named relative to now ("Today",
// "Yesterday"), so the gallery can give it a fixed day.
func uptimeBarAt(now time.Time, days []monitor.Day, opts ...string) (template.HTML, error) {
	o, err := options("uptimeBar", opts, "size")
	if err != nil {
		return "", err
	}
	if s := o["size"]; s != "" && s != "mini" {
		return "", fmt.Errorf("uptimeBar: size %q must be mini or nothing", s)
	}
	type bar struct{ State, Title string }
	bars := make([]bar, len(days))
	for i, d := range days {
		bars[i] = bar{d.State, d.Title(now)}
	}
	return renderComponent("uptime-bar", map[string]any{"Bars": bars, "Mini": o["size"] == "mini", "Days": len(days)})
}

// checkState is a monitoring check's state as a pill: up, down, paused,
// or not checked yet.
func checkState(state string) (template.HTML, error) {
	if !slices.Contains([]string{monitor.StateUp, monitor.StateDown, monitor.StatePaused, monitor.StateUnknown}, state) {
		return "", fmt.Errorf("checkState: unknown state %q", state)
	}
	return renderComponent("check-state", state)
}

// badge labels something. Variants: "highlight" uses the brand highlight
// ("New"); "ok" for something done (Installed); "warn" for something to
// weigh before acting (a Community service).
func badge(text string, variant ...string) (template.HTML, error) {
	v := ""
	switch {
	case len(variant) == 0:
	case len(variant) == 1 && slices.Contains([]string{"highlight", "ok", "warn"}, variant[0]):
		v = variant[0]
	default:
		return "", fmt.Errorf("badge: variant %q must be \"highlight\", \"ok\", \"warn\" or nothing", variant)
	}
	return renderComponent("badge", map[string]any{"Text": text, "Variant": v})
}

// logo shows a service's logo (src, a URL on this site) on a light tile,
// the same in both modes, since projects draw their logos for light
// backgrounds (Vaultwarden's is black). Without one (src ""), the drawn
// icon instead (an icons name), on the same tile, so lists line up. The
// logo is decorative: the service's name is beside it.
func logo(src, iconName string) (template.HTML, error) {
	if strings.Contains(src, ":") || strings.HasPrefix(src, "//") {
		return "", fmt.Errorf("logo: %q must be a path on this site", src)
	}
	var drawn template.HTML
	if src == "" {
		var err error
		if drawn, err = icon(iconName); err != nil {
			return "", fmt.Errorf("logo: %w", err)
		}
	}
	return renderComponent("logo", map[string]any{"Src": src, "Icon": drawn})
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

// confirmSubmit is a form's submit button that asks question first, for a
// form with fields of its own (confirm makes its own form, with hidden
// fields only). The form's own checks (required fields) run before the
// dialog opens. kind is the dialog's button class ("danger" or "").
func confirmSubmit(question, text, kind string) (template.HTML, error) {
	if kind != "" && kind != "danger" {
		return "", fmt.Errorf("confirmSubmit: button kind %q must be danger or empty", kind)
	}
	return renderComponent("confirm-submit", map[string]any{"Question": question, "Text": text, "Kind": kind,
		"ID": confirmID("", question, nil)})
}

// copyBox shows text to copy (an invite, a command) with a Copy button.
func copyBox(text string) (template.HTML, error) {
	return renderComponent("copy-box", text)
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

// segmentChoice is one of a segmented control's choices.
type segmentChoice struct {
	Value, Text string
	Icon        template.HTML
}

// segChoice is a segmented control's choice: VALUE, TEXT and optionally
// an ICON (an icons name).
func segChoice(value, text string, iconName ...string) (segmentChoice, error) {
	if value == "" || text == "" || len(iconName) > 1 {
		return segmentChoice{}, fmt.Errorf("choice: %q %q needs a value and text, and at most one icon", value, text)
	}
	c := segmentChoice{Value: value, Text: text}
	if len(iconName) == 1 {
		var err error
		if c.Icon, err = icon(iconName[0]); err != nil {
			return segmentChoice{}, fmt.Errorf("choice: %w", err)
		}
	}
	return c, nil
}

// segmented is a small exclusive choice drawn as a row of buttons:
// radios named NAME (LABEL says what they choose, for screen readers),
// SELECTED checked (the first choice if it's ""), then two to four
// choices (choice VALUE TEXT [ICON]) and optionally "size=small". Inside
// a .seg-group, the choice's panel shows (a .seg-panel with data-seg
// counting choices from 1): CSS only, so it works without scripts.
func segmented(name, label, selected string, args ...any) (template.HTML, error) {
	var choices []segmentChoice
	var opts []string
	for _, a := range args {
		switch a := a.(type) {
		case segmentChoice:
			choices = append(choices, a)
		case string:
			opts = append(opts, a)
		default:
			return "", fmt.Errorf("segmented: %v is neither a choice nor an option", a)
		}
	}
	o, err := options("segmented", opts, "size")
	if err != nil {
		return "", err
	}
	if s := o["size"]; s != "" && s != "small" {
		return "", fmt.Errorf("segmented: size %q must be small or nothing", s)
	}
	if name == "" || label == "" || len(choices) < 2 || len(choices) > 4 {
		return "", fmt.Errorf("segmented: %q needs a name, a label and two to four choices", name)
	}
	found := selected == ""
	for i, c := range choices {
		for _, d := range choices[:i] {
			if d.Value == c.Value {
				return "", fmt.Errorf("segmented: %q has two choices %q", name, c.Value)
			}
		}
		found = found || c.Value == selected
	}
	if !found {
		return "", fmt.Errorf("segmented: %q has no choice %q", name, selected)
	}
	if selected == "" {
		selected = choices[0].Value
	}
	return renderComponent("segmented", map[string]any{"Name": name, "Label": label, "Selected": selected,
		"Choices": choices, "Small": o["size"] == "small"})
}

// folderCounts says what a folder holds ("2 folders, 1 file", "empty"),
// for the folder picker.
func folderCounts(folders, files int) string {
	var parts []string
	if folders > 0 {
		parts = append(parts, strconv.Itoa(folders)+words.Plural(folders, " folder", " folders"))
	}
	if files > 0 {
		parts = append(parts, strconv.Itoa(files)+words.Plural(files, " file", " files"))
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, ", ")
}

// pickerDialog is the dialog a folder picker opens in (app.js): a link
// with data-picker="ID" opens it, and a folder chosen replaces the page's
// part PLACE (an id selector, "#place-drive").
func pickerDialog(id, place string) (template.HTML, error) {
	if id == "" || !strings.HasPrefix(place, "#") || strings.ContainsAny(place, " ,>") {
		return "", fmt.Errorf("pickerDialog: %q needs an id, and %q an id selector", id, place)
	}
	return renderComponent("folder-picker-dialog", map[string]any{"ID": id, "Place": place})
}
