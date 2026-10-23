# Move a service

If your lab has more than one server, the Owner can move a service from one
to another: to a server with a bigger disk, say, or off one you're
retiring.

1. Open the service's page and choose **Move to another server…**.
2. The page shows where it runs now and how much data it has. Choose the
   server to **move it to**. A server whose kind of processor the service
   has no version for is listed but can't be chosen, and says so.
3. Choose **how**:

    Copy directly
    :   Roostack will copy its data while it keeps running, then stop it
        for a final copy of what's changed. Quickest when both servers are
        on the same network or the data is small.

    Through a backup
    :   Roostack will back it up (only what's changed since the last
        backup), stop it, and restore it on the new server. Better for lots
        of data or a slow link. It needs the old server's backups set up in
        Roostack.

    Roostack will suggest one, from how much data there is.

4. Choose **Move**.

The page will show each step. The service will be unavailable only while
it's stopped for the last part. Its **web address will stay the same**.

## The old copy

The service's data will stay on the old server in case you need it. It will
be listed on that server's card in **Servers**, under **Kept
copies**, with its size. **Delete** it once you're sure you don't.

??? note "For technical users"

    **Copy directly** streams the service's folders from the old server to
    the new one over the lab's network while it runs, then stops it, copies
    what changed (including deletions) and its Podman secrets, and switches
    the instance's `host`. **Through a backup** runs a fresh backup, then the
    controller streams the service's folders out of it (`restic dump`, with
    the recovery key) to the new server, which loads the database dumps.

    The old data is renamed with a `.moved-to-<server>-<time>` suffix;
    anything already at the destination is kept as `.before-move-<time>`.
    Both are listed under **Kept copies** on the **Servers** page until you
    delete them.
