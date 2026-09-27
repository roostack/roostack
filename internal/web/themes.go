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
}

// DefaultPalette is the theme for new users and pages before login.
const DefaultPalette = "teal"

// Themes are the built-in themes, in the order they're offered.
var Themes = []Theme{
	{"teal", "Teal and violet", "MyLab's default", nil},
	{"ocean", "Ocean and magenta", "Clear blue with a magenta spark", nil},
	{"catppuccin", "Catppuccin", "Soft pastels: mauve and pink (Latte when light, Mocha when dark)",
		&Credit{Name: "Catppuccin", URL: "https://catppuccin.com", License: "MIT"}},
	{"gruvbox", "Gruvbox", "Warm and retro: orange and yellow on browns",
		&Credit{Name: "Gruvbox", By: "Pavel Pertsev", URL: "https://github.com/morhetz/gruvbox", License: "MIT"}},
	{"nord", "Nord", "Cool and calm: arctic blues",
		&Credit{Name: "Nord", By: "Arctic Ice Studio", URL: "https://www.nordtheme.com", License: "MIT"}},
	{"solarized", "Solarized", "The classic: cream or deep teal, with blue and magenta",
		&Credit{Name: "Solarized", By: "Ethan Schoonover", URL: "https://ethanschoonover.com/solarized", License: "MIT"}},
	{"everforest", "Everforest", "Soft greens, easy on the eyes",
		&Credit{Name: "Everforest", By: "sainnhe", URL: "https://github.com/sainnhe/everforest", License: "MIT"}},
	{"rose-pine", "Rosé Pine", "Muted rose and pine (Dawn when light, Moon when dark)",
		&Credit{Name: "Rosé Pine", URL: "https://rosepinetheme.com", License: "MIT"}},
	{"tokyo-night", "Tokyo Night", "Vivid blue and purple (Day when light, Night when dark)",
		&Credit{Name: "Tokyo Night", By: "Folke Lemaitre", URL: "https://github.com/folke/tokyonight.nvim", License: "Apache 2.0"}},
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
