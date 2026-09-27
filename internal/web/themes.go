package web

import (
	"slices"

	"github.com/mylab/mylab/internal/store"
)

// Theme is a built-in colour theme: a palette file in
// static/css/themes/<ID>.css (docs/DESIGN.md).
type Theme struct {
	ID, Name, Description string
}

// DefaultPalette is the theme for new users and pages before login.
const DefaultPalette = "teal"

// Themes are the built-in themes, in the order they're offered.
var Themes = []Theme{
	{"teal", "Teal and violet", "MyLab's default"},
	{"ocean", "Ocean and magenta", "Clear blue with a magenta spark"},
	{"catppuccin-frappe", "Catppuccin Frappé", "Catppuccin Latte when light, Frappé when dark"},
	{"catppuccin-macchiato", "Catppuccin Macchiato", "Catppuccin Latte when light, Macchiato when dark"},
	{"catppuccin-mocha", "Catppuccin Mocha", "Catppuccin Latte when light, Mocha when dark"},
}

// validPalette reports whether id is a built-in theme. Only these ever
// reach a stylesheet URL.
func validPalette(id string) bool {
	return slices.ContainsFunc(Themes, func(t Theme) bool { return t.ID == id })
}

// userPalette is the user's theme, or the default.
func userPalette(u *store.User) string {
	if u != nil && validPalette(u.Palette) {
		return u.Palette
	}
	return DefaultPalette
}
