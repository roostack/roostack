# Sidebar re-work: mock-ups (2026-10-14)

**Chosen (2026-10-14): D, Console glass**, written up in [DESIGN.md](../../DESIGN.md) ("Layout"). These mock-ups stay as the record of what was considered.

Four directions (three, then a fourth after the Owner's first look) for moving Roostack's UI, and Roostack Cloud's portal and admin console, to a sidebar-based management layout that borrows the website's Night roost look, in light and dark. Static HTML on top of the real `tokens.css` and theme files; nothing in the app changes until the Owner chooses.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/sidebar/>. The bar at the bottom right switches the direction, the page (Home, a service, Backups, Cloud admin's accounts), light or dark, the theme (all nine built-in ones), and a phone-width preview. Choices go into the address, so a view can be linked (`?d=b&p=backups&m=light&t=nord`). On a service page, the Friendly/Technical switch flips to the files behind it.

Content is real Roostack content: a lab of three servers (nuc, mum-and-dad, cloud-box), the backup plan's 3-2-1 copies, Immich's real generated Quadlet file, and the admin console's accounts from the portal-admin mock-ups.

`fonts/` holds Space Grotesk (the website's heading font, SIL Open Font License, `fonts/OFL.txt`) for the mock-ups only.

## What they all share

- **A sidebar** with the lab's name at the top (the switcher for a person with several labs, later), the main areas, then Settings' pages as a group, and Help, Light/Dark and the person at the bottom. Servers becomes a main area (today it's under Settings); Settings' own sidebar (`area-nav`) folds into the main one. On phones the sidebar is a drawer behind a menu button (C: a tab bar instead).
- **A slim top bar** per page: where you are (breadcrumbs), a search box (later), the page's help link.
- **Wider, denser pages**: a 12-column grid up to 1560 px wide, cards side by side instead of one column; tables for lists you scan (services, servers, restores, accounts).
- **From the website**: Space Grotesk headings, status pills (a dot and a word on the status tint), the 3:02 big figure, tiles of different sizes, the Friendly/Technical switch, gradient text (once per page at most), the night rail's and tiles' faint teal-and-violet glow.
- **Text size 16 px**, 15 px in tables and the sidebar (today 17 px everywhere): the Owner's choice, 2026-10-14.

## Proposed tokens (all derived, so every theme fills them as it is)

| Token | From | Use |
|---|---|---|
| `--font-display` | Space Grotesk, then `--font-sans` | Headings |
| `--canvas` | `--bg` (light); `--bg` 62% with black (dark, as the website) | The page behind the cards |
| `--glass`, `--glass-2`, `--edge`, `--edge-strong` | `--fg` at 4/7/10/17% | Translucent insets and edges over a glow |
| `--glow-a`, `--glow-b` | `--accent`, `--highlight` at 12–22% | The faint glow at the top of a page |
| `--lift` | a shadow per mode | Cards on the canvas |
| `--nav-bg`, `--nav-fg`, `--nav-muted`, `--nav-border`, `--nav-accent`, `--nav-highlight(-soft)`, `--nav-ok/warn/bad` | the theme's **dark** palette in both modes (`--pal-*-d`, picked in `tokens.css` like other purpose tokens) | Direction B's night rail |
| `--text-xs` | 0.78rem | Pills, table headings, eyebrows |

No theme file changes. The contrast test (`TestThemesAreReadable`) would gain the night rail's pairs: measured in the mock-up for all nine themes, its muted text is 6.5:1 or more on the rail and its highlight 4.2:1 or more (Solarized lowest); the deeper dark canvas only raises contrast.

## A. Console

Quiet and conventional, like most admin platforms.

- **Layout and navigation:** a solid sidebar in the page's surface colour with a border; where you are is the highlight tint with a highlight bar on its edge (the design system's "where you are" rule).
- **Density:** the densest. Home is a strip of four figures, then the services table full width, then servers (with disk bars) and recent activity side by side.
- **Colour and the website:** least of it: Space Grotesk, pills, one gradient phrase on Home. No glow, no glass.
- **Tokens and components:** `--font-display`, `--text-xs`, `--canvas`. New components: sidebar, top bar, stat strip, table list, status pill, Friendly/Technical switch.
- **Themes:** every theme works with no change; it's closest to today, so the least can go wrong.
- **Accessibility:** solid surfaces only, so contrast is exactly what the test checks today. Focus is the existing ring. Only the installing pill's dot pulses (off with reduced motion).

## B. Night rail (recommended before D)

The website's night carried into the app as a dark sidebar in both modes, with the content light or dark as the person chooses.

- **Layout and navigation:** the sidebar is always the theme's dark palette, a little deeper, with the website's teal-and-violet glow at its top; the current page is a lighter row with its icon in the highlight. The content area has a faint glow at the top of each page.
- **Density:** between A and C. Home is tiles of different sizes (services as a grid of small tiles, the 3:02 backups tile, servers with disk bars, activity); tables where a list is scanned (restoring, accounts).
- **Colour and the website:** the night rail, the glow, pill buttons, the big figure, gradient text once on Home, translucent insets (`--glass`) inside cards. Cards themselves are solid, so text always sits on a known colour.
- **Tokens and components:** A's plus `--glass*`, `--edge*`, `--glow-*`, `--lift` and the `--nav-*` set. Same new components as A, plus the tile grid.
- **Themes:** every theme gets a night rail from its own dark palette (Gruvbox's warm brown, Nord's polar night, Catppuccin's Mocha…), so it keeps its character. The contrast test adds the rail's text, muted text, highlight and status colours on `--nav-bg`.
- **Accessibility:** contrast as above; the rail's focus ring uses the rail's highlight. The glow is static (no animation). It reads as a brand, as the website does, but stays a work tool.

## C. Glass roost

Closest to the website: the whole app floats on its slow aurora.

- **Layout and navigation:** a floating, rounded glass sidebar inset from the window's edges; the current page is a solid text-coloured pill (the website's buttons). On phones, a floating tab bar at the bottom (Home, Services, Backups, Servers, More) instead of a menu button.
- **Density:** the least: bigger headings (2.4 rem), more padding, 24 px corners. Same tiles as B.
- **Colour and the website:** all of it: the moving aurora behind everything (in light mode as a pale wash, like the site's Daylight mock-up), translucent cards with blur, text-coloured pill buttons, large gradient headline.
- **Tokens and components:** as B, but no `--nav-*`; cards use `--surface` at 62% with a blur.
- **Themes:** works in all, but translucent cards over a moving glow mean the contrast test can't check what text sits on; it would have to test against the worst case (surface mixed with the strongest glow colour), and some themes' muted text would need nudging.
- **Accessibility:** the weakest: contrast varies with what's behind; the aurora moves (off with reduced motion); `backdrop-filter` on every card is slow on old phones and small NUC-class hardware browsers, and repaints visibly while scrolling.

## D. Console glass (added 2026-10-14, after the Owner's first look)

The Owner liked A most but found it a little plain, and C too much (especially its padding): D is A's structure and density with some of C's glass.

- **Layout and navigation:** A's: the same sidebar, figures strip, tables and grid. The page header sits on the page as in C (no band, no border), and the side padding is 24 px (A's is 32). The sidebar floats 8 px from the window's edge with 16 px corners, see-through (the surface at 70%) with a blur; where you are is the highlight tint with a faint highlight edge.
- **Density:** as A.
- **Colour and the website:** C's aurora behind everything, but fainter (about two thirds of C's strength; light mode a pale wash) and slower; cards are the surface at 80% with a blur, so the glow tints them just a little; glass insets and buttons (`--glass`, `--edge`); a gradient avatar; the gradient phrase on Home. Corners 16 px (cards), 10 px (buttons).
- **Tokens and components:** A's plus `--glass*`, `--edge*`, `--lift`, and the aurora (three blurred circles in `--accent`, `--highlight`, `--chart-1`). No `--nav-*`.
- **Themes:** every theme as it is. The contrast test has to check text on the worst case: a card (or the sidebar) with the strongest glow colour behind it. Measured in the mock-up for all nine themes in both modes: muted text, links and warning text on a card are 4.54:1 or more (Nord light lowest); on the sidebar 4.5:1 or more at 62% surface, which is why it's 70%.
- **Accessibility:** contrast as above, testable; the aurora moves slowly and stops with reduced motion; `backdrop-filter` on the sidebar, top bar and cards costs less than C (fewer, mostly opaque layers) but is still worth checking on a slow phone. Browsers without `color-mix` or `backdrop-filter` get the solid surface.

## Recommendation (before D)

**B, Night rail.** It gives the app the website's identity in both modes (the night rail is the brand, always there) while the content stays solid, readable and dense enough for a management tool. Every theme fills it from its own dark palette with no change, and it's testable with today's contrast test plus one set of pairs. A is the safe fallback; C is better kept for the website and marketing moments (the Cloud portal's sign-in page, say).

## The Owner's answers (2026-10-14)

1. **Direction:** D, Console glass, with C's header style and a little less side padding than A (both done here).
2. **Text size:** 16 px body text, 15 px in tables and the sidebar (was 17 px).
3. **Space Grotesk:** yes, vendored into `internal/web/static` (OFL licence, 22 KB for Latin).
4. **Navigation:** as mocked: Servers as a main area, Settings' pages in the sidebar, the overview kept as "All settings".
5. **Search** in the header: still open (shown as a placeholder).
