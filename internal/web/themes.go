package web

import (
	"slices"

	"github.com/mylab/mylab/internal/store"
)

// Theme is a built-in colour theme: a palette file in
// static/css/themes/<ID>.css (docs/DESIGN.md).
type Theme struct {
	ID, Name, Description string
	// Source credits an open-source palette (empty for MyLab's own).
	Source *Credit
	// Accents are optional colours for actions (buttons, links), each a
	// [data-palette][data-accent] block in the theme's file. The first is
	// the default.
	Accents []Accent
}

// Accent is a named colour a theme offers for actions.
type Accent struct{ ID, Name string }

// catppuccinAccents are Catppuccin's named colours, without red and
// maroon (red buttons read as "danger").
var catppuccinAccents = []Accent{
	{"mauve", "Mauve"}, {"pink", "Pink"}, {"flamingo", "Flamingo"}, {"rosewater", "Rosewater"}, {"peach", "Peach"},
	{"yellow", "Yellow"}, {"green", "Green"}, {"teal", "Teal"}, {"sky", "Sky"}, {"sapphire", "Sapphire"},
	{"blue", "Blue"}, {"lavender", "Lavender"},
}

// DefaultPalette is the theme for new users and pages before login.
const DefaultPalette = "teal"

// Themes are the built-in themes, in the order they're offered.
var Themes = []Theme{
	{"teal", "Teal and violet", "MyLab's default", nil, nil},
	{"ocean", "Ocean and magenta", "Clear blue with a magenta spark", nil, nil},
	{"catppuccin", "Catppuccin", "Soft pastels: mauve and pink (Latte when light, Mocha when dark)",
		&Credit{Name: "Catppuccin", URL: "https://catppuccin.com", License: "MIT"}, catppuccinAccents},
	{"gruvbox", "Gruvbox", "Warm and retro: orange and yellow on browns",
		&Credit{Name: "Gruvbox", By: "Pavel Pertsev", URL: "https://github.com/morhetz/gruvbox", License: "MIT"}, nil},
	{"nord", "Nord", "Cool and calm: arctic blues",
		&Credit{Name: "Nord", By: "Arctic Ice Studio", URL: "https://www.nordtheme.com", License: "MIT"}, nil},
	{"solarized", "Solarized", "The classic: cream or deep teal, with blue and magenta",
		&Credit{Name: "Solarized", By: "Ethan Schoonover", URL: "https://ethanschoonover.com/solarized", License: "MIT"}, nil},
	{"everforest", "Everforest", "Soft greens, easy on the eyes",
		&Credit{Name: "Everforest", By: "sainnhe", URL: "https://github.com/sainnhe/everforest", License: "MIT"}, nil},
	{"rose-pine", "Rosé Pine", "Muted rose and pine (Dawn when light, Moon when dark)",
		&Credit{Name: "Rosé Pine", URL: "https://rosepinetheme.com", License: "MIT"}, nil},
	{"tokyo-night", "Tokyo Night", "Vivid blue and purple (Day when light, Night when dark)",
		&Credit{Name: "Tokyo Night", By: "Folke Lemaitre", URL: "https://github.com/folke/tokyonight.nvim", License: "Apache 2.0"}, nil},
}

// validPalette reports whether id is a built-in theme. Only these ever
// reach a stylesheet URL.
func validPalette(id string) bool {
	return slices.ContainsFunc(Themes, func(t Theme) bool { return t.ID == id })
}

// theme returns a built-in theme by ID.
func theme(id string) *Theme {
	for i := range Themes {
		if Themes[i].ID == id {
			return &Themes[i]
		}
	}
	return nil
}

// validAccent reports whether a theme offers an accent ("" is always
// valid: the theme's default).
func validAccent(palette, accent string) bool {
	t := theme(palette)
	return accent == "" || (t != nil && slices.ContainsFunc(t.Accents, func(a Accent) bool { return a.ID == accent }))
}

// userAccent is the user's accent if their theme offers it, else "".
func userAccent(u *store.User) string {
	if u != nil && u.Accent != "" && validAccent(userPalette(u), u.Accent) {
		return u.Accent
	}
	return ""
}

// userPalette is the user's theme, or the default.
func userPalette(u *store.User) string {
	if u != nil && validPalette(u.Palette) {
		return u.Palette
	}
	return DefaultPalette
}
