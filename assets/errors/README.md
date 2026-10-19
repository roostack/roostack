# Error scenes

Pictures for error pages (SPEC.md §6.3.4): the small mark's hen and server
(or Roostack Cloud's cloud), without the tile, with props.

| Scene | Used for |
|---|---|
| `flown` | Roostack: 404 |
| `pecking` | Roostack: 403 |
| `scrambled` | Roostack: 500; services: any other error |
| `vacant` | Services: 404 |
| `shooed` | Services: 403 (refused by CrowdSec: a banned address, or a request that looked like an attack) |
| `crowded` | Services: 429 (rate limited) |
| `asleep` | Services: 502 |
| `hatching` | Services: 503 |
| `slow` | Services: 504 |
| `cloud-asleep`, `cloud-empty`, `cloud-unplugged` | Roostack Cloud's own pages |

## Regenerating

```sh
python3 assets/errors/generate.py
```

This rewrites `internal/errpage/scenes.go` and the SVGs here. Don't edit
either by hand. The SVGs carry the default theme's colours (light, with a
dark media query); in the UI the `es-*` classes in `app.css` colour them
from the person's theme. Then regenerate the design gallery and generator
golden files, and look at the scenes in both modes:

```sh
go test ./internal/web -run TestGalleryFile -update-gallery
make golden
```
