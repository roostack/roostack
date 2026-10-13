"""Generate Roostack's brand files (see README.md).

Usage, from anywhere:

    python3 assets/brand/generate.py [--font SpaceGrotesk.ttf]

Always writes the marks and favicon (assets/brand), the app's copies
(internal/web/brandmark.go and internal/web/static/) and the help site's
logo (help/docs/assets/logo.svg). PNG and ICO files need rsvg-convert.

The wordmarks and lockups need the Space Grotesk variable font
(https://github.com/google/fonts/tree/main/ofl/spacegrotesk, OFL) and the
fontTools Python package; without --font they're left as they are. The text
is converted to outlines, so nothing loads a font.
"""
import argparse
import pathlib
import re
import shutil
import struct
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
BRAND = ROOT / "assets" / "brand"
STATIC = ROOT / "internal" / "web" / "static"

# Teal and violet (internal/web/static/css/themes/teal.css) and status greens (tokens.css).
# The online lights sit on the server body, which is the opposite of the background, so each
# mode uses the other mode's status green.
MODES = {
    "light": dict(fg="#1b2426", hi="#7c3aed", soft="#ede9fe", led="#7ee2ae"),
    "dark": dict(fg="#e5eceb", hi="#c4b5fd", soft="#2a1e47", led="#17603f"),
}
# Mono: drawn white into a mask, cut-outs black, then filled with currentColor.
MASK = dict(fg="#fff", hi="#fff", soft="#000", led="#000")

# Geometry is written once with colour roles (@fg, @hi, @soft, @led); paint() swaps in colours
# and classify() swaps in CSS classes (for the favicon's light/dark and the app's themes).
ROLES = dict(body="@fg", comb="@hi", cut="@soft", crack="@soft", led="@led", cloud="@hi",
             cloud_led="@led", dim=".45", sheen=".55")

# ---------- Bird: one silhouette, tail feathers, wing, three-lobed comb, wattle, gripping toes ----------
BIRD_BODY = (
    "M24 35C30 37.5 38 36 41 30C43 26 43.5 23 42.6 20.6L44.6 18.6"   # belly, breast, neck, chin
    "C44.9 12.6 42.2 10.4 39 10.4C35.8 10.4 33.4 12.8 33.4 16"       # head
    "C33.4 18.5 32.4 20 30 21C26 22.6 22 22.6 19.6 21.4"             # back
    "C17 18 14.5 14.5 11.6 12.6C11.2 15.6 12 18 13.4 19.8"           # tail feathers: upper,
    "C11.6 19 9.6 18.8 8 19.4C9.6 21.4 11.6 22.6 13.6 23.4"          # middle,
    "C12.2 23.8 10.8 24.6 10 25.6C12 26.6 14 26.8 15.4 26.6"         # lower
    "C17 31 20 34 24 35Z"                                            # rump
)
COMB = "M34.5 14C33 11 34 8.2 36.3 8.6C36.6 6.2 39.6 5.6 40.4 7.8C42.2 6.9 44.2 8.6 43.2 11.2C42.8 12.4 42 13.4 41 14Z"
WATTLE = "M43.4 18C45.8 18.4 46.6 21.8 45.2 24.2C43.4 23.6 42.4 20.4 43.4 18Z"
BEAK = "M44.2 14.6 49.2 16.6 44.4 18.6Z"
WING = "M21 25.4C25.5 30 32.5 30.6 37 26.4"


def bird(c, feet_y=37.4, legs=((27, 34), (31.4, 34)), wing=True):
    # Comb, wattle and beak go first; the body, outlined in the cut colour, covers their bases
    # and keeps a light comb separate from a light head in dark mode.
    s = (
        f'<path d="{COMB}" fill="{c["comb"]}"/>'
        f'<path d="{WATTLE}" fill="{c["comb"]}"/>'
        f'<path d="{BEAK}" fill="{c["body"]}"/>'
        f'<path d="{BIRD_BODY}" fill="{c["body"]}" stroke="{c["cut"]}" stroke-width="1.1" stroke-linejoin="round"/>'
    )
    if wing:
        s += f'<path d="{WING}" fill="none" stroke="{c["cut"]}" stroke-width="1.5" stroke-linecap="round"/>'
    s += f'<circle cx="40.2" cy="14.6" r="1.3" fill="{c["cut"]}"/>'
    for x, top in legs:
        s += (f'<path d="M{x:g} {top:g}V{feet_y:g}M{x-1.6:g} {feet_y:g}H{x+2:g}" fill="none" stroke="{c["body"]}" '
              f'stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>')
    return s


# ---------- Server units: rack ears, two lights, vents ----------
def unit(c, y):
    s = (
        f'<rect x="7.5" y="{y+1}" width="3.5" height="8" rx="1" fill="{c["body"]}"/>'
        f'<rect x="53" y="{y+1}" width="3.5" height="8" rx="1" fill="{c["body"]}"/>'
        f'<circle cx="9.25" cy="{y+5}" r=".9" fill="{c["cut"]}"/>'
        f'<circle cx="54.75" cy="{y+5}" r=".9" fill="{c["cut"]}"/>'
        f'<rect x="10.2" y="{y}" width="43.6" height="10" rx="2.4" fill="{c["body"]}"/>'
        f'<circle cx="16" cy="{y+5}" r="1.9" fill="{c["led"]}"/>'
        f'<circle cx="21.2" cy="{y+5}" r="1.1" fill="{c["cut"]}" fill-opacity="{c["dim"]}"/>'
    )
    for i in range(5):
        x = 34 + i * 3.6
        s += f'<rect x="{x:g}" y="{y+2.6:g}" width="1.5" height="4.8" rx=".75" fill="{c["cut"]}" fill-opacity="{c["dim"]}"/>'
    return s


def perch(c):
    return bird(c) + unit(c, 38.6) + unit(c, 50.6)


# ---------- Egg and woven nest ----------
EGG = "M32 4C39.6 4 46 15.6 46 25.6C46 34.2 39.8 40.4 32 40.4C24.2 40.4 18 34.2 18 25.6C18 15.6 24.4 4 32 4Z"
CRACK = "M19.6 23 23.8 26.6 27.2 21.8 31 27 34.6 21.4 38.6 26 44.4 22"
CRACK2 = "M31 27 30.4 30.6"
SHEEN = "M24.2 14.4C25 11.6 26.6 9.6 28.4 8.6"


def egg(c, dy=0):
    t = f' transform="translate(0 {dy})"' if dy else ""
    return (
        f'<g{t}><path d="{EGG}" fill="{c["body"]}"/>'
        f'<path d="{CRACK}" fill="none" stroke="{c["crack"]}" stroke-width="2.3" stroke-linecap="round" stroke-linejoin="round"/>'
        f'<path d="{CRACK2}" fill="none" stroke="{c["crack"]}" stroke-width="1.4" stroke-linecap="round"/>'
        f'<path d="{SHEEN}" fill="none" stroke="{c["cut"]}" stroke-width="1.6" stroke-linecap="round" stroke-opacity="{c["sheen"]}"/></g>'
    )


def layer(c, x, y, w, flip):
    """One nest layer: a rounded twig bundle with a few evenly spaced weave strokes, centred."""
    s = f'<rect x="{x}" y="{y}" width="{w}" height="7.4" rx="3.7" fill="{c["body"]}" stroke="{c["cut"]}" stroke-width="2.2"/>'
    n = round((w - 8) / 10)
    gap = 10
    start = x + (w - (n - 1) * gap) / 2 - 1.3
    for i in range(n):
        tx = start + i * gap
        a, b = (tx, tx + 2.6) if not flip else (tx + 2.6, tx)
        s += (f'<path d="M{a:g} {y+2.1:g}L{b:g} {y+5.3:g}" stroke="{c["cut"]}" stroke-width="1.3" '
              f'stroke-linecap="round" stroke-opacity="{c["dim"]}"/>')
    return s


def nest(c):
    return egg(c) + layer(c, 4, 34.4, 56, False) + layer(c, 9, 43.6, 46, True) + layer(c, 16, 52.8, 32, False)


# ---------- Clouds ----------
def vents(c, x0, y, n=4):
    return "".join(
        f'<rect x="{x0 + i*3.6:g}" y="{y}" width="1.5" height="4.6" rx=".75" fill="{c["cut"]}" fill-opacity="{c["dim"]}"/>'
        for i in range(n))


def perch_cloud(c):
    cl = c["cloud"]
    return bird(c, feet_y=37.6) + (
        f'<rect x="4" y="46" width="56" height="14" rx="7" fill="{cl}"/>'
        f'<circle cx="15.5" cy="49" r="7.5" fill="{cl}"/>'
        f'<circle cx="29" cy="47.4" r="8.6" fill="{cl}"/>'
        f'<circle cx="45" cy="46.6" r="10.4" fill="{cl}"/>'
        f'<circle cx="12.5" cy="53.2" r="1.9" fill="{c["cloud_led"]}"/>'
        f'<circle cx="17.6" cy="53.2" r="1.1" fill="{c["cut"]}" fill-opacity="{c["dim"]}"/>'
        + vents(c, 40, 50.9)
    )


CLOUD = ("M12 61H52C57 61 60.5 57.5 60.5 53 60.5 48.5 57 45 52.5 45 51.5 39.5 47 36 41.5 36 "
         "38.8 36 36.4 37 34.6 38.6 32.6 35.6 29.3 34 25.6 34 19.6 34 15 38.6 15 44.5 8.8 45 3.5 48.5 "
         "3.5 53 3.5 57.5 7.2 61 12 61Z")


def nest_cloud(c):
    return egg(c, -1.5) + (
        f'<path d="{CLOUD}" fill="{c["cloud"]}" stroke="{c["cut"]}" stroke-width="2.4" stroke-linejoin="round"/>'
        f'<circle cx="12.5" cy="53.2" r="1.9" fill="{c["cloud_led"]}"/>'
        f'<circle cx="17.6" cy="53.2" r="1.1" fill="{c["cut"]}" fill-opacity="{c["dim"]}"/>'
        + vents(c, 39, 50.9)
    )


# ---------- Small mark: favicon, app top bar, help logo ----------
def small(c):
    """The hen without its wing line, larger, on one plain server: clear at 16px."""
    return (
        f'<g transform="translate(-3.6 .6) scale(1.2)">{bird(c, wing=False)}</g>'
        f'<rect x="8" y="46.5" width="48" height="11.5" rx="3.2" fill="{c["body"]}"/>'
        f'<circle cx="15" cy="52.25" r="2.8" fill="{c["led"]}"/>'
    )


def small_cloud(c):
    """Roostack Cloud's small mark: the small mark's hen on a plain cloud (the highlight colour,
    as the Cloud marks), with one light: clear at 16px, and told apart from Roostack's at a glance."""
    cl = c["cloud"]
    return (
        f'<g transform="translate(-3.6 .6) scale(1.2)">{bird(c, wing=False)}</g>'
        f'<rect x="5" y="47.5" width="54" height="11.5" rx="5.75" fill="{cl}"/>'
        f'<circle cx="17" cy="48.6" r="6.2" fill="{cl}"/>'
        f'<circle cx="46" cy="47.6" r="7.6" fill="{cl}"/>'
        f'<circle cx="13.5" cy="53.25" r="2.8" fill="{c["cloud_led"]}"/>'
    )


MARKS = {"perch": perch, "nest": nest, "perch-cloud": perch_cloud, "nest-cloud": nest_cloud}
TITLES = {"perch": "Roostack", "nest": "Roostack", "perch-cloud": "Roostack Cloud", "nest-cloud": "Roostack Cloud"}


# ---------- Colour and class output ----------
def tile(inner, radius=14, inset=True):
    """The mark on a soft-highlight tile, inset to 81.25% (the small mark fills the tile itself)."""
    body = inner(ROLES)
    if inset:
        body = f'<g transform="translate(6 6) scale(.8125)">{body}</g>'
    return f'<rect width="64" height="64" rx="{radius}" fill="@soft"/>{body}'


def paint(s, colours):
    return re.sub(r'@([a-z]+)', lambda m: colours[m.group(1)], s)


def mono(inner, mask_id="m"):
    """One colour, no tile: every cut-out is truly transparent, so it works on any background."""
    shapes = paint(inner(dict(ROLES, dim="1", sheen="1")), MASK)
    return (f'<mask id="{mask_id}" maskUnits="userSpaceOnUse" x="0" y="0" width="64" height="64">{shapes}</mask>'
            f'<rect width="64" height="64" fill="currentColor" mask="url(#{mask_id})"/>')


def classify(s, prefix):
    """Replace fill="@x" and stroke="@y" with classes PREFIXf-x and PREFIXs-y."""
    def fix(m):
        tag, classes = m.group(0), []
        for attr in ("fill", "stroke"):
            a = re.search(f' {attr}="@([a-z]+)"', tag)
            if a:
                classes.append(f"{prefix}{attr[0]}-{a.group(1)}")
                tag = tag.replace(a.group(0), "", 1)
        if classes:
            tag = re.sub(r'^<([a-z]+)', lambda t: f'<{t.group(1)} class="{" ".join(classes)}"', tag)
        return tag
    return re.sub(r'<[a-z]+[^>]*>', fix, s)


def class_rules(colours, prefix=""):
    return "".join(f".{prefix}f-{r}{{fill:{v}}}.{prefix}s-{r}{{stroke:{v}}}" for r, v in colours.items())


def svg(title, body, view="0 0 64 64", size=(64, 64), extra=""):
    return (f'<svg xmlns="http://www.w3.org/2000/svg"{extra} viewBox="{view}" width="{size[0]:g}" height="{size[1]:g}" '
            f'role="img" aria-label="{title}"><title>{title}</title>{body}</svg>\n')


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    return path


# ---------- Wordmark: Space Grotesk outlined ----------
class Wordmark:
    """Outlines text in Space Grotesk at fixed weights, with the font's kerning and CSS-style tracking."""

    def __init__(self, font_path):
        from fontTools.ttLib import TTFont
        from fontTools.varLib.instancer import instantiateVariableFont
        self.fonts = {}
        for w in (500, 700):
            self.fonts[w] = instantiateVariableFont(TTFont(font_path), {"wght": w})

    def kern(self, font, left, right):
        gpos = font.get("GPOS")
        if not gpos:
            return 0
        t = gpos.table
        lookups = {i for fr in t.FeatureList.FeatureRecord if fr.FeatureTag == "kern" for i in fr.Feature.LookupListIndex}
        for i in sorted(lookups):
            for st in t.LookupList.Lookup[i].SubTable:
                if getattr(st, "ExtSubTable", None):
                    st = st.ExtSubTable
                if getattr(st, "LookupType", 2) != 2 or left not in st.Coverage.glyphs:
                    continue
                if st.Format == 1:
                    for rec in st.PairSet[st.Coverage.glyphs.index(left)].PairValueRecord:
                        if rec.SecondGlyph == right and rec.Value1:
                            return getattr(rec.Value1, "XAdvance", 0) or 0
                else:
                    c1 = st.ClassDef1.classDefs.get(left, 0)
                    c2 = st.ClassDef2.classDefs.get(right, 0)
                    v = st.Class1Record[c1].Class2Record[c2].Value1
                    if v and getattr(v, "XAdvance", 0):
                        return v.XAdvance
        return 0

    def outline(self, runs, size, tracking):
        """runs: [(text, weight, role)]. Returns (svg paths with fill="@role", bounds) at font size `size`."""
        from fontTools.pens.boundsPen import BoundsPen
        from fontTools.pens.svgPathPen import SVGPathPen
        from fontTools.pens.transformPen import TransformPen
        out, x, bounds, prev = [], 0.0, None, None
        for text, weight, role in runs:
            font = self.fonts[weight]
            gs, cmap, upm = font.getGlyphSet(), font.getBestCmap(), font["head"].unitsPerEm
            scale = size / upm
            d = []
            for ch in text:
                g = cmap[ord(ch)]
                if prev and prev[0] is font:
                    x += self.kern(font, prev[1], g) * scale
                pen = SVGPathPen(gs, lambda v: f"{v:.2f}".rstrip("0").rstrip("."))
                bp = BoundsPen(gs)
                tf = (scale, 0, 0, -scale, x, 0)
                gs[g].draw(TransformPen(pen, tf))
                gs[g].draw(TransformPen(bp, tf))
                if pen.getCommands():
                    d.append(pen.getCommands())
                if bp.bounds:
                    b = bp.bounds
                    bounds = b if not bounds else (min(bounds[0], b[0]), min(bounds[1], b[1]),
                                                   max(bounds[2], b[2]), max(bounds[3], b[3]))
                x += gs[g].width * scale + tracking * size
                prev = (font, g)
            out.append(f'<path fill="@{role}" d="{"".join(d)}"/>')
        return "".join(out), bounds


# Perch: "roo" medium + "stack" bold, -0.03em. Nest: all medium, -0.02em. Cloud: "cloud" medium, highlight.
WORDMARKS = {
    "perch": ([("roo", 500, "fg"), ("stack", 700, "fg")], -0.03),
    "perch-cloud": ([("roo", 500, "fg"), ("stack", 700, "fg"), (" ", 500, "fg"), ("cloud", 500, "hi")], -0.03),
    "nest": ([("roostack", 500, "fg")], -0.02),
    "nest-cloud": ([("roostack ", 500, "fg"), ("cloud", 500, "hi")], -0.02),
}


def wordmarks(font_path):
    wm = Wordmark(font_path)
    written = []
    text_colours = {m: dict(fg=MODES[m]["fg"], hi=MODES[m]["hi"]) for m in MODES}
    mono_colours = dict(fg="currentColor", hi="currentColor")
    for name, (runs, tracking) in WORDMARKS.items():
        title = TITLES[name]
        # Wordmark alone, 32px type, tight to the letters.
        paths, (x0, y0, x1, y1) = wm.outline(runs, 32, tracking)
        flip = paths  # glyphs are drawn with the baseline at y=0
        view = f"{x0:.2f} {y0:.2f} {x1-x0:.2f} {y1-y0:.2f}"
        size = (round(x1 - x0, 2), round(y1 - y0, 2))
        for m in MODES:
            written.append(write(BRAND / f"wordmark-{name}-{m}.svg", svg(title, paint(flip, text_colours[m]), view, size)))
        written.append(write(BRAND / f"wordmark-{name}-mono.svg",
                             svg(title, paint(flip, mono_colours), view, size, f' color="{MODES["light"]["fg"]}"')))
        # Lockup: the 64-unit mark, a gap of 0.4em, then the 32-unit wordmark centred on the mark.
        gap = 0.4 * 32
        tx, ty = 64 + gap - x0, 32 - (y0 + y1) / 2
        width = 64 + gap + (x1 - x0)
        text = f'<g transform="translate({tx:.2f} {ty:.2f})">{paths}</g>'
        view, size = f"0 0 {width:.2f} 64", (round(width, 2), 64)
        for m in MODES:
            body = paint(tile(MARKS[name]) + text, dict(MODES[m]))
            written.append(write(BRAND / f"lockup-{name}-{m}.svg", svg(title, body, view, size)))
        body = mono(MARKS[name]) + paint(text, mono_colours)
        written.append(write(BRAND / f"lockup-{name}-mono.svg",
                             svg(title, body, view, size, f' color="{MODES["light"]["fg"]}"')))
    return written


# ---------- Bitmaps ----------
def png(svg_text, px, path):
    with tempfile.NamedTemporaryFile("w", suffix=".svg", delete=False) as f:
        f.write(svg_text)
    subprocess.run(["rsvg-convert", "-w", str(px), "-h", str(px), f.name, "-o", str(path)], check=True)
    pathlib.Path(f.name).unlink()
    return path


def ico(svg_text, path, sizes=(16, 32, 48)):
    """An ICO holding PNG images (supported by every browser that still asks for favicon.ico)."""
    with tempfile.TemporaryDirectory() as d:
        images = [png(svg_text, s, pathlib.Path(d) / f"{s}.png").read_bytes() for s in sizes]
    header = struct.pack("<HHH", 0, 1, len(images))
    offset, entries = 6 + 16 * len(images), b""
    for s, data in zip(sizes, images):
        entries += struct.pack("<BBBBHHII", s % 256, s % 256, 0, 0, 1, 32, len(data), offset)
        offset += len(data)
    path.write_bytes(header + entries + b"".join(images))
    return path


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--font", help="Space Grotesk variable font (.ttf), to rebuild the wordmarks and lockups")
    args = ap.parse_args()
    written = []

    for name, fn in MARKS.items():
        for m in MODES:
            written.append(write(BRAND / f"{name}-{m}.svg", svg(TITLES[name], paint(tile(fn), MODES[m]))))
        written.append(write(BRAND / f"{name}-mono.svg",
                             svg(TITLES[name], mono(fn), extra=f' color="{MODES["light"]["fg"]}"')))

    # Favicon: light and dark by the device setting. Classes, not CSS variables: some renderers
    # (rsvg, older tools) don't support var() in SVG.
    small_tile = classify(tile(small, inset=False), "")
    style = f'<style>{class_rules(MODES["light"])}@media (prefers-color-scheme:dark){{{class_rules(MODES["dark"])}}}</style>'
    favicon = svg("Roostack", style + small_tile)
    written.append(write(BRAND / "favicon.svg", favicon))
    written.append(write(STATIC / "favicon.svg", favicon))
    written.append(write(ROOT / "help" / "docs" / "assets" / "logo.svg", favicon))
    written.append(write(BRAND / "small-light.svg", svg("Roostack", paint(tile(small, inset=False), MODES["light"]))))
    written.append(write(BRAND / "small-dark.svg", svg("Roostack", paint(tile(small, inset=False), MODES["dark"]))))

    # The app's top-bar mark: classes styled with purpose tokens in app.css, so it follows the theme.
    app_svg = ('<svg class="brand-mark" viewBox="0 0 64 64" aria-hidden="true" focusable="false">'
               + classify(tile(small, inset=False), "bm-") + "</svg>")
    go = ("// Code generated by assets/brand/generate.py. DO NOT EDIT.\n\n"
          "package web\n\n"
          'import "html/template"\n\n'
          "// brandMark is Roostack's mark (a hen on a server) for the top bar. Its bm-*\n"
          "// classes are coloured with purpose tokens in app.css, so it follows the\n"
          "// user's theme. Decorative: the name beside it says what it is.\n"
          f"const brandMark template.HTML = `{app_svg}`\n")
    written.append(write(ROOT / "internal" / "web" / "brandmark.go", go))

    # Roostack Cloud's small mark and favicons (copied into Cloud's website by its
    # tools/sync-design.sh): the same files, named -cloud.
    cloud_tile = classify(tile(small_cloud, inset=False), "")
    written.append(write(BRAND / "favicon-cloud.svg", svg("Roostack Cloud", style + cloud_tile)))
    for m in MODES:
        written.append(write(BRAND / f"small-cloud-{m}.svg", svg("Roostack Cloud", paint(tile(small_cloud, inset=False), MODES[m]))))
    written.append(write(BRAND / "brandmark-cloud.svg",
                         '<svg class="brand-mark" viewBox="0 0 64 64" aria-hidden="true" focusable="false" xmlns="http://www.w3.org/2000/svg">'
                         + classify(tile(small_cloud, inset=False), "bm-") + "</svg>\n"))

    if shutil.which("rsvg-convert"):
        light_small = svg("Roostack", paint(tile(small, inset=False), MODES["light"]))
        # iOS rounds the corners itself and shows transparency as black: a square tile.
        square = svg("Roostack", paint(tile(small, radius=0, inset=False), MODES["light"]))
        written += [
            ico(light_small, BRAND / "favicon.ico"),
            png(square, 180, BRAND / "apple-touch-icon.png"),
            png(light_small, 512, BRAND / "icon-512.png"),
        ]
        light_cloud = svg("Roostack Cloud", paint(tile(small_cloud, inset=False), MODES["light"]))
        written += [
            ico(light_cloud, BRAND / "favicon-cloud.ico"),
            png(svg("Roostack Cloud", paint(tile(small_cloud, radius=0, inset=False), MODES["light"])), 180,
                BRAND / "apple-touch-icon-cloud.png"),
        ]
        shutil.copy(BRAND / "favicon.ico", STATIC / "favicon.ico")
        shutil.copy(BRAND / "apple-touch-icon.png", STATIC / "apple-touch-icon.png")
        written += [STATIC / "favicon.ico", STATIC / "apple-touch-icon.png"]
    else:
        print("rsvg-convert isn't installed: PNG and ICO files not updated")

    if args.font:
        written += wordmarks(args.font)
    else:
        print("no --font: wordmarks and lockups not updated")

    for p in written:
        print(p.relative_to(ROOT))


if __name__ == "__main__":
    main()
