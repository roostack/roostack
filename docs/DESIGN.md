# Roostack design system

Every page is built from the tokens and components here. The gallery shows them all, in light and dark side by side:

- In Roostack: **/design** (linked in the footer of every page).
- In the repo: [docs/design/gallery.html](design/gallery.html). Open it straight in a browser; it links the real CSS files. It's generated from the same templates as `/design`, and a test fails if it's out of date. Regenerate it with:

  ```sh
  go test ./internal/web -run TestGalleryFile -update-gallery
  ```

**Rule:** pages only use what's in the gallery. If a page needs something new, add it to the design system first (tokens, component, gallery example), then use it.

## Files

| File | What it holds |
|---|---|
| `internal/web/static/css/tokens.css` | Scales (spacing, type, radius, shadow), status colours, and how the current mode picks purpose tokens from the palette |
| `internal/web/static/css/themes/<name>.css` | A built-in theme: its raw colours for light and dark |
| `internal/web/themes.go` | The list of built-in themes (names shown to users) |
| `internal/web/static/css/app.css` | Base styles, components and page layouts. Uses purpose tokens and scales only |
| `internal/web/static/css/gallery.css` | Styles for the gallery page itself |
| `internal/web/templates/components.html` | Templates of components with shared markup (notice, status, badge, server, back, confirm, activity, install steps, project credit) |
| `internal/web/icons.go` | The icons (`icon NAME`), drawn for Roostack |
| `internal/web/static/app.js` | Roostack's own small script for self-refreshing parts: sends the fingerprint of what a part shows (the server answers 204 when it's unchanged, `renderLive`), and keeps open `<details>` (with an `id`) open when a part does change. Give a `<details>` inside a refreshing area a stable `id`; poll with `every Ns [document.visibilityState === 'visible'], visibilitychange[document.visibilityState === 'visible'] from:document` and render the part with `renderLive` |
| `internal/web/components.go` | The functions pages call for those components; they check their arguments |
| `internal/web/gallery.go` | The gallery's examples |

## Layout: Console glass (chosen 2026-10-14, not built yet)

The Owner chose the next look for Roostack's UI, and for Roostack Cloud's portal and admin console (which copy this design system with `tools/sync-design.sh`): a **sidebar-based management layout** with some of the website's Night roost glass. Mock-ups, with notes on every direction considered: [docs/mockups/sidebar/](mockups/sidebar/README.md) (direction D; open through a web server at the repo root).

**Why:** the brief (2026-10-14) asked for a more professional, administrative feel: navigation down the side rather than along the top, better use of large screens while staying good on a phone, and the website's look (glow, translucent tiles, Space Grotesk, status pills, the Friendly/Technical switch), in light and dark.

**What it is:**

- **Shell:** a sidebar floating 8 px from the window's edge (16 px corners, the surface at 70% with a blur): the lab's name at the top, then Home, Services, Backups, Servers, then a Settings group listing Settings' pages (and "All settings", the overview), then Help, Light/Dark and the person at the bottom. Where you are: the highlight tint with a faint highlight edge. On phones the sidebar is a drawer behind a menu button. Servers becomes a main area (it was under Settings), and Settings' own sidebar (`area-nav`) folds into this one.
- **Header:** each page's header sits on the page (no band, no border): breadcrumbs, the page's help link, and a search box later.
- **Pages:** a 12-column grid up to 1560 px wide with 24 px side padding (16 px on phones); cards side by side; tables for lists people scan (services, servers, restores, accounts); a strip of figures on Home.
- **Glass:** a faint aurora behind everything (three blurred circles in `--accent`, `--highlight` and `--chart-1`, slowly moving; paler in light mode); cards are the surface at 80% with a blur, so the glow only tints them; translucent insets and secondary buttons.
- **From the website:** Space Grotesk headings, status pills (a dot and a word on the status tint), the big figure (the last backup's time), gradient text at most once a page, the Friendly/Technical switch on a service's page (flipping to the files behind it).
- **Text:** 16 px body text (was 17 px), 15 px in tables and the sidebar.

**New tokens** (all derived from existing purpose tokens, so no theme file changes): `--font-display` (Space Grotesk, vendored into `internal/web/static`, OFL), `--text-xs`, `--canvas` (the page behind the cards: `--bg` in light, `--bg` 62% with black in dark, as the website), `--glass`/`--glass-2` and `--edge`/`--edge-strong` (`--fg` at 4, 7, 10, 17%), `--lift` (a card shadow per mode). **New components:** sidebar, page header with breadcrumbs, figures strip, table list, status pill (replacing `status`' look), Friendly/Technical switch, code panel; each goes into the gallery.

**Themes and accessibility:** every built-in theme works as it is. Text sits on translucent surfaces, so `TestThemesAreReadable` gains the worst case: muted text, links and status text on a card and on the sidebar with the strongest glow colour behind (the surface mixed with `--canvas` tinted by `--accent` or `--highlight` at the aurora's strength). Measured in the mock-ups for all nine themes in both modes: 4.54:1 or more on cards (Nord light lowest), and 4.5:1 on the sidebar at 62% surface, which is why it's 70%. The aurora stops for reduced motion; browsers without `color-mix` or `backdrop-filter` get solid surfaces. Focus stays `--focus-ring`.

**Considered** (all in the mock-ups): **A, Console**: a solid sidebar and the same dense pages; the Owner liked it most but found it a little plain. **B, Night rail**: the sidebar always dark, from each theme's dark palette, with solid cards (recommended first; needed a `--nav-*` token set and extra contrast pairs). **C, Glass roost**: the website as it is, floating glass everywhere, a tab bar on phones; too much, especially its padding, and its contrast can't be tested well. **D, Console glass**, A's structure with some of C's glass, was made after the Owner's first look and chosen, with C's header style and a little less side padding.

**Still to decide:** search in the header (shown as a placeholder).

## Tokens

Three layers:

1. **Palette** (`themes/<name>.css`): a theme's raw colours, `--pal-<name>-l` for light and `--pal-<name>-d` for dark, scoped to `[data-palette="<name>"]`. Every page loads all theme files (they're small); `data-palette` on `<html>` picks one, from the user's choice (Account → Appearance) or the default.
2. **Purpose** (`tokens.css`): what components use. Picked from the palette for the current mode.
3. **Scales** (`tokens.css`): `--space-1`…`--space-7`, `--text-sm`…`--text-2xl`, `--radius-sm`/`--radius`/`--radius-lg`/`--radius-pill`, `--shadow-1`, `--focus-ring`.

Purpose colours:

| Token | Use |
|---|---|
| `--bg`, `--surface`, `--surface-2` | Page, cards, insets (inputs, code) |
| `--fg`, `--muted` | Text, secondary text |
| `--border`, `--border-strong` | Borders, input borders |
| `--accent`, `--accent-fg`, `--accent-soft` | Actions and links; text on accent; info backgrounds |
| `--highlight`, `--highlight-fg`, `--highlight-soft` | The brand's spark: the brand mark (its comb on a soft-highlight tile), where you are (current page), focus rings, "New" badges. Use sparingly |
| `--ok`, `--warn`, `--bad` (+ `-bg`, and `--bad-fg`) | Status. Roostack's own themes share them, so a problem always looks like a problem; a palette may override them (`--pal-ok-l`…), as Catppuccin does |
| `--chart-1`, `--chart-2`, `--chart-3` | Chart colours for parts of a whole (the disk bar): blue, magenta, violet, in that order. The same in every theme (themes' accent and highlight are often close hues), with light and dark steps, checked with the dataviz validator against every theme's surface. Never orange, yellow, red or green (status), and always with a label beside the colour |
| `--ok-on-fg` | The status green that shows on a text-coloured shape: the brand mark's online light on its server. It's the other mode's green; a palette may set its own (`--pal-ok-on-fg-l`/`-d`), as Everforest does. Not for text |

Never use raw colours (`#…`) or palette variables (`--pal-…`) in `app.css` or templates.

### Mode

`data-mode` on `<html>` is `light` or `dark`; without it, the device's preference decides (the user's Auto setting). It also works on any element, which is how the gallery shows both at once. The two dark blocks in `tokens.css` (media query and attribute) must stay identical.

### Built-in themes

| Theme | Light / dark | Actions / highlight | Source (licence) |
|---|---|---|---|
| Teal and violet (default) | Roostack | Teal / violet | Roostack |
| Ocean and magenta | Roostack | Ocean blue / magenta | Roostack |
| Catppuccin | Latte / Mocha | Mauve / pink | [Catppuccin](https://catppuccin.com) (MIT) |
| Gruvbox | Light / dark | Orange / yellow | [Gruvbox](https://github.com/morhetz/gruvbox) (MIT) |
| Nord | Snow Storm / Polar Night | Frost blue / Aurora purple | [Nord](https://www.nordtheme.com) (MIT) |
| Solarized | Light / dark | Blue / magenta | [Solarized](https://ethanschoonover.com/solarized) (MIT) |
| Everforest | Light / dark (medium) | Green / orange | [Everforest](https://github.com/sainnhe/everforest) (MIT) |
| Rosé Pine | Dawn / Moon | Pine / rose (Roostack status colours) | [Rosé Pine](https://rosepinetheme.com) (MIT) |
| Tokyo Night | Day / Night | Blue / magenta | [Tokyo Night](https://github.com/folke/tokyonight.nvim) (Apache 2.0) |

Theme files use plain hex colours, and `TestThemesAreReadable` checks every theme in both modes against WCAG contrast: text and muted text on every surface and on notice tints (4.5:1), links (4.5:1), text on buttons (4.5:1), status text on its tint and on cards (4.5:1), the highlight (3:1, it isn't text), and the brand mark's online light on the text colour (3:1). Where a project's own colour is too faint for UI text on its own background (common in light variants), it's nudged just far enough to pass, keeping its hue; each file lists what was adjusted.

Catppuccin's flavours only differ in how dark they are, so Roostack offers one Catppuccin theme (Latte and Mocha) rather than several near-identical ones. Its identity is its named accent colours instead: users pick one for buttons and links (12 of Catppuccin's 14; red and maroon are left out because red buttons mean "danger" in Roostack), and the highlight pairs with it (pink for cool accents, mauve for warm). Latte's pastels are darkened as needed to be readable as text on Latte's pale backgrounds; Mocha's are used as they are.

### Accents

A theme may offer accent colours for actions: `Accents` in `themes.go`, and one `[data-palette="<name>"][data-accent="<accent>"]` block per accent in its file, overriding `--pal-accent-*` and `--pal-highlight-*`. `data-accent` on `<html>` comes from the user's choice. The contrast test checks every accent in both modes. Only Catppuccin has accents; if accent choice becomes a general wish, other themes can add blocks the same way.

### Adding a built-in theme

1. Copy `themes/teal.css` to `themes/<name>.css`, change the selector to `[data-palette="<name>"]`, and change the `--pal-*` values (hex only). Credit the source and licence at the top.
2. Add it to `Themes` in `internal/web/themes.go`, with a `Source` credit if it comes from an open-source palette (shown on the theme's row and the About page), then regenerate `THIRD_PARTY.md` (`go test ./internal/web -run TestThirdPartyFile -update-credits`).
3. Run `go test ./internal/web`: the contrast test tells you which colours to adjust.
4. Regenerate the gallery and compare it with the others (the gallery has a theme picker).

Nothing else changes: components only see purpose tokens.

## Components

Template components are called as functions; everything else is plain HTML with the classes below.

| Component | How | Notes |
|---|---|---|
| Buttons | `<button>` (primary), `.secondary`, `.danger`, `.quiet` (looks like a link), `.quiet.danger`; `<a class="button">` for navigation | One primary per form or card |
| Confirm | `{{confirm ACTION QUESTION TEXT KIND ["name=value"…]}}` | For anything hard to undo. KIND is the button class (`danger`, `secondary`, `quiet`, `""`); trailing `name=value` are hidden fields. The question goes through a data attribute, never into script |
| Confirm a form | `{{confirmSubmit QUESTION TEXT KIND}}` inside a form | For a form with fields of its own (checkboxes, choices); its required fields are checked before the dialog opens. KIND is the dialog button's class (`danger` or `""`) |
| Notice | `{{notice KIND TEXT ["link=…" "linkText=…"]}}` | KIND: `info`, `ok`, `warn`, `error` |
| Status | `{{status .Status}}` | Service state; `installing` gets a spinner |
| Install steps | `{{template "install-steps" .Service.Install}}` in a notice; `{{template "install-line" .}}` (a ServiceView) in lists | A new service's install (`node.InstallProgress`): `ol.install-steps` with `li.step-done` / `step-now` / `step-todo`, time taken or "for …" so far. `.address-pending` shows an address that isn't a link yet |
| Badge | `{{badge "New" ["highlight"]}}` | Highlight badges are rare |
| Back link | `{{back "/settings" "Settings"}}` | |
| Brand mark | `{{brandMark}}` | Roostack's small mark (the hen on a server), in the theme's colours through the `bm-*` classes in `app.css`. Generated into `brandmark.go` by `assets/brand/generate.py`: change it there, not by hand. Decorative; the name beside it says what it is |
| Top bar | `layout.html`: `.brand` with `{{brandMark}}`; once the lab has a name, `.brand-stack` (a small uppercase `.brand-eyebrow` "Roostack" over `.brand-lab`) | The lab's name is the title, so people know whose lab it is; long names end in "…" (full name on hover) |
| Page help | `<div class="page-top">{{back …}}{{help "settings/people" "People and access"}}</div>` above the page title | Every page with a help page: a question-mark icon and the help page's title, top right, opening in a new window; the back link or setup step sits on its left. `TestHelpLinks` checks each page and #section exists in `help/docs` |
| New window | `<a href="…" target="_blank" rel="noopener">Text{{template "new-window"}}</a>` | Every link that opens a new window (the help, the design system, services, other sites): an icon after the text, and "(opens in a new window)" for screen readers. `TestNewWindowLinks` checks every such link has it |
| Card | `.card`, with a `.heading` row for title plus link | One topic per card |
| Area heading | `<h2 class="area-heading">` between cards | A page with groups of cards: Backups has "Each server's backups" (the server switcher, then that server's cards) and "Your whole lab" (the same whichever server is chosen). Only when there's more than one group |
| Facts | `<dl class="facts">` | Label/value pairs; stack on phones |
| Forms | `<label>Text <input><small>hint</small></label>`, `label.check` for checkboxes and radios, `.with-suffix` (a fixed end, e.g. the domain), `.with-prefix` (a fixed start, e.g. a service's package name), `select.inline` | Errors: `<p class="error" role="alert">` above the form |
| Rows | `ul.rows` with `li.row-split` | Lists you act on (people, kept copies) |
| Needs attention | `.attention .item` with `.item-problem` / `.item-warning` | Home page |
| Activity | `{{activity .SomeActivity}}` | Host operations: status line plus collapsed log; nothing if there's none |
| Technical details | `<details class="technical">` | Collapsed by default |
| Secret | `<code class="secret">` | Values to copy, shown once |
| Area pages | `data["AreaNav"] = areaNav{…}` (Settings: `withArea`; your account: `renderAccount`) | A sidebar of the area's pages, the current one marked, beside the page (`area-nav`). On phones Settings' sidebar hides (the back link leads to its overview) and the account's becomes a row of tabs (`Tabs`) |
| Address choice | `{{template "address-choice" .AddressChoice}}` inside a form | The address another server or lab connects to (join code, storage invite): radio choices Roostack knows, each with the address in `code`, then "Another address" revealing a field (`address`, `address_other`) |
| Disk space | `.space-bar` with `.space-<part>` segments (`data-width`), `dl.space-list` with `.swatch` | A disk's use in coloured parts, each named with its size in the key |
| Project credit | `{{template "project-credit" .Credit}}` (a `projectCredit`, from `about:` in a package) | Who makes an app, its licence (linked to its text on spdx.org) and its website, source and support links, beside its logo; on the install page, under the description |
| Copy box | `{{copyBox TEXT}}` | Text people copy and send or paste elsewhere (an invite, a command), with a Copy button |
| Figures | `dl.stat-tiles` (a `div` of `dt` and `dd`, optional `dd.hint`) | A few totals side by side (visitors through the relay) |
| Bar chart | `{{template "bar-chart" .Chart}}` (a `barChart`: caption and `barView`s) | Counts over time, a column a period; heights through `data-height` (app.js, as for `data-width`); the exact figure on hover and for screen readers |
| Ranked list | `{{template "rank-list" .Ranks}}` (`[]rankView`) | Names by count with a bar for each one's share; the rest (`Other`) muted |
| Choice of period | `nav.period-choice` of links, the current one `aria-current` | How far back a set of figures goes |

### Adding a component

1. Add its CSS to `app.css` (tokens only). If its markup is worth sharing, add a template to `components.html` and a function to `components.go` that checks its arguments (a wrong argument should be an error, not a quietly broken page).
2. Add an example to `gallerySections` in `internal/web/gallery.go`.
3. Regenerate `docs/design/gallery.html` (command above) and look at it in both modes.

## Writing

Sentence case everywhere ("Back up now", not "Back Up Now"). Say what happened and what to do. No "successfully", no "please", no exclamation marks. Write for someone who isn't technical: "Devices at home can't reach your services", not "DNS rebinding protection".
