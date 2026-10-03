# Get something back

If a service's data is broken or something was deleted, you can put the
whole service back to how it was in a backup. Only the Owner can do this.

1. On **Backups**, choose **Restore *service*…**.
2. The newest backup is chosen for you, and the page says which and where
   it's read from ("Last night's backup, 02:00 (read from USB drive
   BACKUP)"). If the newest already has the problem you're fixing, open
   **Choose a different backup**: backups are listed once each, by when
   they were made (this week, this month, older), even when your backup
   plan keeps them in several places. If a backup is in more than one
   place, **Read it from** lets you choose a copy, for when the main backup
   can't be read.
3. With more than one server, you can also choose which server it goes
   onto: where it runs now, or another (it then moves there).
4. Read the warning: anything changed in the service since that backup
   will be undone.
5. Tick **I understand**, and choose **Restore**.

The service stops for a few minutes while its data is put back, then starts
again. The page shows each step as it happens.

## Your current data is kept

Restoring never deletes anything. The data the service had before is moved
aside, and listed on the restore page under **Kept copies**. Once you're
happy with the restore, **Delete** the copy there to free the space.

If a restore can't finish (the backup can't be read, say), Roostack puts
your current data back and starts the service again, and says so. If it
can't even do that, it says where your data is, and leaves the service
stopped rather than start it on half a restore.

## Backups from anywhere in your lab

The list has every backup of the service your lab can read: those of the
server it runs on, its extra copies, and those of any server it ran on
before. So a service can come back on any server, even if the server that
backed it up has stopped working. If some backups couldn't be read (a
server that's switched off, say), the page says so under the list.

## When there's nothing to restore

A service only appears in backups made after it was installed. If it's new,
wait for the nightly backup (or choose **Back up now** on the Backups
page).

## Restoring one file

Roostack restores whole services, not single files. A technical user can
take a single file out of a backup by hand (see below).

??? note "For technical users"

    Each backup has one snapshot per service, tagged `service=<name>`,
    holding its data folders and its Podman secrets, and every snapshot of
    one backup run, main and copies alike, is tagged `run=<time>`. The page
    lists them from every repository the controller opens with the recovery
    key (as for pruning), grouped by run, and reads the main backup first
    (restoring from it is usually quicker than from a copy).

    Back onto the server it runs on, from that server's main backup, the
    restore is a maintenance job there: it waits for any backup or update
    running, and nothing interrupts it. It stops the service's units, moves
    each data folder to `<folder>.before-restore-<time>`, restores each
    folder from the snapshot with restic, loads database dumps back (the
    SQLite copy, or `pg_restore` for Postgres), and starts the units again.
    A partial restore that was rolled back is kept as
    `<folder>.failed-restore-<time>`.

    From anywhere else (a copy, another server's backup, or onto another
    server), the controller does it, like
    [moving a service through a backup](../servers/move.md): the service
    stops where it runs (if that server can be reached), the controller
    streams its folders from the snapshot (`restic dump`) to the server it
    goes onto, with the secrets saved in the backup, and that server
    starts it and loads its databases. Data already there is kept as
    `<folder>.before-move-<time>`, and the old server's copy as
    `<folder>.moved-to-<server>-<time>`; both are listed under **Kept
    copies**.

    From the command line:

    ```bash
    roostack backup snapshots
    roostack backup restore <service> -snapshot <ID>
    ```

    To take out a single file, use restic directly with the recovery key.
    For a backup on a drive:

    ```bash
    sudo restic -r <repository> --password-file /var/lib/roostack/backup-key \
      restore <ID> --target /tmp/restore --include /srv/roostack/services/<service>/<path>
    ```

    Online storage also needs the account's keys, in `AWS_ACCESS_KEY_ID`
    and `AWS_SECRET_ACCESS_KEY`. The Backups page's **Manage** shows each
    backup's repository.
