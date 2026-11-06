package web

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"image/png"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/usererr"
)

// The lab's look (SPEC.md §5.2, "The look of the sign-in page", and "The
// lab's name and look"): a logo and one of the built-in themes, with the
// lab's name. Chosen by the Owner on Settings → Name and look (labname.go;
// on People until 2026-11-06), and used by the sign-in page, the status
// page, and Roostack's own pages: the sidebar, the browser tab's icon,
// and the theme of anyone who hasn't chosen their own. The settings keep
// their first names (signin.*).

// Settings for the sign-in page (store settings).
const (
	settingSignInPalette = "signin.palette"
	settingSignInAccent  = "signin.accent"
	// settingSignInLogo is the logo's kind: logoMark, logoWide or ""
	// (none: the hen).
	settingSignInLogo = "signin.logo"
	// settingSignInFiles lists the logo files there are, with their types
	// ("mark=png wide=svg"), and changes with each upload, so browsers
	// fetch the new one.
	settingSignInFiles = "signin.files"
	// settingSignInRemember "off": "Keep me signed in" starts unticked.
	settingSignInRemember = "signin.remember"
	// settingSignInTwoFactor "everyone": everyone needs two-factor on the
	// sign-in page, not only who needs it for Roostack's own.
	settingSignInTwoFactor = "signin.two_factor"
)

// Logo kinds.
const (
	logoMark = "mark" // square, beside the name
	logoWide = "wide" // with the name in it, in place of the name
)

// logoParts are the files a logo can have: the mark; the wide logo, its
// version for dark mode, and a square icon for the browser tab with it.
var logoParts = []string{"mark", "wide", "dark", "icon"}

// maxLogo is a logo file's largest size.
const maxLogo = 200_000

// wideRatio is how many times as wide as it is tall a wide logo may be,
// and logoHeight the tallest it's shown, in CSS pixels. The maintainer
// (2026-11-06, on a real lab): a text logo didn't fit four times, and at
// 56 px with its padding it was small.
const (
	wideRatio  = 6
	logoHeight = 80
)

// signInLook is the sign-in page's look, for its template.
type signInLook struct {
	Palette, Accent string
	// Logo is logoMark, logoWide or "".
	Logo string
	// Files are the logo files there are (part → "png" or "svg"), and
	// Version changes with each upload.
	Files   map[string]string
	Version string
	// widths are the wide logo's and its dark version's widths when
	// logoHeight tall, where their files say their size (logoWidths).
	widths map[string]int
}

// Has reports whether a logo file is there.
func (l signInLook) Has(part string) bool { return l.Files[part] != "" }

// Width is a wide logo file's width when logoHeight tall (0 if its file
// doesn't say), for its img's width and height: with them, an SVG with
// only a viewBox still has a size to scale down from in a box that fits
// its content, and the page doesn't move as it loads.
func (l signInLook) Width(part string) int { return l.widths[part] }

// Height is logoHeight, for templates.
func (signInLook) Height() int { return logoHeight }

// sidebarLogoHeight is the tallest the wide logo is in Roostack's sidebar.
const sidebarLogoHeight = 40

// SidebarWidth is a wide logo file's width when sidebarLogoHeight tall
// (0 if its file doesn't say).
func (l signInLook) SidebarWidth(part string) int {
	if w := l.widths[part]; w > 0 {
		return max(1, w*sidebarLogoHeight/logoHeight)
	}
	return 0
}

// SidebarHeight is sidebarLogoHeight, for templates.
func (signInLook) SidebarHeight() int { return sidebarLogoHeight }

// ThemeName is the look's theme's name (Teal).
func (l signInLook) ThemeName() string {
	if t := theme(l.Palette); t != nil {
		return t.Name
	}
	return l.Palette
}

// lookSummary is Name and look's line on the Settings overview.
func (s *Server) lookSummary(ctx context.Context) string {
	l := s.signInLook(ctx)
	logo := map[string]string{logoMark: "its mark", logoWide: "a wide logo", "": "Roostack's hen"}[l.Logo]
	return s.labName() + " · " + logo + ", " + l.ThemeName()
}

// logoSizes keeps the wide logo's widths for the files' version, so that
// pages don't read the files each time.
type logoSizes struct {
	mu      sync.Mutex
	version string
	widths  map[string]int
}

// logoWidths reads the wide logo's and its dark version's sizes, once
// for each version of the files.
func (s *Server) logoWidths(l signInLook) map[string]int {
	s.logoSizes.mu.Lock()
	defer s.logoSizes.mu.Unlock()
	if s.logoSizes.widths != nil && s.logoSizes.version == l.Version {
		return s.logoSizes.widths
	}
	widths := map[string]int{}
	for _, part := range []string{"wide", "dark"} {
		if !l.Has(part) {
			continue
		}
		data, err := os.ReadFile(s.lookPath(part, l.Files[part]))
		if err != nil {
			continue
		}
		if w, h, ok := imageSize(data, l.Files[part]); ok {
			widths[part] = max(1, int(math.Round(w*logoHeight/h)))
		}
	}
	s.logoSizes.version, s.logoSizes.widths = l.Version, widths
	return widths
}

// imageSize is a PNG's size in pixels, or an SVG's from its width and
// height (in px or without a unit), else its viewBox; ok is false when
// it doesn't say.
func imageSize(data []byte, kind string) (w, h float64, ok bool) {
	if kind == "png" {
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			return 0, 0, false
		}
		return float64(cfg.Width), float64(cfg.Height), true
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	for {
		tok, err := d.Token()
		if err != nil {
			return 0, 0, false
		}
		el, isStart := tok.(xml.StartElement)
		if !isStart {
			continue
		}
		if el.Name.Local != "svg" {
			return 0, 0, false
		}
		attr := map[string]string{}
		for _, a := range el.Attr {
			attr[a.Name.Local] = strings.TrimSpace(a.Value)
		}
		w, wok := svgLength(attr["width"])
		h, hok := svgLength(attr["height"])
		if wok && hok {
			return w, h, true
		}
		box := strings.FieldsFunc(attr["viewBox"], func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
		if len(box) == 4 {
			w, werr := strconv.ParseFloat(box[2], 64)
			h, herr := strconv.ParseFloat(box[3], 64)
			if werr == nil && herr == nil && w > 0 && h > 0 {
				return w, h, true
			}
		}
		return 0, 0, false
	}
}

// svgLength is an SVG length in px or without a unit; a percentage or
// another unit doesn't give a size.
func svgLength(v string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "px"), 64)
	return f, err == nil && f > 0 && !math.IsInf(f, 0)
}

func (s *Server) signInLook(ctx context.Context) signInLook {
	get := func(k string) string { v, _ := s.Store.Setting(ctx, k); return v }
	l := signInLook{Palette: get(settingSignInPalette), Accent: get(settingSignInAccent), Logo: get(settingSignInLogo),
		Files: map[string]string{}}
	if !validPalette(l.Palette) {
		l.Palette, l.Accent = DefaultPalette, ""
	}
	if !validAccent(l.Palette, l.Accent) {
		l.Accent = ""
	}
	files, version, _ := strings.Cut(get(settingSignInFiles), ";")
	l.Version = version
	for _, f := range strings.Fields(files) {
		if part, kind, ok := strings.Cut(f, "="); ok && (kind == "png" || kind == "svg") {
			l.Files[part] = kind
		}
	}
	// A kind without its file shows the hen.
	if (l.Logo == logoMark && !l.Has("mark")) || (l.Logo == logoWide && !l.Has("wide")) {
		l.Logo = ""
	}
	if l.Has("wide") || l.Has("dark") {
		l.widths = s.logoWidths(l)
	}
	return l
}

func (s *Server) signInRememberDefault(ctx context.Context) bool {
	v, _ := s.Store.Setting(ctx, settingSignInRemember)
	return v != "off"
}

func (s *Server) lookPath(part, kind string) string {
	return filepath.Join(s.SSODir, "look-"+part+"."+kind)
}

// lookFile serves one of the logo's files. An SVG is served sandboxed, so
// even opened on its own it can't run anything on the sign-in host.
func (s *Server) lookFile(w http.ResponseWriter, r *http.Request) {
	part := r.PathValue("part")
	kind := s.signInLook(r.Context()).Files[part]
	if kind == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(s.lookPath(part, kind))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeImage(w, kind, data)
}

func writeImage(w http.ResponseWriter, kind string, data []byte) {
	if kind == "svg" {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	} else {
		w.Header().Set("Content-Type", "image/png")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(data)
}

// lookFavicon is the lab's mark (or the wide logo's icon) in the browser
// tab, else Roostack's own.
func (s *Server) lookFavicon(w http.ResponseWriter, r *http.Request) {
	l := s.signInLook(r.Context())
	part := ""
	switch {
	case l.Logo == logoMark:
		part = "mark"
	case l.Logo == logoWide && l.Has("icon"):
		part = "icon"
	}
	if part != "" {
		if data, err := os.ReadFile(s.lookPath(part, l.Files[part])); err == nil {
			writeImage(w, l.Files[part], data)
			return
		}
	}
	data, err := fs.ReadFile(staticFS, "static/favicon.ico")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Write(data)
}

// lookApp is the logo of a service with single sign-on, for "to continue
// to" and the list of services.
func (s *Server) lookApp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.SignIn.Client(name); !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.Manager.ServicePackage(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.Manager.PackageIcon(p.Name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeImage(w, "png", data)
}

// checkLogo checks an uploaded logo file and says its kind: a PNG, or an
// SVG with nothing that runs (it's also served sandboxed). wide: a wide
// logo, up to wideRatio times as wide as it is tall (an SVG's only when
// it says its size).
func checkLogo(data []byte, wide bool) (string, error) {
	if len(data) > maxLogo {
		return "", usererr.New("That file is too big: a logo can be at most 200 KB.")
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(data)); err == nil {
		if wide && (cfg.Width > wideRatio*cfg.Height || cfg.Width <= cfg.Height) {
			return "", errWideRatio
		}
		if !wide && (cfg.Width > cfg.Height*11/10 || cfg.Height > cfg.Width*11/10) {
			return "", usererr.New("A mark must be square (or nearly).")
		}
		return "png", nil
	}
	text := strings.ToLower(string(bytes.TrimLeft(data, "\xef\xbb\xbf \t\r\n")))
	if (strings.HasPrefix(text, "<svg") || strings.HasPrefix(text, "<?xml")) && strings.Contains(text, "<svg") {
		for _, bad := range []string{"<script", "javascript:", "<foreignobject", "<iframe", "<!entity"} {
			if strings.Contains(text, bad) {
				return "", usererr.New("That SVG has scripts or embedded pages in it. Save it again without them or use a PNG.")
			}
		}
		if onAttr(text) {
			return "", usererr.New("That SVG has scripts in it. Save it again without them or use a PNG.")
		}
		if w, h, ok := imageSize(data, "svg"); ok && wide && (w > wideRatio*h || w <= h) {
			return "", errWideRatio
		}
		return "svg", nil
	}
	return "", usererr.New("A logo must be a PNG or SVG file.")
}

// errWideRatio says how wide a wide logo may be.
var errWideRatio = usererr.New("A wide logo must be wider than it is tall, and at most six times as wide.")

// onAttr finds an event handler attribute (onload=, onclick=, …).
func onAttr(text string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], "on")
		if j < 0 {
			return false
		}
		k := i + j
		if k > 0 && strings.ContainsRune(" \t\r\n/\"'", rune(text[k-1])) {
			rest := text[k+2:]
			n := 0
			for n < len(rest) && rest[n] >= 'a' && rest[n] <= 'z' {
				n++
			}
			if n > 0 && strings.HasPrefix(strings.TrimLeft(rest[n:], " \t\r\n"), "=") {
				return true
			}
		}
		i = k + 2
	}
}

// saveLook saves the sign-in page's look from Settings → People: the
// theme, the logo's kind, and any files uploaded.
func (s *Server) saveLook(r *http.Request) error {
	ctx := r.Context()
	palette, accent := r.FormValue("palette"), r.FormValue("accent")
	if !validPalette(palette) {
		return usererr.New("Choose one of the themes.")
	}
	if !validAccent(palette, accent) {
		accent = ""
	}
	kind := r.FormValue("logo")
	if kind != logoMark && kind != logoWide {
		kind = ""
	}
	l := s.signInLook(ctx)
	files := l.Files
	changed := false
	for _, part := range logoParts {
		f, _, err := r.FormFile("logo_" + part)
		if errors.Is(err, http.ErrMissingFile) {
			continue
		}
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxLogo+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(data) == 0 {
			continue
		}
		ext, err := checkLogo(data, part == "wide" || part == "dark")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(s.SSODir, 0o700); err != nil {
			return err
		}
		for _, other := range []string{"png", "svg"} {
			os.Remove(s.lookPath(part, other))
		}
		if err := fsutil.WritePrivate(s.lookPath(part, ext), data); err != nil {
			return err
		}
		files[part], changed = ext, true
	}
	for _, part := range []string{"dark", "icon"} {
		if r.FormValue("remove_"+part) == "on" && files[part] != "" {
			os.Remove(s.lookPath(part, files[part]))
			delete(files, part)
			changed = true
		}
	}
	if kind == logoMark && files["mark"] == "" {
		return usererr.New("Choose a file for the mark, or another kind of logo.")
	}
	if kind == logoWide && files["wide"] == "" {
		return usererr.New("Choose a file for the wide logo, or another kind of logo.")
	}
	for k, v := range map[string]string{settingSignInPalette: palette, settingSignInAccent: accent, settingSignInLogo: kind} {
		if err := s.Store.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	if changed {
		var list []string
		for _, part := range logoParts {
			if files[part] != "" {
				list = append(list, part+"="+files[part])
			}
		}
		v := strings.Join(list, " ") + ";" + strconv.FormatInt(time.Now().UnixNano(), 36)
		if err := s.Store.SetSetting(ctx, settingSignInFiles, v); err != nil {
			return err
		}
	}
	return nil
}
