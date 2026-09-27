package web

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// Every built-in theme must be readable in both modes (WCAG contrast).
// Theme files use plain hex colours so this test can check them.

var cssVar = regexp.MustCompile(`--([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\s*;`)

func readVars(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	for _, m := range cssVar.FindAllStringSubmatch(string(data), -1) {
		vars[m[1]] = m[2]
	}
	return vars
}

func luminance(hex string) float64 {
	var c [3]float64
	for i := range c {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		s := float64(v) / 255
		if s <= 0.03928 {
			c[i] = s / 12.92
		} else {
			c[i] = math.Pow((s+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

var cssBlock = regexp.MustCompile(`(?s)(\[data-palette="[a-z-]+"\](?:\[data-accent="([a-z-]+)"\])?)\s*\{(.*?)\}`)

// themeVariants reads a theme file: the base palette, and one variant per
// accent block with the accent's colours laid over the base.
func themeVariants(t *testing.T, id string) map[string]map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("static", "css", "themes", id+".css"))
	if err != nil {
		t.Fatal(err)
	}
	parse := func(body string) map[string]string {
		vars := map[string]string{}
		for _, m := range cssVar.FindAllStringSubmatch(body, -1) {
			vars[m[1]] = m[2]
		}
		return vars
	}
	variants := map[string]map[string]string{}
	var base map[string]string
	for _, m := range cssBlock.FindAllStringSubmatch(string(data), -1) {
		if m[2] == "" {
			base = parse(m[3])
			variants[""] = base
		}
	}
	for _, m := range cssBlock.FindAllStringSubmatch(string(data), -1) {
		if m[2] != "" {
			v := map[string]string{}
			for k, val := range base {
				v[k] = val
			}
			for k, val := range parse(m[3]) {
				v[k] = val
			}
			variants[m[2]] = v
		}
	}
	return variants
}

func TestThemesAreReadable(t *testing.T) {
	shared := readVars(t, filepath.Join("static", "css", "tokens.css"))
	for _, th := range Themes {
		variants := themeVariants(t, th.ID)
		for _, a := range th.Accents {
			if variants[a.ID] == nil {
				t.Errorf("%s: accent %s has no [data-accent] block", th.ID, a.ID)
			}
		}
		for variant, pal := range variants {
			name := th.ID
			if variant != "" {
				name += "/" + variant
			}
			for _, mode := range []string{"l", "d"} {
				// get resolves a purpose token like tokens.css does.
				get := func(name string) string {
					if v, ok := pal["pal-"+name+"-"+mode]; ok {
						return v
					}
					if v, ok := shared["status-"+name+"-"+mode]; ok { // ok, warn, bad(-bg, -fg)
						return v
					}
					t.Fatalf("%s: no --pal-%s-%s", th.ID, name, mode)
					return ""
				}
				check := func(fg, bg string, min float64) {
					if c := contrast(get(fg), get(bg)); c < min {
						t.Errorf("%s (%s): %s %s on %s %s is %.2f:1, need %.1f", name, map[string]string{"l": "light", "d": "dark"}[mode],
							fg, get(fg), bg, get(bg), c, min)
					}
				}
				for _, bg := range []string{"bg", "surface", "surface-2"} {
					check("fg", bg, 4.5)
					check("muted", bg, 4.5)
					check("accent", bg, 4.5)  // links
					check("highlight", bg, 3) // brand mark, focus: not text
				}
				check("accent-fg", "accent", 4.5) // primary buttons
				check("bad-fg", "bad", 4.5)       // danger buttons
				for _, tint := range []string{"accent-soft", "ok-bg", "warn-bg", "bad-bg"} {
					check("fg", tint, 4.5) // text in notices
				}
				for _, s := range []string{"ok", "warn", "bad"} {
					check(s, s+"-bg", 4.5)   // status pills
					check(s, "surface", 4.5) // status text on cards
				}
			}
		}
	}
}

func TestEveryThemeHasAFile(t *testing.T) {
	for _, th := range Themes {
		if _, err := os.Stat(filepath.Join("static", "css", "themes", th.ID+".css")); err != nil {
			t.Error(fmt.Sprintf("theme %s: %v", th.ID, err))
		}
	}
	files, _ := filepath.Glob(filepath.Join("static", "css", "themes", "*.css"))
	if len(files) != len(Themes) {
		t.Errorf("%d theme files but %d registered themes", len(files), len(Themes))
	}
}
