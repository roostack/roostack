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
| `internal/web/templates/components.html` | Templates of components with shared markup (notice, notice list, status, badge, server, back, confirm, activity, install steps, project credit) |
| `internal/web/icons.go` | The icons (`icon NAME`), drawn for Roostack |
| `internal/web/static/app.js` | Roostack's own small script for self-refreshing parts: sends the fingerprint of what a part shows (the server answers 204 when it's unchanged, `renderLive`), and keeps open `<details>` (with an `id`) open when a part does change. Give a `<details>` inside a refreshing area a stable `id`; poll with `every Ns [document.visibilityState === 'visible'], visibilitychange[document.visibilityState === 'visible'] from:document` and render the part with `renderLive` |
| `internal/web/components.go` | The functions pages call for those components; they check their arguments |
| `internal/web/gallery.go` | The gallery's examples |

## Layout: Console glass (chosen 2026-10-14; shell as built 2026-10-15)

The Owner chose the next look for Roostack's UI, and for Roostack Cloud's portal and admin console (which copy this design system with `tools/sync-design.sh`): a **sidebar-based management layout** with some of the website's Night roost glass. Mock-ups, with notes on every direction considered: [docs/mockups/sidebar/](mockups/sidebar/README.md) (direction D; open through a web server at the repo root).

**Why:** the brief (2026-10-14) asked for a more professional, administrative feel: navigation down the side rather than along the top, better use of large screens while staying good on a phone, and the website's look (glow, translucent tiles, Space Grotesk, status pills, the Friendly/Technical switch), in light and dark.

**What it is:**

- **Shell:** a sidebar floating 8 px from the window's edge (16 px corners, the surface at 70%): the lab's name at the top, then Home, Services, Backups, Servers, Monitoring and Settings (its pages under it while you're in Settings; see "Settings in the sidebar" below), then Help, Light/Dark and the person at the bottom. Where you are: the highlight tint with a faint highlight edge. On phones the sidebar is a drawer behind a menu button. Servers becomes a main area (it was under Settings), and Settings' own sidebar (`area-nav`) folds into this one.
- **Header:** each page's header sits on the page (no band, no border): breadcrumbs, the page's help link, and a search box later.
- **Pages:** a 12-column grid up to 1560 px wide with 24 px side padding (16 px on phones); cards side by side; tables for lists people scan (services, servers, restores, accounts); a strip of figures on Home.
- **Glass:** a faint aurora behind everything (three soft glows in `--accent`, `--highlight` and `--chart-1`, still; paler in light mode); cards are the surface at 80%, so the glow only tints them; translucent insets and secondary buttons.
  - **No blur, and a still aurora — Changed (2026-10-16), the Owner's choice, tried in Firefox step by step:** scrolling was slow in Firefox on the lab, Cloud and the admin console (smooth in Chrome). The aurora's circles were blurred 90px and slowly moving, so every glass card re-blurred on every frame; and each card and the sidebar blurred what was behind it (`backdrop-filter`), which Firefox draws slowly. In turn: the aurora became radial gradients standing still (solid to 60% of the radius, then fading; the motion had gone unnoticed): better; then no blur behind the cards (the aurora is already soft, and an 80% surface hid it, near-opaque in light mode): better; then none behind the sidebar: smooth. The surfaces stay translucent, so the glow still tints them. Considered: keeping the sidebar's blur (one element, but Firefox still redrew it as the page scrolled under it), and the aurora scrolling with the page instead of fixed (not needed).
- **From the website:** Space Grotesk headings, status pills (a dot and a word on the status tint), the big figure (the last backup's time), gradient text at most once a page, the Friendly/Technical switch on a service's page (flipping to the files behind it).
- **Text:** 16 px body text (was 17 px), 15 px in tables and the sidebar.

**New tokens** (all derived from existing purpose tokens, so no theme file changes): `--font-display` (Space Grotesk, vendored into `internal/web/static`, OFL), `--text-xs`, `--canvas` (the page behind the cards: `--bg` in light, `--bg` 62% with black in dark, as the website), `--glass`/`--glass-2` and `--edge`/`--edge-strong` (`--fg` at 4, 7, 10, 17%), `--lift` (a card shadow per mode). **New components:** sidebar, page header with breadcrumbs, figures strip, table list, status pill (replacing `status`' look), Friendly/Technical switch, code panel; each goes into the gallery.

**Themes and accessibility:** every built-in theme works as it is. Text sits on translucent surfaces, so `TestThemesAreReadable` gains the worst case: muted text, links and status text on a card and on the sidebar with the strongest glow colour behind (the surface mixed with `--canvas` tinted by `--accent` or `--highlight` at the aurora's strength). Measured in the mock-ups for all nine themes in both modes: 4.54:1 or more on cards (Nord light lowest), and 4.5:1 on the sidebar at 62% surface, which is why it's 70%. As built (2026-10-15) the test checks text, muted text, links and status text on a card, and text, muted text and links on the sidebar; status text on the sidebar fails for Nord light (warning 4.45:1), so the sidebar shows status only as a pill on its own tint, never as bare text. The aurora doesn't move (so reduced motion needs nothing); browsers without `color-mix` get solid surfaces. Focus stays `--focus-ring`. Checked on 2026-10-15: all nine themes in both modes on the built pages, and the solid look (every `@supports` block removed in the browser, light and dark); outside those blocks only `--aurora` (plain numbers) is used of the new tokens.

**Considered** (all in the mock-ups): **A, Console**: a solid sidebar and the same dense pages; the Owner liked it most but found it a little plain. **B, Night rail**: the sidebar always dark, from each theme's dark palette, with solid cards (recommended first; needed a `--nav-*` token set and extra contrast pairs). **C, Glass roost**: the website as it is, floating glass everywhere, a tab bar on phones; too much, especially its padding, and its contrast can't be tested well. **D, Console glass**, A's structure with some of C's glass, was made after the Owner's first look and chosen, with C's header style and a little less side padding.

**As built (2026-10-15), the shell:** `layout.html` draws the sidebar (`.sidebar`: `.sidebar-lab`, `.nav-group` with `.nav-item`s, Settings' everyday pages as `.nav-item`s with their icons under a `.nav-heading` (History, People, Your lab's address, Updates and restarts; every settings page while you're in Settings, scrolled to the current one; replaced on 2026-10-19 by Settings as a main item, see "Settings in the sidebar"). Under Services, while you're there, each service (`.nav-children` of `.nav-sub`s, and Add a service); beside Servers, how many need attention (`.nav-count`, a warning pill: a change that didn't finish or not answering, which the controller knows; and for 10 minutes after Home last asked every server, a restart due, an OS update that didn't finish or a disk running low), then `.sidebar-foot` with `.sidebar-tools` and `.who`), the page header (`.page-header`: the menu button on phones, `.crumbs`, the page's help), the aurora (`.aurora`, three `<span>`s, `opacity: var(--aurora)`), and the page (`.page`). A page gives its `title` and `content`, and optionally `crumbs` (default: its main area, then its title) and `page-help` (`{{define "page-help"}}{{help …}}{{end}}`). The sidebar's links scroll when the window is short, so its foot stays in view. Below 900 px it's a drawer (Alpine: `menu` on `<body>`; Escape or the dimmed page closes it). Without color-mix the sidebar and cards are solid (`@supports`). Servers is a main area: its list is `/servers` (`/settings/servers` redirects there), each server's own pages stay under `/settings/servers/<id>`. Logged out (sign-in, first run), pages get `.bare`: the brand mark and the help, no sidebar. **The setup wizard (2026-10-18, the Owner's choice) gets `.bare` too** while it lasts (a page whose data has `Wizard`): the brand mark, "Setup / Step N of 5" as `.crumbs` (`wizardSteps` in `wizard.go` numbers them by template), the step's help and Log out (an `.icon-button`), wrapping on phones; the same pages under Settings later have the sidebar (SPEC §6.3).

**Settings in the sidebar — Changed (2026-10-19), the Owner's choice:** Settings is a main item, after Monitoring, linking to the overview. While you're in Settings (the overview or any settings page), every settings page hangs under it, exactly as each service does under Services while you're there (`.nav-children` of `.nav-sub`s, the overview's order); anywhere else it's one row. Settings is `aria-current="page"` on the overview and `"true"` on a settings page (as Services is on a service's page). Every sub-item has a 16 px icon, in the highlight where you are: a settings page its own (`settingsArea.Icon`); a service its logo (`.nav-logo`) or its drawn icon, as in the Services table; Add a service a plus. **Why:** "All settings" at the bottom of a group whose heading wasn't a link was odd (the Owner: "really it should be 'Settings' at the top as a clickable heading"). The everyday pages (`sidebarSettings`) went: they're two clicks away now (Settings, then the page), which the Owner accepted for a simpler sidebar. **Considered** (mock-ups with notes: [docs/mockups/settings-nav/](mockups/settings-nav/README.md)): a clickable heading folding out every page with pins (A; and E, the same as a main item); the overview's groups each folding out (B); recently opened pages (C); a filter box (D); and F with pins kept at the top, always visible (G). The Owner found the fold-out button odd and pins not worth their cost.

**Closer to the mock-ups (2026-10-15, the Owner: "a lot of great ideas seem to have been lost"):** notices are tinted cards with their kind's icon (drawn by CSS, so every notice has one) and a faint edge, not a coloured bar; attention items put their first sentence in bold, the rest under it, and what to do as a small button; the page's parts are spaced by one gap (24 px), not their own margins; buttons have 10 px corners, secondary ones are glass; table links are quiet, with the row's action in bold; services show their logo (or category icon) in tables and beside the title on their page.

**As built (2026-10-15), the pages:** Home is a heading that says how things are (a greeting and "Everything's working." in gradient text, or how many things need attention), the attention items, a figures strip (services running, servers online, the oldest of the servers' last backups, disk space across every server), the services as a table list (logo, status, address, server, last backup), then the servers (with a meter of their disks) beside Recent activity (the servers' last five pieces of work, as History has them; Owner only). Services is one table list (address or install progress under each name; status, server, last backup, Manage), with Roostack's own services per server behind the toggle. A service's page has a page heading (its logo, the title with its status pill, version and description, the Friendly/Technical switch, Open), then two columns: how to reach it (with its version and update choice), a summary of its settings (web address, each setting's value, how it updates, links into the app, tokens to show) with **Edit settings**, and About (who makes it); its backup (the big figure, Restore), actions as rows with a chevron, extra connections and Remove. Its settings change on their own page (`/services/<name>/settings`, the Owner's choice, 2026-10-15): the web address and package settings, how it updates, and its name; saving goes back to the service's page, a mistake stays on the settings page. Its technical side shows each container's state and image, its Quadlet files and its Caddy site with their contents (made again by the controller as they're written: generation is pure; Podman secrets appear by name only), its log command, and where its data lives (what the Services list's Details showed before). Backups: the server switcher as tiles, status (the last backup as a big figure) and plan (the 3-2-1 checks as pills under its heading, the copies one under another at every width, the Owner's choice of 2026-10-18 over tiles side by side, whose numbers read wrong and whose box being added moved to a row of its own; a box being added shows its chooser, then its progress, in its place), with the latest run beside them while there is one, then the whole lab's: restoring as a table list (logo, its server's last backup), storage at other labs, the recovery key. Servers (`/servers`): a table list (with a meter of each server's disks), then each server's card a row each (two to a row squeezed the disk bars), and Add a server. Settings: the overview's groups under small headings. Every other page with a sidebar (2026-10-15) has its title, and a one-line description where it had one, in a `.page-head` above its cards, which line up under it (up to 760 px wide) rather than centring; History is a table list (the time under each entry on phones, `.show-phone`). Your account's pages share one heading, "Your account", above their tabs, each tab's title on its card. Pages without a login (sign-in, first-run setup, errors) stay centred cards.

**Search in the header:** not for now (the Owner, 2026-10-15); the header has room for it later.

## Tokens

Three layers:

1. **Palette** (`themes/<name>.css`): a theme's raw colours, `--pal-<name>-l` for light and `--pal-<name>-d` for dark, scoped to `[data-palette="<name>"]`. Every page loads all theme files (they're small); `data-palette` on `<html>` picks one, from the user's choice (Account → Appearance) or the default.
2. **Purpose** (`tokens.css`): what components use. Picked from the palette for the current mode.
3. **Scales** (`tokens.css`): `--space-1`…`--space-7`, `--text-xs`…`--text-2xl`, `--radius-sm`/`--radius`/`--radius-lg`/`--radius-pill`, `--shadow-1`, `--focus-ring`, and the fonts: `--font-sans` (text, 16 px; 15 px in tables and the sidebar), `--font-display` (Space Grotesk, for headings and figures; vendored in `static/fonts`, OFL) and `--font-mono`.

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
| `--logo-tile`, `--logo-tile-fg` | The tile behind a service's logo, and a drawn icon's lines on it: light in every theme and mode, since projects draw their logos for light backgrounds (Vaultwarden's is black). Only for the `logo` component |
| `--canvas` | The page behind the cards: `--bg` in light, `--bg` 62% with black in dark (deeper, as the website) |
| `--glass`, `--glass-2`, `--edge`, `--edge-strong` | Translucent text colour (4, 7, 10, 17%): insets, secondary buttons and edges over the aurora |
| `--lift` | A card's shadow on the canvas, per mode |
| `--aurora` | How strongly the aurora's circles show (`--aurora-l` 0.11, `--aurora-d` 0.20); the contrast test reads them |
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

Theme files use plain hex colours, and `TestThemesAreReadable` checks every theme in both modes against WCAG contrast: text and muted text on every surface and on notice tints (4.5:1), links (4.5:1), text on buttons (4.5:1), status text on its tint and on cards (4.5:1), the same text on glass (a card at 80% and the sidebar at 70% surface, over the canvas tinted by the accent or highlight at the aurora's strength; see "Layout"), the highlight (3:1, it isn't text), and the brand mark's online light on the text colour (3:1). Where a project's own colour is too faint for UI text on its own background (common in light variants), it's nudged just far enough to pass, keeping its hue; each file lists what was adjusted.

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
| Buttons | `<button>` (primary), `.secondary`, `.danger`, `.quiet` (looks like a link), `.quiet.danger`; `<a class="button">` for navigation | One primary per form or card. A label stays on one line, except on phones (640 px and less), where a long one ("Link and go back to …") wraps rather than leaving its card |
| Confirm | `{{confirm ACTION QUESTION TEXT KIND ["name=value"…]}}` | For anything hard to undo. KIND is the button class (`danger`, `secondary`, `quiet`, `""`); trailing `name=value` are hidden fields. The question goes through a data attribute, never into script |
| Confirm a form | `{{confirmSubmit QUESTION TEXT KIND}}` inside a form | For a form with fields of its own (checkboxes, choices); its required fields are checked before the dialog opens. KIND is the dialog button's class (`danger` or `""`) |
| Notice | `{{notice KIND TEXT ["link=…" "linkText=…"]}}` | KIND: `info`, `ok`, `warn`, `error` |
| Notice list | `{{noticeList KIND TEXT ITEMS}}` | A notice whose text leads into a list (`ITEMS`, plain strings, wrapped anywhere since they may be long and technical); nothing without items. What a package of your own asks for that registry packages can't have |
| Status pill | `{{status .Status}}` | Service state as the website's pill: a dot and a capitalised word on the status tint (solid, so it reads on glass). `installing` and `updating` are text on the info tint with a pulsing accent dot (was a spinner); `waiting` reads "Waiting to be restored" and `registry-stop` "Stopped by Roostack" (the registry withdrew its version as dangerous, SPEC §8.3.4) |
| Check state | `{{checkState .State}}` | A monitoring check's state (SPEC §6.15) as a pill: Up (ok), Down (bad), Paused or Not checked yet (plain) |
| Uptime bar | `{{uptimeBar .Days}}`, `size=mini` in lists | A check's days, oldest first, one bar each: `--ok` up whenever checked, `--warn` down under an hour, `--bad` an hour or more, `--border` not checked; each says how its day went on hover. `.bar-ends` labels the two ends under it. `.spark` is the answer-time line (an inline SVG polyline in `--accent`) on a check's Details |
| Install steps | `{{template "install-steps" .Service.Install}}` in a notice; `{{template "install-line" .}}` (a ServiceView) in lists | A new service's install (`node.InstallProgress`): `ol.install-steps` with `li.step-done` / `step-now` / `step-todo`, time taken or "for …" so far. `.address-pending` shows an address that isn't a link yet |
| Badge | `{{badge "New" ["highlight"\|"ok"\|"warn"]}}` | Highlight badges are rare. `ok` for something done (Installed), `warn` for something to weigh before acting (a Community service) |
| Tier badge | `{{template "tier-badge" .}}` (anything with `.Local` and `.Tier`) | Who looks after a service's package: **Official** (accent), **Community** or **Your own** (warning tint, something to weigh). The same on the catalog, the Services list and a service's page, where it sits beside the title inside the heading (`h1 > .badge`) |
| Logo | `{{logo SRC ICON}}` | A service's logo (SRC, a path on this site) on a light tile, the same in both modes; without one (`""`), the drawn ICON on the same tile, so lists line up. Decorative: the name beside it says what it is |
| Catalog entry | `ul.services` of `li.card.offer`: `{{logo …}}`, `.offer-text` (an `h2` and its badges, `p.hint`, `p.offer-categories`), `.offer-action` (the button, or `p.offer-later` when it can't be installed) | Add a service. On phones the action moves under the text |
| Filter chips | `div.filter-chips` (`role="group"`, an `aria-label`) of `<button type="button">`, the chosen one `aria-pressed="true"` | Narrows a list to one group (the catalog's categories), with a search box above it in `.catalog-filter`; filtering in the page (Alpine), hidden without scripts so the whole list shows |
| Back link | `{{back "/settings" "Settings"}}` | |
| Brand mark | `{{brandMark}}` | Roostack's small mark (the hen on a server), in the theme's colours through the `bm-*` classes in `app.css`. Generated into `brandmark.go` by `assets/brand/generate.py`: change it there, not by hand. Decorative; the name beside it says what it is |
| Error scene | `{{errorScene NAME ID}}` | A picture for an error page (`internal/errpage`): the small mark's hen and server, no tile, with props; `.es-scene`, coloured by the `es-*` classes in `app.css`. ID makes its SVG ID unique on the page. Generated by `assets/errors/generate.py`. Goes with a joke headline and plain words underneath (`error.html`, SPEC.md §6.3.4) |
| Sidebar | `layout.html` (see "Layout"): `.sidebar-lab` with `{{brandMark}}`, a small "Roostack" over the lab's name; `.nav-item`/`.nav-sub` with `aria-current="page"` where you are | The lab's name is the title, so people know whose lab it is; long names end in "…" (full name on hover). Status shows here only as a pill on its own tint |
| Page header and breadcrumbs | `layout.html`; a page may `{{define "crumbs"}}<a href="/services">Services</a><a href="/services/immich">Immich</a><strong aria-current="page">Move</strong>{{end}}` | Where you are, each step a link (separators are CSS), the page itself last; by default its main area then its title. It replaces back links. When the breadcrumbs and the page's help don't both fit (a phone), the help goes on a line of its own |
| Page help | `{{define "page-help"}}{{help "settings/people" "People and access"}}{{end}}` | Every page with a help page: a question-mark icon and the help page's title, at the right of the page header, opening in a new window. `TestHelpLinks` checks each page and #section exists in `help/docs` |
| New window | `<a href="…" target="_blank" rel="noopener">Text{{template "new-window"}}</a>` | Every link that opens a new window (the help, the design system, services, other sites): an icon after the text, and "(opens in a new window)" for screen readers. `TestNewWindowLinks` checks every such link has it |
| Card | `.card`, with a `.card-head` (or older `.heading`) row for its title and a `.more` link | One topic per card; on glass, the surface at 80% |
| Page heading | `.page-head`: the `h1`, a `.lede` under it, `.page-head-actions` on the right | Each main page's own heading under the page header; actions go under it on phones. `.gradient-text` at most once a page |
| Grid | `.grid` with `.span-3`…`.span-8` children (others span all 12) | Cards side by side; 4-wide cards go full width below 1100 px, everything below 900 px. `.section-heading` between groups |
| Figures strip | `ul.figures` of `li.card`: `.figure-label`, `.figure-value` (a unit in `small`), `.figure-note` (`.warn`, `.bad`) | A few figures at the top of Home; two to a row on phones |
| Big figure | `.big-figure` with an optional `small` | The one number a card is about (the last backup's time) |
| Table list | `.table-wrap > table.list` as a card's last part; `.list-name` (logo, name, a `small` line), `.hide-phone`, `.num`; the action in the last column | Lists people scan and act on (services, servers, restores, people), 15 px; it reaches the card's edges |
| Friendly/Technical switch | `.views` containing `label.view-switch` (`<input type="checkbox">`, two `span`s) and `.friendly-only` / `.technical-only` parts | A service's page flips to the files behind it; CSS only (`:has`), the friendly view without it |
| Code panel | `figure.code-panel`: `figcaption.code-bar` (where the file lives, or the command), then `pre > code`; `.code-comment`, `.code-section` | A file Roostack wrote or a command's output, in the technical view |
| Area heading | `<h2 class="area-heading">` between cards | A page with groups of cards: Backups has "Each server's backups" (the server switcher, then that server's cards) and "Your whole lab" (the same whichever server is chosen). Only when there's more than one group |
| Facts | `<dl class="facts">` | Label/value pairs; stack on phones. A short command in one is `<code class="nowrap">`, so `sshd -T` doesn't break at its hyphen on a phone |
| Forms | `<label>Text <input><small>hint</small></label>`, `label.check` for checkboxes and radios, `.with-suffix` (a fixed end, e.g. the domain), `.with-prefix` (a fixed start, e.g. a service's package name), `select.inline` | Errors: `<p class="error" role="alert">` above the form |
| Rows | `ul.rows` with `li.row-split` | Lists you act on (people, kept copies) |
| Drive cards | `.drives` of `.drive`: a `label.check` with a radio (the backup plan's chooser), or a link (`.drive > a`: a media library's drive, opening its folders); `.drive-unusable` greys one out with why (a backup drive) | A drive's name, a badge for its kind, and its size and filesystem in a `small` line |
| Needs attention | `.attention .item` with `.item-problem` / `.item-warning` | Home page. Text wraps anywhere (an address, a key). An error shows only its first line: restic's stack trace stays on the Backups page, under the failed backup's Details |
| Activity | `{{activity .SomeActivity}}` | Host operations: status line plus collapsed log; nothing if there's none |
| Technical details | `<details class="technical">` | Collapsed by default |
| Secret | `<code class="secret">` | Values to copy, shown once |
| Area pages | `data["AreaNav"] = areaNav{…}` (Settings: `withArea`; your account: `renderAccount`) | Settings' pages are listed in the sidebar, the current one marked (`withArea` names it). Your account's pages are a row of tabs at the top of each (`area-nav`, `Tabs`) |
| Address choice | `{{template "address-choice" .AddressChoice}}` inside a form | The address another server or lab connects to (join code, storage invite): radio choices Roostack knows, each with the address in `code`, then "Another address" revealing a field (`address`, `address_other`) |
| Disk space | `.space-bar` with `.space-<part>` segments (`data-width`), `dl.space-list` with `.swatch` | A disk's use in coloured parts, each named with its size in the key; the names take up to about half the width, so a size with a note isn't squeezed |
| Project credit | `{{template "project-credit" .Credit}}` (a `projectCredit`, from `about:` in a package) | Who makes an app, its licence (linked to its text on spdx.org) and its website, source and support links, beside its logo (on the `logo` tile); on the install page, under the description |
| Copy box | `{{copyBox TEXT}}` | Text people copy and send or paste elsewhere (an invite, a command), with a Copy button beside it, or under it on phones |
| Statistic tiles | `dl.stat-tiles` (a `div` of `dt` and `dd`, optional `dd.hint`) | A few totals side by side inside a card (visitors through the relay) |
| Bar chart | `{{template "bar-chart" .Chart}}` (a `barChart`: caption and `barView`s) | Counts over time, a column a period; heights through `data-height` (app.js, as for `data-width`); the exact figure on hover and for screen readers |
| Ranked list | `{{template "rank-list" .Ranks}}` (`[]rankView`) | Names by count with a bar for each one's share; the rest (`Other`) muted |
| Choice of period | `nav.period-choice` of links, the current one `aria-current` | How far back a set of figures goes |

### The package editor's own styles

The package editor (`internal/editor`, SPEC §8.3.7) runs in Roostack, on roostack.cloud and opened from a file with only `tokens.css`, so it carries its own styles: `internal/editor/static/editor.css`, every rule under `.pe`, from the purpose tokens and scales only (no raw colours), and looking like the components above (buttons, inputs, cards, pills, chips). Its buttons start plain (`:where(.pe) button`), since `app.css` styles every button as a main action, and `app.css`'s indented open `<details>` leaves `.pe details` alone. Its parts aren't in the gallery: nothing else uses them, and the editor's own page shows them all. Its icons are Roostack's (`import` and `spark` were added for it); `editor.js` draws copies, which `TestEditorIcons` keeps the same. The look was chosen from [docs/mockups/package-editor/](mockups/package-editor/README.md) (2026-10-20).

### Adding a component

1. Add its CSS to `app.css` (tokens only). If its markup is worth sharing, add a template to `components.html` and a function to `components.go` that checks its arguments (a wrong argument should be an error, not a quietly broken page).
2. Add an example to `gallerySections` in `internal/web/gallery.go`.
3. Regenerate `docs/design/gallery.html` (command above) and look at it in both modes.

## Writing

Sentence case everywhere ("Back up now", not "Back Up Now"). Say what happened and what to do. No "successfully", no "please", no exclamation marks. Write for someone who isn't technical: "Devices at home can't reach your services", not "DNS rebinding protection".
