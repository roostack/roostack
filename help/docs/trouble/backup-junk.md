# Files removed from backups

Your servers can add backups but never delete or change old ones (except
on their own disks). So a server that's been taken over, by ransomware
say, can't destroy its backups. It can still add files to them, though,
and a file that wasn't made with your lab's key can stop those backups
working: backups fail, and old ones can't be cleaned up or restored.

Roostack looks for such files every week, before it cleans up old
backups, and again soon after a backup fails in a way they cause. It reads
each file with your lab's key, twice, a minute apart. A file that fails
both times, in the way only a file made without the key can, is junk:
nothing can use it.

## What Roostack does

Where Roostack has its own access, it will remove the files at once, and
the home page will say so under **Needs attention**:

- another server's disk in your lab;
- storage at a friend's lab;
- a drive or folder on the server that runs Roostack's controller.

**Details** lists each file, its size, and who could have put it there.
Choose **Got it, clear this list** once you've read it.

On online storage and Roostack Cloud, Roostack can't remove them: the
home page will show a problem, and **What to do** will list the files to
delete with the storage's own tools.

## What you should do

Usually nothing more. If it happens again, one of the servers named could
have been taken over: check what runs there and who can log in to it, and
if in doubt, reinstall it and add it to your lab again.

??? note "For technical users"

    Roostack checks the repository's `keys/`, `locks/`, `snapshots/`, and
    `index/` folders (junk in `data/` does no harm: the next prune will
    delete it). A key file is junk if it isn't a restic key restic can try.
    Other files are junk when `restic cat` fails with
    `ciphertext verification failed` twice; a network error or a file
    still being written fails differently. Files are removed through the
    controller's admin access to the store (`DELETE` on rest-server), or
    with `rm` for a folder. The controller's log names each file, and
    nothing is kept.
