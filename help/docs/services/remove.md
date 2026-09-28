# Remove a service

On the service's page, choose **Remove *service*…** at the bottom. Roostack
shows where the service's data is before you confirm.

When you remove a service:

- it's **stopped and removed** from your lab, and its web address stops
  working;
- **its data is kept**, in the folders shown. Nothing is deleted;
- if you **install it again** later, it uses that data, and carries on
  where it left off.

Only Owners and Admins can remove services.

!!! tip "Removing it for good"

    To free the space, delete its data folders from the server once you're
    sure you don't need them. Backups made before you removed it still
    contain its data, until they're tidied away (after six months at most).

??? note "For technical users"

    Removing deletes the instance file
    (`/var/lib/roostack/services/<name>.yaml`); the next apply stops the
    units and removes the service's Quadlet files and Caddy snippet.
    Data under `/srv/roostack/services/<name>/` and
    `/srv/roostack/media/<name>/` is never touched. Its Podman secrets are
    kept too, so reinstalling can read the data.
