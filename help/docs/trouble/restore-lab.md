# Restore a whole lab

If the server that runs your lab dies, is stolen, or is lost in a fire, you
can rebuild the lab on a new computer from its backups. Your services, their
data, your settings and the accounts all come back.

You need:

- a **new computer** with Roostack [installed](../start/install.md) (don't
  set it up);
- your **recovery key**;
- to know **where the backups are**: a USB drive, online storage, or
  another address.

## Steps

1. Open the new server's setup page, and choose **Restore a lab from its
   backups…** under the form.
2. Enter the **setup code**.
3. Say **where the backups are**:

    Cloud storage
    :   Your online storage account: endpoint, bucket, folder (if you
        chose one), and its keys. Roostack looks for every server's
        backups in the bucket.

    A disk connected to this server
    :   Plug in the backup drive, and choose it from the list. Roostack
        looks on it, and a few folders down.

    Another address
    :   Any restic address, for technical users.

4. Enter your **recovery key**, and choose **Look for backups**.
5. Roostack lists the backups the key opens, with when each was last
   backed up. Choose **Restore this lab** next to your lab.

Roostack puts everything back, then restarts as your lab. Keep the page
open: it moves on to the login page when it's done. Large backups can take
a while. Log in as before.

After that, Roostack sets your services up again, and puts their databases
back; the home page says so while it's happening.

## If you had more than one server

The lab comes back on the new server, from its latest backup. Your other
servers reconnect by themselves if they can reach it the way they reached
the old one: the same address, and UDP port 51820 forwarded to it if it's
behind a router.

!!! warning "Don't restore a lab that's still running"

    Restoring a lab onto a second computer while the first still runs gives
    you two copies fighting over the same web addresses and certificates.
    Only restore when the old server is gone.

## If the old server comes back

Sometimes the old server wasn't gone after all: the power or the internet
was out while you were away, and you restored the lab somewhere else to get
things working. When the old one comes back, it notices at its next backup:
the restored lab has been backing up into the same storage, and is newer.
The old copy then stands down. It stops backing up and stops updating your
web addresses, and its **Backups** and home pages say so. Its services keep
running until you deal with it: switch it off, or wipe it.

If it's the other way round (the restore was only a test, say), open the
right copy's **Backups** page and choose **This is the right copy → Keep
this copy**. It backs up again, and the other copy stands down at its next
backup. Switch the other one off first.

A server your lab removed while it couldn't be reached (its services were
restored elsewhere) does the same if it comes back: its own page says it
was removed, and it no longer backs up into your lab's storage.

??? note "For technical users"

    The restore finds repositories the key opens, restores the controller's
    latest snapshot of itself (tagged `server`: `/var/lib/roostack`,
    including the database dump and the WireGuard keys, and the Podman
    secrets), then the latest snapshot of each service on it (tagged
    `service=<name>`). It puts the database in place at the next start, then
    applies the lab, and each service's database is loaded from its dump
    once its containers are set up again.

    A restored lab keeps its WireGuard keys and certificate authority, so
    its other servers and the labs it shares storage with still recognise
    it.
