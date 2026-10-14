# Vendored JavaScript and fonts

Served from the roostack binary (no CDN at runtime, so the UI works offline).

| File | Source | SHA-256 |
|---|---|---|
| htmx-2.0.11.min.js | https://cdn.jsdelivr.net/npm/htmx.org@2.0.11/dist/htmx.min.js | d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717 |
| alpine-3.17.4.min.js | https://cdn.jsdelivr.net/npm/alpinejs@3.17.4/dist/cdn.min.js | 232519394c6c8fdba6f362b1d9da16106db513cdbf899011f00daab4051df31c |
| fonts/space-grotesk-latin.woff2 | https://cdn.jsdelivr.net/npm/@fontsource-variable/space-grotesk@5.3.0/files/space-grotesk-latin-wght-normal.woff2 (licence: fonts/space-grotesk-OFL.txt) | 0640890476fc1198ab4de571fb658de443c4d85b66466ec09534a8737ab1ce9d |

To update: download the new version, replace the file, update this table and the `<script>` tags in `templates/layout.html` (the font: `@font-face` in `css/tokens.css`).
