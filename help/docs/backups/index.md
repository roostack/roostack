# Your backup plan

A backup is a copy of your data somewhere else, so that when something goes
wrong (a disk fails, a file is deleted by mistake, the house floods) you
can get it back. Roostack backs up every service, and its own settings,
every night at 2 am.

The **Backups** page shows your plan, and tells you whether it's enough.
With more than one server, it's in two parts:

- **Each server's backups:** every server backs up its own services, so
  each has its own status and plan. The list at the top shows how each
  server's backups are doing; choose one to see its plan.
- **Your whole lab:** restoring any service (from any of the lab's
  backups), storage other labs give yours, and the recovery key, which
  opens every backup in the lab.

## Three copies: the 3-2-1 rule

Keep **3** copies of your data, on **2** kinds of storage, with **1** away
from home. Roostack numbers your plan the same way:

1. **Your data itself**, on the server. It's not a backup, but it's the
   first of the three copies.
2. **The main backup.** Restores read it first, so it's best at home, where
   getting things back is quick: a USB drive or a second disk.
3. **A second backup**, somewhere else: online storage, or space at a
   friend's Roostack.

You can add **extra backups** after these. They're optional, and not
numbered.

!!! tip "Start with one, then add the second"

    One backup is far better than none. Set up the main backup first; the
    plan suggests what to add next.

Each place in the plan is a box, one under another. **Add** opens the
choices in the box you're adding, and once you've chosen, that box shows
what Roostack is doing to set it up. At the end Roostack restarts to use
it, and the page shows the new backup.

## The checks

Under the plan, Roostack checks five things:

3 copies
:   Your data and at least two backups.

2 kinds of storage
:   Your data is on a disk, so at least one backup mustn't be a disk. A USB
    drive, a second disk, a folder and another server's disk all count as
    the same kind. Online storage and a friend's lab count as another.

1 away from home
:   Online storage, a friend's lab, or another of your servers that's
    somewhere else. Roostack asks you once where each server is, because it
    can't tell for itself.

1 safe from ransomware
:   A backup the server can add to but not delete, so ransomware on it, or
    someone who takes it over, can't destroy every backup. Another server's
    disk, a friend's lab, Roostack Cloud and Backblaze B2 count (on B2,
    while your bucket keeps hidden files for 30 days or more). Other online
    storage counts when its bucket keeps old versions for 30 days or more
    and the key you gave Roostack can't erase them: Roostack checks. A
    drive or folder on the server doesn't count: its card says so.

Backed up lately
:   The last backup worked, and wasn't more than two days ago.

## Changing the plan

Under **Manage**, on each backup, you can:

- see where it is;
- change where the main backup goes (new backups start there, and the old
  ones stay where they are);
- make another backup the main one (the two swap, and both keep their
  backups and their last results: each result belongs to the place it
  went to, not to its number on the plan);
- stop a backup;
- on online storage, give Roostack a new key for the account. Every
  server backing up there gets it (on Backblaze B2, a key of its own when
  the new key can make keys), so you can then delete the old key with the
  provider: after removing a server that held it, for example;
- on online storage that keeps old versions, bring back what was hidden
  or changed (below).

## If a server was taken over

Someone who takes over a server, or ransomware on it, can try to destroy
its backups. Where it can't delete them, it may still **hide** them, or
**overwrite** their files. Backblaze B2, and online storage that keeps old
versions, keep what was there, so Roostack can put it back.

1. First, remove the server from your lab (Settings → Servers), or wipe
   it, so it can't change anything again.
2. On its Backups page, open **Manage** on the backup, then **Bring back
   what was hidden or changed…**
3. Choose a time from before it was taken over, and **Check what
   changes**. Nothing changes yet: Roostack lists what would come back,
   what would be put back as it was, and what was added since and would
   be hidden.
4. If that looks right, choose **Go back to** that time, and confirm.

Nothing is erased: if you chose the wrong time, go back again to a time
just before you did it. Whatever's left over is erased later by your
bucket's own rule for old versions.

A backup on the server's own disk comes with a warning: it helps if you
delete something by mistake, but if the disk fails, you lose the data and
that backup together.

## Every night, and every week

- **Every night at 2 am**, each backup in the plan gets a new copy of what
  changed since the last one. OS updates run after it.
- **Every Sunday at 3 am**, old backups are tidied away and a sample of the
  backup is checked, to make sure it can still be read. Roostack keeps a
  backup from each of the last 7 days, the last 4 weeks and the last 6
  months.

If a backup fails or is more than two days old, it shows in **Needs
attention** on the home page, with what to do. If you remove a server that
held another server's main backup, that server's plan and **Needs
attention** say so straight away: choose a new place for its main backup
(its other backups carry on meanwhile).

??? note "For technical users"

    Backups use [restic](https://restic.net). Each backup in the plan is
    its own restic repository, and each is a full backup of the same files
    (not a `restic copy` of another), so each one can be restored on its
    own.

    - **What's in it:** `/var/lib/roostack` (desired state, Caddy's
      certificates, a consistent copy of Roostack's database) and the
      Podman secrets store, plus every service's data folders. Databases
      are dumped first (a SQLite copy, `pg_dump` for Postgres, or
      `mysqldump`/`mariadb-dump` for MySQL and MariaDB), so
      their backups are consistent. Container images aren't included:
      they're downloaded again.
    - **Encryption:** every repository is encrypted with your
      [recovery key](../start/recovery-key.md), kept in
      `/var/lib/roostack/backup-key`. Other servers' repositories also get
      their own key, and never see the recovery key.
    - **What a server can delete:** a server never removes old backups
      itself. On another server's disk and at a friend's lab it reaches
      [rest-server](https://github.com/restic/rest-server) in append-only
      mode; on Backblaze B2 it gets a key of its own without
      `deleteFiles`, so whatever it deletes is only hidden, and Backblaze
      erases hidden files as the bucket's lifecycle rule says (Roostack
      reads the rule, and never changes it). Roostack can only make those
      keys with an account key that can make keys (`writeKeys`), which
      keys made on Backblaze's website never have: make one with
      Backblaze's command-line tool, signed in with your master
      application key, `b2 key create roostack
      listBuckets,listFiles,readFiles,writeFiles,deleteFiles,writeKeys,deleteKeys,listKeys`
      (the backup's card shows the commands). Until then every server
      gets the account's key. Other S3 providers give every
      server the account's key. Roostack checks whether that key can
      destroy backups there: it reads the bucket's versioning and
      lifecycle rules, and writes a small file, `roostack-check` in your
      lab's folder, deletes it and tries to erase its old version, which a
      safe key isn't allowed to (in AWS's terms, a policy without
      `s3:DeleteObjectVersion`). It checks when a backup is added there,
      when the key changes, and weekly. When you remove a
      server, Settings → Servers lists the shared keys and passwords it
      still holds; change an account's key under **Manage** on a backup
      there.
    - **Going back to a time:** Roostack lists every version of the files
      in the server's folder (`ListObjectVersions`, with the account's
      key) and, for each file changed since that time, copies its version
      from then back on top (`CopyObject` with its `versionId`) or, if it
      was added since, hides it (a delete marker). restic's `locks/`
      folder is left alone; afterwards `restic unlock` clears stale locks
      and `restic check` checks the repository. Nothing is erased, so it
      also works with a key that can't erase versions.
    - **Keeping:** old backups are removed on Sunday by the lab's
      controller (first `restic unlock`, which clears only stale locks,
      such as those of a backup that crashed; then `restic forget --prune --group-by host,tags --keep-daily 7
      --keep-weekly 4 --keep-monthly 6`: each service is kept on its own, as
      each has its own snapshot, tagged `service=<name>`), then 5% of the data is read back
      (`restic check --read-data-subset=5%`).

    "Every night" is 2 am on each server's clock. On the nights the clocks
    change it still runs once (at 3 am when they go forward). A night the
    server was off (or asleep) is caught up: once it's been on for 10
    minutes, it backs up if it hasn't for over a day.

    List the backups from the command line with
    [`roostack backup snapshots`](../reference/cli.md#backups).
