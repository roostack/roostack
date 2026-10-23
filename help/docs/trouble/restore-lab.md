# Restore a whole lab

If the server that runs your lab dies, is stolen, or is lost in a fire, you
can rebuild the lab on a new computer from its backups. Your services, their
data, your settings, and the accounts will all come back.

You need:

- a **new computer** with Roostack [installed](../start/install.md) (don't
  set it up);
- your **recovery key**;
- to know **where the backups are**: Roostack Cloud, a USB drive, online
  storage, or another address.

## Steps

1. Open the new server's setup page, and choose **Restore a lab from its
   backups…** under the form.
2. Enter the **setup code**.
3. Say **where the backups are**:

    Roostack Cloud
    :   If your lab backed up to Roostack Cloud. Nothing else to enter:
        your recovery key is enough. Roostack will prove to Roostack Cloud
        that you have it without sending it, and Roostack Cloud will email
        your account to say the backups were opened.

    Online storage
    :   Your online storage account: endpoint, bucket, folder (if you
        chose one), and its keys. Roostack will look for every server's
        backups in the bucket.

    A disk connected to this server
    :   Plug in the backup drive and choose it from the list. Roostack will
        look on it and a few folders down.

    Another address
    :   Any restic address, for technical users.

4. Enter your **recovery key**, and choose **Look for backups**.
5. Roostack will list the backups the key opens, with when each was last
   backed up. Choose **Restore this lab** next to your lab. If you once
   unlinked the lab from Roostack Cloud and linked it again, Roostack
   Cloud keeps the backups from before for a while: the page will list each
   link's backups, with when it was linked or unlinked, so that you can
   choose.

Roostack will put everything back, then restart as your lab. Keep the page
open: it will move on to the login page when it's done. Large backups can
take a while. Log in as before.

After that, Roostack will set your services up again and put their
databases back; the home page will say so while it's happening.

??? note "If it wasn't you: an email about your recovery key"

    Roostack Cloud emails your account whenever your lab's recovery key is
    used with it: to read the lab's backups for a restore, or to relink a
    lab whose certificate stopped working. If you didn't do it, someone has
    your recovery key, and it opens every backup: change it now, on the
    lab's **Backups** page (**Change recovery key…**). The old key will
    then stop working, in Roostack Cloud too.

??? note "For technical users"

    The recovery key's **identity** is an Ed25519 key derived from it
    (HKDF-SHA256). The lab registers its public half with Roostack Cloud
    when it links, and again when the recovery key changes. Restoring signs
    a one-time challenge; Roostack Cloud then gives keys that last 12 hours
    and can read your lab's folders (and write only restic's locks). The
    same proof gets a lab a new certificate when Roostack Cloud no longer
    accepts its own, unless the lab was unlinked.

## If you had more than one server

The lab will come back on the new server, from its latest backup. Your
other servers will reconnect by themselves if they can reach it the way
they reached the old one: the same address, and UDP port 51820 forwarded to
it if it's behind a router.

!!! warning "Don't restore a lab that's still running"

    Restoring a lab onto a second computer while the first is still running
    will give you two copies fighting over the same web addresses and
    certificates. Only restore when the old server is gone.

## If the old server comes back

Sometimes the old server wasn't gone after all: the power or the internet
was out while you were away, and you restored the lab somewhere else to get
things working. When the old one comes back, it will notice at its next
backup: the restored lab has been backing up into the same storage, and is
newer. The old copy will then stand down: it will stop backing up and stop
updating your web addresses, and its **Backups** and home pages will say
so. Its services will keep running until you deal with it: switch it off or
wipe it.

If it's the other way round (the restore was only a test, say), open the
right copy's **Backups** page and choose **This is the right copy → Keep
this copy**. It will back up again, and the other copy will stand down at
its next backup. Switch the other one off first.

A server your lab removed while it couldn't be reached (its services were
restored elsewhere) will do the same if it comes back: its own page will
say it was removed, and it will no longer back up into your lab's storage.

??? note "For technical users"

    The restore finds repositories the key opens, and shows whose folder
    each is in: every server's backups are in a folder named after its ID.
    A backup that says it's the controller's is offered only from that
    server's own folder, since any of the lab's servers could make one. It
    restores the controller's latest snapshot of itself (tagged `server`: `/var/lib/roostack`,
    including the database dump and the WireGuard keys, and the Podman
    secrets), then the latest snapshot of each service on it (tagged
    `service=<name>`), and nothing outside those places and each
    service's own folders. It puts the database in place at the next start, then
    applies the lab, and each service's database is loaded from its dump
    once its containers are set up again.

    A restored lab keeps its WireGuard keys and certificate authority, so
    its other servers and the labs it shares storage with still recognise
    it.
