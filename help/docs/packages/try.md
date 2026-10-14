# Try it on a lab

Before a package goes into the registry, it's tried on a real lab from
start to finish: installed, used, backed up, restored onto a fresh server
and removed. That's what shows its data is really in its backups, which
reading the file can't.

Use servers you can wipe, not the lab your family relies on: two spare
computers or virtual machines with Roostack
[installed](../start/install.md). Set up the first as a new lab, with a
[backup plan](../backups/index.md); keep the second fresh for the restore.

As you go, note what you did and what happened: you'll paste it into the
pull request ([what to include](submit.md#what-to-include)).

## Add it and install it

1. On **Add a service**, under **Add your own**, choose **Add a
   package…**, then your package file and its logo, and **Add**
   ([Add your own](../services/add.md#your-own) has the details).
2. Choose **Install** beside it. Before you do, read **What it sets up**:
   is everything people would miss marked **backed up**? Under
   **Details**, check the files Roostack would write.
3. Install it, and wait for every step to finish.
4. Open it, and use it as someone would: create an account, add some
   data (a note, a photo, a repository), change a setting. Keep a note of
   what you added: you'll look for it after the restore.
5. If it has an address people might make public, make it public and
   check the pages in `login_paths` and `private_paths` behave as the
   package says.

## Measure its memory

Once it has been in use for a few minutes, see what each container
uses:

```bash
sudo -u roostack bash -c 'cd && XDG_RUNTIME_DIR=/run/user/$(id -u) podman stats --no-stream --format "{{.Name}} {{.MemUsage}}"'
```

Put each container's figure, rounded up, in its `memory` field (and a
database's, if it's well above Roostack's own guess of 200 MB for Postgres
or 50 MB for Redis).

## Back it up and restore it

1. On **Backups**, choose **Back up now**, and wait for it to finish.
   `sudo roostack backup snapshots` lists a backup of the service, tagged
   with its name.
2. On the fresh server, [restore the whole lab](../trouble/restore-lab.md)
   from those backups. Your package comes back with the lab.
3. Open the service on the restored lab, and check everything you added
   is there and works: log in, open what you created.

This is the test that matters most: a folder that should have been
`critical`, or a database that wasn't declared, shows up here as data
that didn't come back.

## Update it

Roostack can't yet move an installed service to a new version of its
package, so this step waits until it can. For now, a new version of a
package goes through the same steps as a new package.

## Remove it

On the service's page, choose **Remove *service*…**. Check it stops, its address stops
working, and its data folders are still there
([what removing does](../services/remove.md)).

Next: [submit it](submit.md).

??? note "For technical users"

    Roostack's own developers try packages on the development VMs in
    Roostack's repository (`scripts/dev-vm.sh`): `ROOSTACK_VM=1` as the
    lab, `ROOSTACK_VM=2` as the fresh server to restore onto, and
    `scripts/dev-vm.sh usb add` for a backup drive.

    While you work on a package, change it on your lab by adding the file
    again (it replaces the one you added before), then remove the service
    and install it again. Removing keeps the service's data, so delete
    `/srv/roostack/services/<name>/` (and `/srv/roostack/media/<name>/`)
    first for a truly fresh install.
