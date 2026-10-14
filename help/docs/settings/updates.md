# Updates and restarts

## Updates

Every night, after the backup, each server installs **Debian's security and
bug-fix updates**. **Settings → Updates and restarts** shows when it last
ran and what it did; if it failed, **What happened** shows the details.

**Check for updates now** runs it straight away.

Services have their own versions: see [Services you can install](#services-you-can-install).

## Restarts

Some updates, such as ones for the system's core (the *kernel*), only take
effect after the server restarts. When one is needed, the page says so,
and lists the updates waiting for it. You can choose **Restart now**.

Under **When updates need a restart**:

Restart automatically at *time* *(recommended)*
:   Restarts at the time you choose (4 am unless you change it), after
    backups have finished. Services are unavailable for a minute or two.

Only tell me
:   Roostack waits for you to choose **Restart now**. Until then, security
    fixes for the kernel don't take effect.

Everything starts again by itself after a restart.

## Services you can install

Roostack gets the services you can install, and fixes to them, from the
**service registry**, [Roostack's public list of packages](https://github.com/roostack/services).
It checks every few hours, and when you open **Add a service**. On the
controller, **Settings → Updates and restarts** shows:

- when it last checked, and whether that worked (if not, **What went
  wrong** says why: usually the server can't reach the internet);
- how old the list of services is, how many there are, and the registry
  commit it was built from (a link to exactly those files);
- **Not offered on this lab**: services that need a newer Roostack, or
  aren't built for this server's processor.

**Check now** checks straight away.

If the list gets more than two weeks old, Roostack warns you under **Needs
attention**: the registry publishes a new list every day, so an old one
means Roostack can't reach it, and doesn't know about fixes or versions
withdrawn since. You can still install services from the old list.

**Check the service registry for services and fixes** is on unless you
turn it off. Off, you can't install services from the registry, and
Roostack doesn't hear when a version is withdrawn because it's unsafe.
Services you've installed keep running: each runs from the lab's own copy of
its package.

??? note "For technical users"

    The registry is at `https://registry.roostack.cloud/v1/` (`registry.url`
    in `lab.yaml` sets another address, such as a mirror). Roostack fetches
    `keys.json`, signed by the registry's root key (built into Roostack),
    which names the keys that may sign `index.json`; the index lists every
    package and version with its SHA-256. Roostack checks both signatures,
    refuses an index or key list older than one it has seen, and fetches
    only the package files and logos it needs, checking each one's SHA-256.
    The files are in `/var/lib/roostack/catalog`; `status.json` there is
    the last check. `roostack registry check` does the same from the
    command line and prints what it found. How the keys are kept is in
    Roostack's `docs/REGISTRY-KEYS.md`.

## Changes Roostack didn't make

Roostack looks after the whole server. If someone changes it by hand in a
way that could stop updates or the firewall working, the page lists it
under **Changes Roostack didn't make**:

- software from outside Debian (extra package sources; a cloud provider's
  own copy of Debian, such as `mirrors.linode.com`, counts as Debian);
- packages held back from updates, which then don't get security fixes;
- another firewall, whose rules apply as well as Roostack's;
- Roostack's own firewall missing.

If the changes were on purpose, choose **I made these changes**. Roostack
stops warning about them, until something else changes.

??? note "For technical users"

    Updates run through `roostack-agent`, as a transient systemd unit
    (`roostack-os-updates`): `apt-get update`, then `unattended-upgrade -v`,
    with its output streamed to the page. A restart is needed when
    `/run/reboot-required` exists (the packages are in
    `/run/reboot-required.pkgs`). All maintenance (backups, updates,
    restarts, restores) goes through one queue, one job at a time, so a
    restart never interrupts a backup.

    Host checks: apt sources that aren't Debian's mirrors, `apt-mark`
    holds, nftables tables other than Roostack's (or ufw/firewalld), and
    whether Roostack's `inet roostack` table is loaded.
