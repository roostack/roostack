# Get something back

If a service's data is broken or something was deleted, you can put the
whole service back to how it was in a backup. Only the Owner can do this.

1. On **Backups**, choose **Restore *service*…**.
2. Choose which backup: the newest is selected, with older ones below.
3. Read the warning: anything changed in the service since that backup
   will be undone.
4. Tick **I understand**, and choose **Restore**.

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

## When there's nothing to restore

A service only appears in backups made after it was installed. If it's new,
wait for the nightly backup (or choose **Back up now** on the Backups
page).

## Restoring one file

Roostack restores whole services, not single files. A technical user can
take a single file out of a backup by hand (see below).

??? note "For technical users"

    A restore is a maintenance job: it waits for any backup or update
    running, and nothing interrupts it. It stops the service's units, moves
    each data folder to `<folder>.before-restore-<time>`, restores each
    folder from the snapshot with restic, loads database dumps back (the
    SQLite copy, or `pg_restore` for Postgres), and starts the units again.
    A partial restore that was rolled back is kept as
    `<folder>.failed-restore-<time>`.

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
