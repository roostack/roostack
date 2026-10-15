# When a server is gone

If one of your lab's servers stops working for good (a dead disk, a
stolen laptop, a cloud server you stopped paying for), its services can
come back on another server, from their backups.

!!! tip "Switched off isn't gone"

    A server that's only switched off, or whose network is down, comes
    back by itself when it's running again, with its services. Wait for it
    if you can.

## Restoring its services

1. Go to **Servers**. A server that can't be reached says
   **Not connected**.
2. Choose ***server* is gone: restore its services…**
3. Tick the services to restore now. Each one says which backup it comes
   from (the newest that can still be read: if the server's own drive went
   with it, a copy elsewhere) and how big it is. All are ticked when they
   fit on the server you restore onto.
4. Choose which server they go onto, tick ***server* isn't coming back**,
   and choose **Continue**.

The services are restored one after another, and the page shows how it's
going. A service with no backup that can be read can't be restored: remove
it from its page, and install it again if you need it.

## Services waiting to be restored

The services you didn't tick **wait to be restored**. They run nowhere,
but nothing is deleted:

- The **Services** page and the home page say they're waiting.
- Restore one whenever you like, onto any server (one you add later
  included): **Backups → Restoring**, or **Restore…** on its page.
- Their backups are kept: the nightly clean-up never removes the last
  backups of a service that's no longer backed up.
- Don't need one any more? **Remove *service* and delete its backups…** on
  its page deletes it, and every backup of it, for good.

Once none of its services are waiting, the gone server leaves the lab. Its
own backups stay where they are.

??? note "For technical users"

    Saying a server is gone marks each of its services `waiting: true` in
    its instance file (`/var/lib/roostack/services/<service>.yaml`), with
    `host` still naming the gone server, so it's in no server's assignment.
    Restoring uses the restore through the controller (see
    [Get something back](../backups/restore.md)): the controller streams
    the service's snapshot from whichever repository it reads, with the
    secrets saved in it, to the new server. "Remove and delete its backups"
    runs `restic forget --prune` on every snapshot tagged
    `service=<name>`, in every repository the recovery key opens.
