# MyLab design system

Every page is built from the tokens and components here. The gallery shows them all, in light and dark side by side:

- In MyLab: **/design** (linked in the footer of every page).
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
| `internal/web/templates/components.html` | Components with markup worth sharing (`notice`, `status`, `badge`, `back`, `confirm-button`) |
| `internal/web/gallery.go` | The gallery's examples |

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
| `--highlight`, `--highlight-fg`, `--highlight-soft` | The brand's spark: the logo mark, where you are (current page), focus rings, "New" badges. Use sparingly |
| `--ok`, `--warn`, `--bad` (+ `-bg`, and `--bad-fg`) | Status. MyLab's own themes share them, so a problem always looks like a problem; a palette may override them (`--pal-ok-l`…), as Catppuccin does |

Never use raw colours (`#…`) or palette variables (`--pal-…`) in `app.css` or templates.

### Mode

`data-mode` on `<html>` is `light` or `dark`; without it, the device's preference decides (the user's Auto setting). It also works on any element, which is how the gallery shows both at once. The two dark blocks in `tokens.css` (media query and attribute) must stay identical.

### Built-in themes

| Theme | Light / dark | Actions / highlight | Source (licence) |
|---|---|---|---|
| Teal and violet (default) | MyLab | Teal / violet | MyLab |
| Ocean and magenta | MyLab | Ocean blue / magenta | MyLab |
| Catppuccin | Latte / Mocha | Mauve / pink | [Catppuccin](https://catppuccin.com) (MIT) |
| Gruvbox | Light / dark | Orange / yellow | [Gruvbox](https://github.com/morhetz/gruvbox) (MIT) |
| Nord | Snow Storm / Polar Night | Frost blue / Aurora purple | [Nord](https://www.nordtheme.com) (MIT) |
| Solarized | Light / dark | Blue / magenta | [Solarized](https://ethanschoonover.com/solarized) (MIT) |
| Everforest | Light / dark (medium) | Green / orange | [Everforest](https://github.com/sainnhe/everforest) (MIT) |
| Rosé Pine | Dawn / Moon | Pine / rose (MyLab status colours) | [Rosé Pine](https://rosepinetheme.com) (MIT) |
| Tokyo Night | Day / Night | Blue / magenta | [Tokyo Night](https://github.com/folke/tokyonight.nvim) (Apache 2.0) |

Theme files use plain hex colours, and `TestThemesAreReadable` checks every theme in both modes against WCAG contrast: text and muted text on every surface and on notice tints (4.5:1), links (4.5:1), text on buttons (4.5:1), status text on its tint and on cards (4.5:1), and the highlight (3:1, it isn't text). Where a project's own colour is too faint for UI text on its own background (common in light variants), it's nudged just far enough to pass, keeping its hue; each file lists what was adjusted.

Catppuccin's flavours only differ in how dark they are, so MyLab offers one Catppuccin theme (Latte and Mocha) rather than several near-identical ones.

### Adding a built-in theme

1. Copy `themes/teal.css` to `themes/<name>.css`, change the selector to `[data-palette="<name>"]`, and change the `--pal-*` values (hex only). Credit the source and licence at the top.
2. Add it to `Themes` in `internal/web/themes.go`, with a `Source` credit if it comes from an open-source palette (shown on the theme's row and the About page), then regenerate `THIRD_PARTY.md` (`go test ./internal/web -run TestThirdPartyFile -update-credits`).
3. Run `go test ./internal/web`: the contrast test tells you which colours to adjust.
4. Regenerate the gallery and compare it with the others (the gallery has a theme picker).

Nothing else changes: components only see purpose tokens.

## Components

| Component | How | Notes |
|---|---|---|
| Buttons | `<button>` (primary), `.secondary`, `.danger`, `.quiet` (looks like a link), `.quiet.danger`; `<a class="button">` for navigation | One primary per form or card |
| Confirm | `{{template "confirm-button" (dict "Action" … "Confirm" … "Text" … "Kind" …)}}` | For anything hard to undo. User text goes through a data attribute, never into script |
| Notice | `{{template "notice" (dict "Kind" "warn" "Text" … "Link" … "LinkText" …)}}` | Kind: none (info), `ok`, `warn`, `error` |
| Status | `{{template "status" "running"}}` | Service state |
| Badge | `{{template "badge" (dict "Text" "New" "Highlight" true)}}` | Highlight badges are rare |
| Back link | `{{template "back" (dict "Href" "/settings" "Text" "Settings")}}` | |
| Card | `.card`, with a `.heading` row for title plus link | One topic per card |
| Facts | `<dl class="facts">` | Label/value pairs; stack on phones |
| Forms | `<label>Text <input><small>hint</small></label>`, `label.check` for checkboxes and radios, `.with-suffix`, `select.inline` | Errors: `<p class="error" role="alert">` above the form |
| Rows | `ul.rows` with `li.row-split` | Lists you act on (people, kept copies) |
| Needs attention | `.attention .item` with `.item-problem` / `.item-warning` | Home page |
| Activity | `{{template "activity" (dict "Activity" …)}}` | Host operations: status line plus collapsed log |
| Technical details | `<details class="technical">` | Collapsed by default |
| Secret | `<code class="secret">` | Values to copy, shown once |

### Adding a component

1. Add its CSS to `app.css` (tokens only), and a template to `components.html` if its markup is worth sharing.
2. Add an example to `gallerySections` in `internal/web/gallery.go`.
3. Regenerate `docs/design/gallery.html` (command above) and look at it in both modes.

## Writing

Sentence case everywhere ("Back up now", not "Back Up Now"). Say what happened and what to do. No "successfully", no "please", no exclamation marks. Write for someone who isn't technical: "Devices at home can't reach your services", not "DNS rebinding protection".
