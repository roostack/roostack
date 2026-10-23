# Moving Immich

The general steps are in [Moving from Docker Compose](bring-data.md). This is
what's particular to Immich.

## Before you start

- **Update the old Immich** to the version Roostack runs (its install page
  says which). If it's older than 1.133, let it finish its own move from
  pgvecto.rs to VectorChord first: Roostack's database has VectorChord, and
  the check will refuse a dump that needs pgvecto.rs (`vectors`).
- If the old Immich reads a config file (`IMMICH_CONFIG_FILE`), put its
  settings into Immich's Administration → Settings first: the file won't be
  brought.

## Your files and the dump

- The folder to copy is the one `UPLOAD_LOCATION` in its `.env` names. Its
  `thumbs` and `encoded-video` will go to their own places by themselves.
  They can be made again, but bring them: making thumbnails for 100,000
  photos takes many hours.
- The dump, with the old database still running:
  `docker exec immich_postgres sh -c 'pg_dump --format=custom --no-owner -U "$POSTGRES_USER" "$POSTGRES_DB"' > main.dump`.
  Immich's own nightly backups (`.sql.gz`) can't be loaded yet.
- An Immich from before 1.133 kept its library at `/usr/src/app/upload`:
  choose that folder for *Your photos and videos* by hand. Immich moves its
  own paths to `/data` as it starts (since 1.136), so the check's warning
  about old folders in the data is expected.
- The smart search models aren't needed: Immich will download them again.

## Switching

Stop the server and machine learning, and keep them stopped, or phones will
carry on uploading to the old one:
`docker compose stop immich-server immich-machine-learning`, and
`restart: "no"` for both in its Compose file. Take the dump again, then
switch. A large library takes a while to copy the first time (1 TB is about
three hours over gigabit Ethernet); the switch itself is usually minutes.

## Afterwards

- Sign in with your old account: everyone's accounts and passwords came with
  the data. Open an album, search for something, upload from a phone.
- In Administration → Settings, look at the external domain (used in share
  links) and the machine-learning address, if you'd changed them.
- If you signed in through another identity provider before (Authelia,
  Authentik), that setting came with the data and still points there.
