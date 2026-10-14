#!/usr/bin/env python3
"""Roostack's error scenes (SPEC.md §6.3.4): the small mark's
hen and server (or Roostack Cloud's cloud), without the tile, on a soft
wash, with props. Writes internal/errpage/scenes.go and a standalone SVG of
each here. Run it from the repository's root after changing a scene:

    python3 assets/errors/generate.py

Colours are classes (es-fg, es-hi, ...), set by the page's CSS from the
theme's purpose tokens, so the scenes follow the user's theme; the
standalone SVGs carry the default theme's colours, light and dark.
"""
import html
import re
import subprocess

# The small mark's hen (64-unit grid), placed into a 160x100 scene at (48, 22).
HEN = '''<g transform="translate(-3.6 .6) scale(1.2)">
<path class="hi" d="M34.5 14C33 11 34 8.2 36.3 8.6C36.6 6.2 39.6 5.6 40.4 7.8C42.2 6.9 44.2 8.6 43.2 11.2C42.8 12.4 42 13.4 41 14Z"/>
<path class="hi" d="M43.4 18C45.8 18.4 46.6 21.8 45.2 24.2C43.4 23.6 42.4 20.4 43.4 18Z"/>
<path class="fg" d="M44.2 14.6 49.2 16.6 44.4 18.6Z"/>
<path class="fg bgline" d="M24 35C30 37.5 38 36 41 30C43 26 43.5 23 42.6 20.6L44.6 18.6C44.9 12.6 42.2 10.4 39 10.4C35.8 10.4 33.4 12.8 33.4 16C33.4 18.5 32.4 20 30 21C26 22.6 22 22.6 19.6 21.4C17 18 14.5 14.5 11.6 12.6C11.2 15.6 12 18 13.4 19.8C11.6 19 9.6 18.8 8 19.4C9.6 21.4 11.6 22.6 13.6 23.4C12.2 23.8 10.8 24.6 10 25.6C12 26.6 14 26.8 15.4 26.6C17 31 20 34 24 35Z" stroke-width="1.1" stroke-linejoin="round"/>
%EYE%
<path class="fgline" d="M27 34V37.4M25.4 37.4H29" fill="none" stroke-width="1.7" stroke-linecap="round"/>
<path class="fgline" d="M31.4 34V37.4M29.8 37.4H33.4" fill="none" stroke-width="1.7" stroke-linecap="round"/>
%EXTRA%
</g>'''
OPEN = '<circle class="bg" cx="40.2" cy="14.6" r="1.3"/>'
SHUT = '<path class="bgline" d="M38.8 14.4Q40.2 15.8 41.6 14.4" fill="none" stroke-width="1" stroke-linecap="round"/>'
NIGHTCAP = ('<path class="hi" d="M33.6 12.4C33.4 7 37.4 4 41.4 4.6C38 4.2 33.8 6 31 9.6C29 12 27.4 15.4 27.6 18.2C29.6 15.6 31.4 13.6 33.6 12.4Z"/>'
            '<path class="hi" d="M33.6 12.4C36.4 10.4 41.6 9.8 45.6 11.2C45.8 7.4 44 4.8 41.4 4.6"/>'
            '<path class="bgline" d="M36.6 6.8C35 8.4 33.8 10 33.4 12M40.2 5.4C38.6 6.6 37.4 8.4 37 10.6" fill="none" stroke-width=".9" stroke-linecap="round" opacity=".5"/>'
            '<path class="hi" d="M33.2 11.8C37 10 42 9.6 46.2 11L46 12.8C42 11.6 37.4 11.8 33.6 13.6Z" opacity=".75"/>'
            '<circle class="warn" cx="27.6" cy="18.6" r="1.9"/>')
def hen(eye=OPEN, extra='', x=48, y=22, s=1, flip=False, rot=0):
    t = f'translate({x} {y}) rotate({rot} 32 40) scale({s})'
    if flip:
        t += ' translate(64 0) scale(-1 1)'
    return f'<g transform="{t}">' + HEN.replace('%EYE%', eye).replace('%EXTRA%', extra) + '</g>'
def server(light='ok'):
    vents = ''.join(f'<rect class="bg" x="{33+i*4}" y="49.6" width="1.6" height="5.3" rx=".8" opacity=".45"/>' for i in range(5))
    return ('<ellipse class="shadow" cx="80" cy="81" rx="30" ry="2.6"/>'
            f'<g transform="translate(48 22)"><rect class="fg" x="8" y="46.5" width="48" height="11.5" rx="3.2"/>'
            f'<circle class="{light}" cx="15" cy="52.25" r="2.8"/><circle class="bg" cx="22" cy="52.25" r="1.6" opacity=".45"/>{vents}</g>')
def cloud(light='ok'):
    return ('<g transform="translate(48 22)"><rect class="hi" x="5" y="47.5" width="54" height="11.5" rx="5.75"/><circle class="hi" cx="17" cy="48.6" r="6.2"/>'
            f'<circle class="hi" cx="46" cy="47.6" r="7.6"/><circle class="{light}" cx="13.5" cy="53.25" r="2.8"/></g>')
SCENE = '<svg viewBox="0 0 160 100" class="scene" aria-hidden="true" focusable="false">' + \
    '<defs><radialGradient id="@ID@"><stop class="wash" offset="0" stop-opacity=".95"/>' + \
    '<stop class="wash" offset=".65" stop-opacity=".55"/><stop class="wash" offset="1" stop-opacity="0"/></radialGradient></defs>' + \
    '<ellipse cx="80" cy="56" rx="78" ry="44" fill="url(#@ID@)"/>'
def scene(*parts):
    # The wash's ID is made unique where it's placed (place()): SVGs on one
    # page share IDs, and each must use its own theme's colour.
    return SCENE + ''.join(parts) + '</svg>'
_n = [0]
def place(svg):
    _n[0] += 1
    return svg.replace('@ID@', f'wash{_n[0]}')
def ground(): return '<path class="dimline" d="M14 82.5H146" fill="none" stroke-width="1.2" stroke-linecap="round" stroke-dasharray="1 4"/>'
def txt(x, y, s, size, cls='fg', rot=0):
    return f'<text class="{cls} txt" x="{x}" y="{y}" font-size="{size}" transform="rotate({rot} {x} {y})">{html.escape(s)}</text>'
def feather(x, y, rot=0, s=1):
    return (f'<g transform="translate({x} {y}) rotate({rot}) scale({s})"><path class="hi" d="M0 0c5-6 11-7 13-5-1 5-7 9-13 9z"/>'
            '<path class="bgline" d="M.6 3.4 10 -4" fill="none" stroke-width=".8"/></g>')
def footprints(x, y, n=4):
    out = ''
    for i in range(n):
        fx, fy = x + i * 11, y + (i % 2) * 3
        out += (f'<path class="dimline" d="M{fx} {fy}l3 -2.6M{fx} {fy}l3.6 0M{fx} {fy}l3 2.6M{fx} {fy}h-2.2" fill="none" '
                'stroke-width="1.1" stroke-linecap="round"/>')
    return out
def smoke(x, y):
    return ''.join(f'<circle class="dim" cx="{x+dx}" cy="{y+dy}" r="{r}"/>' for dx, dy, r in [(0, 0, 3.4), (5, -7, 4.4), (1, -15, 3.6), (7, -21, 2.6)])
def eggshells():
    return ('<path class="fg" d="M64 68.5c0-6 3-10.5 6.5-10.5l1.6 3 2.4-2.4 1.6 3 2.2-2.2c2.4 1.8 3.2 5 3.2 9.1z" transform="rotate(-14 71 64)"/>'
            '<path class="fg" d="M91 68.5c0-4.4 2-7.4 4.4-8l.9 2.2 1.6-1.6 1.4 2.2 1.5-1.5c1.6 1.6 1.8 4 1.8 6.7z" transform="rotate(18 96 65)"/>'
            '<path class="warn" d="M74 68.5h16c1.5 0 1.5 1.6 0 2.2-2 .8-2.4 1.6-2.4 3 0 1.6-1.6 1.6-1.6 0 0-1.4-1-2.2-3-2.2h-9c-2 0-2-3 0-3z"/>'
            '<ellipse class="warn" cx="82" cy="66.8" rx="5" ry="2.4"/>')
def extinguisher(x, y):
    # A cylinder with a domed top, a label band and a shine, the valve and
    # lever on top, and a hose curling down to its nozzle.
    return (f'<g transform="translate({x} {y})">'
            '<path class="bad" d="M0 9a5 5 0 0 1 10 0v15.5a2 2 0 0 1-2 2H2a2 2 0 0 1-2-2z"/>'
            '<rect class="bg" x="0" y="14" width="10" height="4.4" opacity=".9"/><rect class="bad" x="2" y="15.2" width="6" height="2" rx=".6" opacity=".7"/>'
            '<path class="bgline" d="M2.4 7.4v14" stroke-width="1" stroke-linecap="round" opacity=".55"/>'
            '<rect class="fg" x="3.4" y="1.6" width="3.2" height="3.4" rx=".6"/>'
            '<path class="fgline" d="M3.6 2.2-1.4.4M6.4 2.2 11 0" fill="none" stroke-width="1.3" stroke-linecap="round"/>'
            '<path class="fgline" d="M6.6 4c4 0 6.5 2 6.5 6s-1 8 1.2 10" fill="none" stroke-width="1.2" stroke-linecap="round"/>'
            '<path class="fg" d="M13.4 19.4l2.6.6-.6 3.2-2.6-.6z"/></g>')
def sparks(x, y):
    return ''.join(f'<path class="warnline" d="M{x+dx} {y+dy}l{ex} {ey}" stroke-width="1.3" stroke-linecap="round"/>'
                   for dx, dy, ex, ey in [(0, 0, -3, -3), (-2, 5, -4, 0), (1, -5, -1, -4)])
def lock(x, y):
    return (f'<g transform="translate({x} {y})"><path class="hiline" d="M2 7V4.5a4 4 0 0 1 8 0V7" fill="none" stroke-width="2"/>'
            '<rect class="hi" x="0" y="6.5" width="12" height="10" rx="2.2"/><circle class="bg" cx="6" cy="11" r="1.4"/><rect class="bg" x="5.4" y="11" width="1.2" height="3"/></g>')
def locked_egg(x, y):
    # An egg (the small mark's) with a strap round it and a padlock on the
    # strap: the hen's, and not for you.
    return (f'<g transform="translate({x} {y})"><ellipse class="fg" cx="0" cy="-12" rx="8.6" ry="11"/>'
            '<path class="bgline" d="M-5 -19.5c1.2-2.4 3-3.6 4.4-3.8" fill="none" stroke-width="1.2" stroke-linecap="round" opacity=".55"/>'
            '<path class="hi" d="M-8.5 -11.4c5.6 2 11.4 2 17 0v2.6c-5.6 2-11.4 2-17 0z"/>'
            '<path class="hiline" d="M-2.6 -6V-8.6a2.6 2.6 0 0 1 5.2 0V-6" fill="none" stroke-width="1.6"/>'
            '<rect class="hi" x="-4.2" y="-6.4" width="8.4" height="7.4" rx="1.6"/><circle class="bg" cx="0" cy="-3.4" r="1.1"/>'
            '<rect class="bg" x="-.5" y="-3.4" width="1" height="2.4"/></g>')
def ladder(x, y):
    rungs = ''.join(f'<path class="dimline" d="M{x} {y+i*7}h10" stroke-width="1.4"/>' for i in range(6))
    return f'<path class="dimline" d="M{x} {y-3}v42M{x+10} {y-3}v42" fill="none" stroke-width="1.6"/>' + rungs
def sign(x, y, word):
    return (f'<path class="fgline" d="M{x+12} {y+9}V{y+29.5}" fill="none" stroke-width="1.6"/><rect class="hi" x="{x}" y="{y}" width="24" height="10" rx="1.6"/>'
            + txt(x + 12, y + 7.2, word, 5.4, 'bg').replace('class="bg txt"', 'class="bg txt" text-anchor="middle"'))
def nest(x, y):
    # Woven straw, like the brand's nest mark, with nobody in it.
    weave = ''.join(f'<path class="bgline" d="M{x+dx} {y-6.4}l{1.6 if i%2 else -1.6} 3.6" stroke-width=".9" stroke-linecap="round" opacity=".5"/>'
                    for i, dx in enumerate([-9, -4.5, 0, 4.5, 9]))
    straws = ''.join(f'<path class="fgline" d="M{x+a} {y-8}l{b} {d}" stroke-width="1.1" stroke-linecap="round"/>'
                     for a, b, d in [(-13, -3.4, -2.6), (-9, -1.6, -4), (10, 2.6, -3.6), (13, 3.6, -1.6), (2, .4, -3.2)])
    return (straws + f'<path class="fg" d="M{x-14} {y-7}h28c0 4.4-4 7-14 7s-14-2.6-14-7z"/>'
            f'<path class="fgline" d="M{x-12} {y-7.6}c2-1.6 22-1.6 24 0" fill="none" stroke-width="2.6" stroke-linecap="round"/>' + weave)
def poster(x, y):
    # A "missing" poster on a post: the hen's silhouette, a reward line.
    return (f'<path class="fgline" d="M{x+11} {y+17}V{y+42.5}" fill="none" stroke-width="1.8" stroke-linecap="round"/>'
            f'<g transform="rotate(4 {x+11} {y+8})"><rect class="bg fgline" x="{x}" y="{y-6}" width="22" height="26" rx="1.2" stroke-width="1"/>'
            + txt(x + 11, y - .8, 'MISSING', 3.8, 'fg').replace('class="fg txt"', 'class="fg txt" text-anchor="middle"')
            + hen(x=x + 4.2, y=y - 1.2, s=.21)
            + f'<path class="dimline" d="M{x+4} {y+15.6}h14M{x+6} {y+18}h10" stroke-width="1" stroke-linecap="round"/>'
            f'<circle class="bad" cx="{x+11}" cy="{y-5}" r="1.1"/></g>')
def cobweb(x, y):
    return (f'<g transform="translate({x} {y})" class="dimline" fill="none" stroke-width=".7"><path d="M0 0 14 0M0 0 0 14M0 0 11 11M0 0 13 6M0 0 6 13"/>'
            '<path d="M4 0Q4 4 0 4M8 0Q8 8 0 8M12 0Q12 12 0 12"/></g>')
def tumbleweed(x, y):
    return (f'<g transform="translate({x} {y})" class="dimline" fill="none" stroke-width="1"><circle r="6"/><path d="M-5 -3C-1 0 2 -4 5 1M-4 3C0 1 2 5 5 -2M-2 -5C0 0 -3 3 2 5"/></g>'
            f'<path class="dimline" d="M{x-14} {y+6}h-6M{x-10} {y+2}h-5" fill="none" stroke-width="1" stroke-linecap="round"/>')
def moon(x, y):
    return f'<path class="hi" d="M{x} {y}a9 9 0 1 0 9 11 7 7 0 0 1-9-11z"/>'
def star(x, y, s=1, cls='hi'):
    return f'<path class="{cls}" d="M{x} {y-3*s}l{.9*s} {2.1*s} {2.1*s} {.9*s}-{2.1*s} {.9*s}-{.9*s} {2.1*s}-{.9*s}-{2.1*s}-{2.1*s}-{.9*s} {2.1*s}-{.9*s}z"/>'
def zz(x, y):
    return txt(x, y, 'z', 8, 'hi') + txt(x + 6, y - 6, 'z', 6, 'hi') + txt(x + 11, y - 11, 'z', 4.6, 'hi')
def egg_hatching():
    return ('<g transform="translate(48 22)"><ellipse class="fg" cx="32" cy="35" rx="9.5" ry="12"/>'
            '<path class="bgline" d="M23 33 26.6 30.2 29.4 34 32.6 30.4 35.8 34 38.6 30.4 41 33" fill="none" stroke-width="1.5" stroke-linejoin="round"/>'
            '<path class="hi" d="M30.4 25.8 33.8 23.6 32.8 27Z"/></g>')
def cone(x, y):
    return (f'<g transform="translate({x} {y})"><path class="warn" d="M5 0 10 16H0z"/><rect class="bg" x="2.4" y="6" width="5.2" height="2.4"/>'
            '<rect class="fg" x="-2" y="15.5" width="14" height="2.4" rx="1"/></g>')
def lamp(px, y, c=80):
    # A heat lamp on a stand beside the server (pole at px), its arm
    # reaching over so the dome hangs above the egg at c, a warm glow
    # falling on it.
    return ('<ellipse class="warn" cx="80" cy="56" rx="16" ry="18" opacity=".18"/>'
            f'<rect class="fg" x="{px-6}" y="{y+56}" width="12" height="2.6" rx="1.3"/>'
            f'<path class="fgline" d="M{px} {y+56}V{y+4}q0-6 6-6H{c-2}" fill="none" stroke-width="1.8" stroke-linecap="round"/>'
            f'<path class="fg" d="M{c-6} {y-2.4}h12c4 0 6.6 3.6 6.6 8.8H{c-12.6}c0-5.2 2.6-8.8 6.6-8.8z"/>'
            f'<rect class="fg" x="{c-2}" y="{y-4.6}" width="4" height="2.6" rx=".8"/>'
            f'<path class="warn" d="M{c-4.4} {y+6.4}a4.4 3.2 0 0 0 8.8 0z"/>'
            + ''.join(f'<path class="warnline" d="M{c+dx} {y+11.4}l{dx*.5} 4" stroke-width="1.2" stroke-linecap="round" opacity=".8"/>' for dx in (-5, 0, 5)))
def hourglass(x, y):
    return (f'<g transform="translate({x} {y})"><rect class="fg" x="-1" y="-1" width="12" height="2" rx="1"/><rect class="fg" x="-1" y="17" width="12" height="2" rx="1"/>'
            '<path class="hi" d="M0 1h10l-5 8 5 8H0l5-8z"/><path class="warn" d="M2.6 16h4.8L5 12.4z"/></g>')
def snail(x, y):
    return (f'<g transform="translate({x} {y})"><path class="fg" d="M-2 9h22c0-3-3-4-6-4"/><circle class="hi" cx="8" cy="3" r="6"/>'
            '<path class="bgline" d="M8 3m-3 0a3 3 0 1 0 3-3" fill="none" stroke-width="1.1"/>'
            '<path class="fgline" d="M18 5l2-5M20 5l3-4" fill="none" stroke-width="1" stroke-linecap="round"/></g>'
            f'<path class="dimline" d="M{x-16} {y+9.5}h12" fill="none" stroke-width="1" stroke-dasharray="1 2"/>')
def snail_on_server():
    # Standing in for the hen on the server: a snail wearing its comb, a
    # slime trail behind it.
    return ('<path class="dimline" d="M60 67.8h18" stroke-width="1.4" stroke-linecap="round" stroke-dasharray="2 2.6"/>'
            '<g transform="translate(78 47)">'
            '<path class="fg" d="M-2 20.8h30c1.6 0 2.4-1.4 1.4-2.6l-2.2-2.6c-1-1.2-3-1.6-4.4-.8z"/>'
            '<path class="fgline" d="M23 16c.6-4 2-7 4-8.4M25.6 16.4c1.2-3.2 3-5.4 5-6.2" fill="none" stroke-width="1.6" stroke-linecap="round"/>'
            '<circle class="fg" cx="27" cy="7.4" r="1.6"/><circle class="fg" cx="30.6" cy="10" r="1.6"/>'
            '<circle class="hi" cx="10.4" cy="10.6" r="10"/>'
            '<path class="bgline" d="M10.4 10.6m-3.8 0a3.8 3.8 0 1 0 3.8-3.8a6.6 6.6 0 1 0 6.6 6.6" fill="none" stroke-width="1.3" stroke-linecap="round" opacity=".7"/>'
            '<path class="hi" d="M19.8 15.6c.4-2 1.6-3.2 2.6-3 .2-1.6 2-1.8 2.4-.6 1.2-.4 2.2.6 1.6 2-.2.8-.8 1.4-1.4 1.6z"/>'
            '</g>')
def broom(x, y):
    return (f'<g transform="translate({x} {y}) rotate(28)"><path class="fgline" d="M0 0v26" fill="none" stroke-width="1.8" stroke-linecap="round"/>'
            '<path class="warn" d="M-5 26h10l2 10H-7z"/><path class="fgline" d="M-3 29v6M0 29v6M3 29v6" fill="none" stroke-width=".7"/></g>')
def crowd():
    # Too many hens on one server: squeezed shoulder to shoulder, one
    # tipping off the end, one perched on top.
    return (hen(x=33, y=37, s=.68, flip=True) + hen(x=52, y=37, s=.68) + hen(x=66, y=36.4, s=.7, flip=True)
            + hen(x=83, y=37, s=.68) + hen(x=56, y=17, s=.62) + hen(x=104, y=40, s=.62, rot=38)
            + txt(122, 34, '!', 9, 'hi'))
def noentry(x, y): return f'<circle class="bad" cx="{x}" cy="{y}" r="7"/><rect class="bg" x="{x-4.6}" y="{y-1.2}" width="9.2" height="2.4" rx="1.2"/>'
def plug_dangle():
    return ('<path class="fgline" d="M100 74c6 6 4 14 10 16" fill="none" stroke-width="1.6" stroke-linecap="round"/>'
            '<g transform="translate(108 88) rotate(-20)"><rect class="fg" x="0" y="0" width="7" height="6" rx="1.5"/>'
            '<path class="fgline" d="M7 1.5h3M7 4.5h3" stroke-width="1.3" stroke-linecap="round"/></g>'
            '<path class="dimline" d="M128 84c-3 0-4 4-7 4" fill="none" stroke-width="1.6" stroke-linecap="round"/>'
            '<g transform="translate(130 80)"><rect class="dim" x="0" y="0" width="8" height="7" rx="1.5"/></g>'
            '<path class="badline" d="M116 74l4 4m0-4-4 4" stroke-width="1.6" stroke-linecap="round"/>')
def birds(x, y):
    return ''.join(f'<path class="dimline" d="M{x+dx} {y+dy}q3-3 6 0q3-3 6 0" fill="none" stroke-width="1.3" stroke-linecap="round"/>'
                   for dx, dy in [(0, 0), (14, -8), (26, 2)])
def question(x, y): return txt(x, y, '?', 20, 'fg')


# Each scene: its name (the Go key and file name), and its drawing.
SCENES = {
    "flown": scene(ground(), server(), feather(52, 22, 25), feather(104, 12, -30, .8), feather(30, 50, 60, .7), footprints(118, 78), question(73, 60)),
    "scrambled": scene(ground(), server('bad'), eggshells(), smoke(102, 54), sparks(60, 58), extinguisher(110, 55)),
    "pecking": scene(ground(), server(), hen(x=30, y=22), locked_egg(103, 68)),
    "vacant": scene(ground(), server('off'), nest(80, 68.5), feather(84, 59, -10, .55), poster(110, 40)),
    "asleep": scene(ground(), moon(30, 14), star(52, 12, .8), star(124, 22, 1), star(140, 10, .7), server('warn'), hen(SHUT, NIGHTCAP), zz(108, 30)),
    "hatching": scene(ground(), lamp(46, 24), server('warn'), egg_hatching(), cone(118, 64)),
    "slow": scene(ground(), server('warn'), snail_on_server(), hourglass(118, 42)),
    "crowded": scene(ground(), server(), crowd(), feather(40, 26, -20, .7), feather(118, 18, 30, .6), feather(132, 40, 70, .5)),
    # Roostack Cloud's (its pages are built in its own repository).
    "cloud-asleep": scene(moon(30, 14), star(52, 10, .8), star(128, 18, 1), cloud('warn'), hen(SHUT, NIGHTCAP), zz(108, 30)),
    "cloud-empty": scene(cloud('off'), birds(104, 30), question(73, 62), feather(46, 26, 20, .8)),
    "cloud-unplugged": scene(cloud(), hen(), plug_dangle()),
}

def prefixed(svg):
    """Classes get an es- prefix, so they can't clash with a page's."""
    def one(m):
        return 'class="' + ' '.join('es-' + c for c in m.group(1).split()) + '"'
    return re.sub(r'class="([a-z ]+)"', one, svg)

# The default theme's colours (themes/teal.css and tokens.css), for the
# standalone SVGs and Caddy's error pages, light then dark.
COLOURS = {
    "light": {"wash": "#ede9fe", "bg": "#ffffff", "fg": "#1b2426", "hi": "#7c3aed", "ok": "#7ee2ae", "warn": "#f5c16c",
              "bad": "#b3261e", "off": "#56676a"},
    "dark": {"wash": "#2a1e47", "bg": "#1a2021", "fg": "#e5eceb", "hi": "#c4b5fd", "ok": "#17603f", "warn": "#f5c16c",
             "bad": "#ff8a80", "off": "#98a8a8"},
}

def css(c):
    """The scenes' classes for one set of colours."""
    return (f".es-wash{{stop-color:{c['wash']}}}.es-bg{{fill:{c['bg']}}}.es-fg{{fill:{c['fg']}}}.es-hi{{fill:{c['hi']}}}"
            f".es-ok{{fill:{c['ok']}}}.es-warn{{fill:{c['warn']}}}.es-bad{{fill:{c['bad']}}}.es-off,.es-dimfill{{fill:{c['off']}}}"
            f".es-dim{{fill:{c['off']};opacity:.5}}.es-shadow{{fill:{c['fg']};opacity:.08}}.es-bgline{{stroke:{c['bg']}}}"
            f".es-fgline{{stroke:{c['fg']}}}.es-hiline{{stroke:{c['hi']}}}.es-badline{{stroke:{c['bad']}}}"
            f".es-warnline{{stroke:{c['warn']}}}.es-dimline{{stroke:{c['off']};opacity:.7}}")

TXT = ".es-txt{font-family:system-ui,sans-serif;font-weight:800}"

def main():
    go = ['// Code generated by assets/errors/generate.py. DO NOT EDIT.', '', 'package errpage', '',
          '// scenes are the error scenes\' SVGs, by name. "@ID@" is replaced by a', '// unique ID where one is shown (Scene).',
          'var scenes = map[string]string{']
    for name, svg in SCENES.items():
        svg = prefixed(svg)
        assert '`' not in svg
        go.append(f'\t"{name}": `{svg}`,')
        standalone = svg.replace('<svg ', '<svg xmlns="http://www.w3.org/2000/svg" width="320" height="200" ', 1).replace('@ID@', 'wash')
        style = (f"<style>{TXT}{css(COLOURS['light'])}@media (prefers-color-scheme: dark){{{css(COLOURS['dark'])}}}</style>")
        standalone = standalone.replace('<defs>', style + '<defs>', 1)
        open(f'assets/errors/{name}.svg', 'w').write(standalone + '\n')
    go.append('}')
    go += ['', '// sceneCSS is the scenes\' classes in the default theme\'s colours, light and', '// dark (Caddy\'s pages can\'t know the user\'s theme).',
           f'const sceneCSS = `{TXT}{css(COLOURS["light"])}@media (prefers-color-scheme: dark){{{css(COLOURS["dark"])}}}`', '']
    open('internal/errpage/scenes.go', 'w').write('\n'.join(go))
    subprocess.run(['gofmt', '-w', 'internal/errpage/scenes.go'], check=True)

if __name__ == '__main__':
    main()
