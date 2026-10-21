# Third-party software

Generated from internal/web/credits.go (go test ./internal/web -run TestThirdPartyFile -update-credits).
Roostack shows the same list on its About page.

## Included in Roostack

Compiled into the `roostack` binary or served by it.

| Name | Licence | By | Notes |
|---|---|---|---|
| [Go standard library](https://go.dev) | BSD-3-Clause | The Go Authors |  |
| [htmx](https://htmx.org) | 0BSD |  | Page updates without reloading |
| [Alpine.js](https://alpinejs.dev) | MIT |  | Small interactive bits of pages |
| [yaml](https://eemeli.org/yaml) | ISC | Eemeli Aro | Reading and writing YAML in the package editor, comments kept |
| [Space Grotesk](https://github.com/floriankarsten/space-grotesk) | SIL Open Font License 1.1 | The Space Grotesk Project Authors | The font for headings and figures |
| [SQLite for Go](https://gitlab.com/cznic/sqlite) | BSD-3-Clause | The Sqlite Authors | Roostack's database (SQLite itself is public domain) (Go: `modernc.org/sqlite`) |
| [libc, mathutil, memory](https://gitlab.com/cznic) | BSD-3-Clause | The modernc.org Authors | Used by SQLite for Go (Go: `modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory`) |
| [Go crypto and sys](https://go.googlesource.com/crypto) | BSD-3-Clause | The Go Authors | Password hashing (argon2id) (Go: `golang.org/x/crypto`, `golang.org/x/sys`) |
| [YAML for Go](https://github.com/yaml/go-yaml) | MIT and Apache 2.0 | The go-yaml Authors | (Go: `go.yaml.in/yaml/v3`) |
| [qr](https://pkg.go.dev/rsc.io/qr) | BSD-3-Clause | The Go Authors (Russ Cox) | QR codes for two-factor setup (Go: `rsc.io/qr`) |
| [uuid](https://github.com/google/uuid) | BSD-3-Clause | Google | (Go: `github.com/google/uuid`) |
| [go-humanize](https://github.com/dustin/go-humanize) | MIT | Dustin Sallings | (Go: `github.com/dustin/go-humanize`) |
| [bigfft](https://github.com/remyoudompheng/bigfft) | BSD-3-Clause | Rémy Oudompheng | (Go: `github.com/remyoudompheng/bigfft`) |

## Colour themes

Palettes behind built-in themes (`internal/web/static/css/themes`). Roostack adjusts a few colours so text stays readable.

| Name | Licence | By | Notes |
|---|---|---|---|
| [Catppuccin](https://catppuccin.com) | MIT |  | Colours for the Catppuccin theme |
| [Gruvbox](https://github.com/morhetz/gruvbox) | MIT | Pavel Pertsev | Colours for the Gruvbox theme |
| [Nord](https://www.nordtheme.com) | MIT | Arctic Ice Studio | Colours for the Nord theme |
| [Solarized](https://ethanschoonover.com/solarized) | MIT | Ethan Schoonover | Colours for the Solarized theme |
| [Everforest](https://github.com/sainnhe/everforest) | MIT | sainnhe | Colours for the Everforest theme |
| [Rosé Pine](https://rosepinetheme.com) | MIT |  | Colours for the Rosé Pine theme |
| [Tokyo Night](https://github.com/folke/tokyonight.nvim) | Apache 2.0 | Folke Lemaitre | Colours for the Tokyo Night theme |

## Programs Roostack uses

Installed alongside Roostack and run by it; not part of Roostack.

| Name | Licence | By | Notes |
|---|---|---|---|
| [Podman](https://podman.io) | Apache 2.0 |  | Runs services in containers |
| [Caddy](https://caddyserver.com) | Apache 2.0 |  | Web addresses and HTTPS certificates |
| [restic](https://restic.net) | BSD-2-Clause |  | Backups |
| [immich-go](https://github.com/simulot/immich-go) | AGPL-3.0 | simulot | Migrating photos into Immich; built from its source on the server, the first time it's needed |
| [Debian](https://www.debian.org) | Various free licences |  | The operating system, and its updates |
