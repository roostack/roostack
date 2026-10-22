# The migration rehearsal (2026-10-20)

Scripts from the rehearsal of bringing a Compose Immich and Plex into
Roostack by hand, on a throwaway dev VM (VM3, destroyed afterwards). Results:
docs/ASSUMPTIONS.md, section W. They're a record, not a tool: the passwords
in them are test values for that VM.

- `seed-immich.sh`: fills a Compose Immich with an admin, a second person,
  photos, videos, an album and a shared link, through Immich's API.
- `switch-immich.sh`: what "Switch to this data" would do for Immich:
  stop the old server, dump, copy by container path, load the dump with
  Roostack's restore command, start.
- `check-immich.sh`: counts, every file on disk, signing in, the album and
  the shared link.
- `switch-plex.sh`: the same for Plex's config folder.

The Compose Immich was Immich's own file at v3.3.3 with its library at
`/usr/src/app/upload` (`IMMICH_MEDIA_LOCATION` set, as an install from
before 1.133 has it) and its Postgres at
`ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0`. The
Compose Plex was `plexinc/pms-docker:1.43.4.10903-e5521bd8c` with
`PLEX_UID` 1000 and `/movies` read-only. Roostack's services were the
registry's packages, installed by writing `services/<name>.yaml` and
`packages/<name>/<version>.yaml` and running `roostack apply`, with the
Plex package's Quadlet lines given `Volume=…/plex/films:/movies:ro,rslave`
to try a library at its old path.
