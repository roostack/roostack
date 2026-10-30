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

!!! tip "Removing it for good"

    To free the space, delete its data folders from the server once you're
    sure you don't need them. Backups made before you removed it still
    contain its data, until they're tidied away (after six months at most).

??? note "For technical users"

    Removing deletes the instance file
    (`/var/lib/roostack/services/<name>.yaml`); the next apply stops the
    units, removes the service's Quadlet files and Caddy snippet, and then
    removes its Podman network (`podman network rm roostack-<name>`), which
    the next install will make again. Data under `/srv/roostack/services/<name>/` and
    `/srv/roostack/media/<name>/` is never touched. Its Podman secrets are
    kept too, so reinstalling can read the data.
