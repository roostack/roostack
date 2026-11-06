# Remove a service

On the service's page, choose **Remove *service*…** at the bottom. Roostack
will show where the service's data is before you confirm.

When you remove a service:

- it will be **stopped and removed** from your lab, and its web address
  will stop working;
- **its data will be kept** in the folders shown, and nothing will be
  deleted;
- if you **install it again** later, under the same name on the same
  server, it will use that data and carry on where it left off, and the
  install page will say so. Its first-time settings (such as Forgejo's
  administrator) can then be left empty: the service will keep the accounts
  it has. Fill them in only to set it up again with them, for example to
  add another administrator.

Only Owners and Admins can remove services.

## Its data, later

Removed services whose data is kept are listed under **Services →
Removed, data kept**: what each one was, which server it was on, when and
by whom it was removed, and how much space its data takes (counted once a
day, after the nightly backup). From there:

- **Install again** will open the install form with its name, title and
  server filled in, so it carries on with its data;
- **Delete…** (Owners only) will delete its folders and the passwords
  Roostack kept for it, for good, once you tick that you understand.

An Owner can also tick **Delete its data too** when removing a service:
Roostack will delete it once the service has stopped.

Backups made before then will still contain its data until they're tidied
away (after six months at most), and the Delete dialog will say how many
there are and when the oldest is from.

??? note "For technical users"

    Removing deletes the instance file
    (`/var/lib/roostack/services/<name>.yaml`); the next apply stops the
    units, removes the service's Quadlet files and Caddy snippet, and then
    removes its Podman network (`podman network rm roostack-<name>`), which
    the next install will make again. Data under `/srv/roostack/services/<name>/` and
    `/srv/roostack/media/<name>/` is never touched. Its Podman secrets are
    kept too, so reinstalling can read the data. The controller notes the
    removal in its database (`removed_services`).

    **Delete…** asks the service's server to run `podman unshare rm -rf`
    on those folders as the `roostack` user, and `podman secret rm` on its
    secrets (`roostack-<name>-…`, but not another service's whose name
    starts with it, such as `immich-2`'s). The server refuses while the
    service is still its own, or while its Quadlet files are still there.
