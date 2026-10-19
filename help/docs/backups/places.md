# Where backups can go

When you add a backup to [your plan](index.md), Roostack asks where it
should go. There are five kinds of place.

## A drive

A **USB drive**, a **second disk** inside the server, or a **network share**
(such as a folder on a NAS). Roostack lists the drives it can see, best
first, and the list updates by itself: plug a drive in and it appears
within a few seconds.

- A drive that already has files on it can be used as it is. Roostack
  keeps its backups in a folder named `roostack` on it, and leaves
  everything else alone.
- A **new drive**, or one with nothing Roostack can use on it, can be
  **erased and prepared**. Roostack names the drive, its model and size,
  and asks you to confirm first: everything on it is deleted. Roostack
  never erases a drive it could use as it is (one with files on it, say),
  nor one that's in use. To start such a drive afresh, erase it on another
  computer first.
- **This server's own disk** is at the end of the list. It helps if you
  delete something by mistake, but not if the disk fails, so it's never
  enough on its own.

Once chosen, the drive is Roostack's backup drive for good: it's used
again after a restart, and if you unplug it and plug it back in. If it's
not connected when a backup is due, the backup fails with a message
saying so, rather than filling up the server's own disk.

!!! tip "At home and quick"

    A drive is the best **main backup**: restores come from it first, and
    it's fast.

## Online storage

A storage account with a company such as **Backblaze B2** or **Wasabi**, or
anything else that works like Amazon's **S3**. You'll need, from the
company's website:

- the **endpoint** (an address such as
  `https://s3.us-west-004.backblazeb2.com`);
- a **bucket** (a storage area you create there), and optionally a folder
  in it;
- an **access key** and **secret key**.

Roostack remembers the account, so any of your servers can back up to it
later, each into its own folder. It's a good **second backup**: it's away
from home, and it's a different kind of storage from a disk.

## Roostack Cloud

If your lab is [linked to Roostack Cloud](../settings/cloud.md), you can
back up to Roostack Cloud's own storage, in Australia. There's nothing to
set up: no account, keys or address. The chooser, and then the backup in
the plan, show how much of your plan's space your lab uses (as Roostack
Cloud measured it, once a day). From 80% full, Home says so too, as
Roostack Cloud's emails do. If your account has several labs, they share
the space, and each lab shows only what its own backups take.

Your backups are encrypted with your recovery key before they leave, so
Roostack Cloud stores them but can't read them. It's away from home, and
a different kind of storage from a disk, so it makes a good **second
backup**.

## A friend's lab

Space on a friend's (or a relative's) Roostack. They make an **invite
code** on their Roostack, under **Settings → Storage for others**, and send
it to you; you paste it in. Your backups are encrypted before they leave,
so they can't read them.

It's away from home too, and costs nothing if you do the same for them.
See [Storage for other labs](../servers/storage-for-others.md).

## Another of your servers

If your lab has more than one server, one can keep another's backups on
its disk. It's shown as **Another Roostack instance**. It counts as away
from home only if that server is somewhere else, so Roostack asks you
where each server is.

??? note "For technical users"

    **Drives** are mounted by filesystem UUID in `/etc/fstab` (marked
    `# Added by Roostack`), with `nofail` and `x-systemd.automount`, under
    `/mnt/roostack-<name>`. exFAT, FAT and NTFS drives are mounted so the
    `roostack` user owns their files. Erasing makes one GPT partition with
    an ext4 filesystem labelled `ROOSTACK`. The system disk, and LVM, RAID
    and encrypted disks, are never offered. Only a disk with no filesystem
    backups could use, nothing mounted and nothing in `/etc/fstab` can be
    erased. Instead of a drive you can type a folder under `/mnt`, `/media`
    or `/srv` (**A folder instead**) that exists already: Roostack makes a
    `roostack` folder inside it, owned by the `roostack` user.

    **Online storage** is any S3-compatible service; each server's
    repository is `s3:<endpoint>/<bucket>/<folder>/roostack/<lab ID>/<server ID>`. For Backblaze B2, Roostack
    makes an application key for each server that can reach only that
    server's folder, and deletes it when the server leaves the lab.

    **Roostack Cloud** gives each server a folder of its own in an
    S3-compatible bucket. No server keeps a storage password: before each
    backup it gets keys that last 12 hours, open only its own folder and
    can only add backups (the controller asks Roostack Cloud; other
    servers ask the controller). They're kept in
    `/var/lib/roostack/cloud-backup-keys.json`. The controller removes old
    backups weekly with the recovery key, using keys that can delete within
    your lab's folders only, and Roostack Cloud keeps deleted files for 90
    days in case of a mistake or an attack.

    **A friend's lab** and **another server** run restic's rest-server:
    backups travel over Roostack's WireGuard network, to a store only your
    lab can write to. Servers can only add backups; old ones are removed by
    the lab's controller.
