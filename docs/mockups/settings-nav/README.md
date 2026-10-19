# Settings in the sidebar: mock-ups (2026-10-19)

**Chosen (2026-10-19): F, In Settings**, with icons on both sub-menus; built the same day (DESIGN.md "Layout"). The other directions stay as the record of what was considered.

**The problem** (the Owner, 2026-10-19): "Having 'All Settings' at the bottom is pretty weird - really it should be 'Settings' at the top as a clickable heading, with the full list of settings expandable underneath and maybe the option to pin commonly used ones (maybe with the current common ones pinned by default)? I'd be interested in playing around with a few alternative ideas."

**Today** (`layout.html`, DESIGN.md "Layout"): under the main areas, a "Settings" heading that isn't a link, then the everyday pages (History, People, Your lab's address, Updates and restarts: `sidebarSettings`), every settings page while you're inside Settings, and "All settings" (the overview, `/settings`) at the bottom.

**Open it** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/settings-nav/>. The bar at the bottom right switches the direction (Today for comparison, A to G), the page (Home, the overview, any settings page), Owner or Admin, light or dark, and a phone-width frame; Reset forgets pins, recent pages and what's open. The links in the sidebar work, so you can move about and see how each direction behaves. Choices go into the address (`?d=a&m=dark#time-zone`; `&open=1` opens the list, `&f=ssh` types into D's filter, `&embed=1&menu=1` is the phone view with the menu open).

It's the real `tokens.css`, Teal theme and `app.css`, with the real settings pages, groups and icons (`settingsAreas`, `settingsArea.Icon`); `mock.css` holds only what the real stylesheet lacks, and `mock.js` draws the sidebar with the vendored Alpine. The pin icon is new (it would go in `icons.go`).

## What every direction shares

- **"Settings" is a link** to the overview (`/settings`), at the top of its group; "All settings" goes. On the overview, the heading is where you are (`aria-current="page"`, the highlight tint).
- **Where you are always shows:** the current settings page is in the list even when it wouldn't be otherwise (A, E: unpinned; C: it's the latest; D: under an empty filter), and the sidebar scrolls to it, as today.
- **Roles:** only the Owner sees the group (settings need `auth.ManageHost`); an Admin sees Home, Services, Backups and Monitoring only (try "As Admin"). Unchanged from today.
- **Phones:** the same group in the drawer (the drawer is 300 px wide, so nothing wraps); buttons are 30 px or more; pins show without hovering on a touch screen (`@media (hover: none)`).
- **Keyboard and screen readers:** the fold is a real `<button>` with `aria-expanded` and `aria-controls` (its label, "Every settings page", doesn't change; the state is in `aria-expanded`); a pin is a toggle button (`aria-pressed`, "Pin Time zone"); every page is a plain link, so Tab order is the visual order. Escape still closes the drawer.

## A. Pins (the Owner's idea)

The heading opens the overview; a chevron beside it folds out every settings page, each with a pin. Pinned pages stay when it's closed; today's everyday four are pinned by default. A pinned page shows a filled pin in the highlight colour; an unpinned one shows an outline pin on hover or focus (always on phones). Closed, the page you're on shows below the pins even if it isn't pinned, with its outline pin, so pinning it is one click from where you are.

- **Good:** does exactly what the Owner described; predictable (nothing moves unless you move it); someone who never pins gets today's sidebar; "where you are" always shows.
- **Less good:** the pin is a small target beside a long label: at 15 px, "Updates and restarts" only just fits next to it in the 256 px sidebar (it's tuned to fit). Pins are a little hidden on desktop until you hover. One more thing to keep per person.
- **Where pins live:** per person, as the theme is: a `pinned_settings` column on the store's `users` row (a comma-separated list of area IDs, `''` meaning the default four), saved by a small POST (`/account/pins`, PRG, with htmx so the sidebar doesn't reload). Not a lab setting: the Owner and a second Owner may want different ones. Whether the list is open is a browser convenience (`localStorage`), not worth a column.
- **Order:** pinned pages keep the overview's order, not the order you pinned them; drag-to-reorder isn't worth it for four or five items.

## B. Groups

The overview's five groups (Lab, People and access, Network, Servers, Each server) as buttons, each folding out its pages as Services' are (`.nav-children`, no icons); the current page's group opens by itself.

- **Good:** shows the overview's structure; no state to keep (beyond what's open); scales if Settings grows.
- **Less good:** everything is two clicks away, even the everyday pages; five rows of group names say little ("Servers" right under the Servers main area is confusing, "Lab"); the icons go.

## C. Recent

Under the heading, the last four settings pages you opened, latest first ("Recently opened"); the chevron folds out every page. Before you've opened any: the everyday four.

- **Good:** no setup; adapts to what you do.
- **Less good:** the list reorders every time you open a page, so things are never where you left them (try moving between Time zone and People); a page you opened once by mistake pushes out one you use. Per person it'd need a column updated on every page view.

## D. Filter

A "Find a setting" box under the heading; empty, the everyday four (and the page you're on); typing narrows every settings page by title, group and a few words each ("ssh" finds Remote access, "dns" two pages, "password" People). Enter opens the first match; Escape clears it. Results are announced (`aria-live`).

- **Good:** fastest with a keyboard; finds pages by what you're looking for, not what they're called.
- **Less good:** a second search box once the header gets its search (DESIGN.md: "a search box later"), which should find settings too; on a phone it brings up the keyboard in the drawer; the words need keeping up to date.

## E. A, as a main item (added after trying A)

A's pins, but Settings is a main area below Monitoring with its icon, and its pages hang under it as Services' do (`.nav-children` of `.nav-sub`s, no icons); the chevron beside it folds out every page. While you're on a settings page, Settings is marked as a parent (`aria-current="true"`), as Services is on a service's page.

- **Good:** consistent with Services (every main area is an item, its pages under it); the smaller, iconless sub-items leave more room for the pin; the group stops looking like a heading that you might not know is clickable.
- **Less good:** Settings now looks as important as Home or Backups; the everyday pages lose their icons (less quick to scan); "where you are" shows twice (Settings and the page), as it does under Services.

## After the Owner's first look (2026-10-19)

The Owner: "None of these are doing it for me. Maybe just E with no pins is best. If we were going to do pins - the visible pins should always be visible, and they should also pin those items to the top regardless of whether settings is open or not. Being on/in settings would also be what expands settings rather than that weird drop down." Two more, with no fold button: being in Settings is what opens it.

### F. In Settings (E without pins)

Settings is a main item below Monitoring. While you're on the overview or any settings page, every settings page hangs under it (`.nav-children` of `.nav-sub`s, the overview's order), exactly as Services lists its services while you're there; anywhere else, Settings is one row. Settings is marked as a parent (`aria-current="true"`) on a settings page and as the page (`"page"`) on the overview.

- **Good:** the simplest: no state, no buttons, nothing to store; consistent with Services; the sidebar is shortest when you're not in Settings.
- **Less good:** an everyday page (People, Updates and restarts) is two clicks away from anywhere else (Settings, then the page, or the overview's card).
- **Building it:** `layout.html` and the sidebar's data only (`sidebarSettings` goes); no store change.

### G. Pins on top (F with pins)

F, plus pins: pinned pages are always under Settings, first, wherever you are; while you're in Settings the rest follow, after a faint line. Every pin is always visible (pinned: filled in the highlight; unpinned: an outline, quieter until you hover or focus it). Pinning a page moves it up into the pinned block; unpinning moves it back to its place in the rest. Today's everyday four are pinned by default.

- **Good:** an everyday page stays one click away; the pin's meaning is visible at once (pinned pages are the ones that stay).
- **Less good:** a page jumps when you pin or unpin it; 13 pins in a column is busy while you're in Settings.
- **Where pins live:** as A: a `pinned_settings` column on the `users` row, the default four when empty, a small POST to change them.

## Recommendation

First (before F and G): A or E, leaning E. After the Owner's first look: **F**, if one-click everyday pages aren't needed: it's the same pattern as Services and has nothing to keep. **G** if they are. D's job is better done by the header search later; A to E are recorded as considered.

## Choice

**F, In Settings** (the Owner, 2026-10-19: "Let's go F. Make sure to follow the same styling as the Services sub-menu items when that's open. Perhaps we could keep the icons for the sub-menu items too?").

- Settings' pages under it use exactly Services' sub-menu markup and classes (`.nav-children` of `.nav-sub`s); measured in the mock-up, their computed styles are identical (spacing, size, weight, colour, where you are). The mock-up now shows Services' own sub-menu too ("A service (Photos)" in the page list), to compare.
- **Icons on the sub-items** ("Sub-item icons" in the bar; `&i=0` turns them off): each settings page keeps its icon (`settingsArea.Icon`) at 16 px, in the highlight colour where you are. For consistency Services' sub-items get one too: in the mock-up their category icon and a plus for Add a service; built, it'd be the service's logo where it has one (`IconURL`), else its drawn icon (`DrawnIcon`), as the Services table shows. The Owner chose icons on both (2026-10-19).
