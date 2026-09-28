# Updates and restarts

## Updates

Every night, after the backup, each server installs **Debian's security and
bug-fix updates**. **Settings → Updates and restarts** shows when it last
ran and what it did; if it failed, **What happened** shows the details.

**Check for updates now** runs it straight away.

Services have their own versions, which change when Roostack is updated.

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

## Changes Roostack didn't make

Roostack looks after the whole server. If someone changes it by hand in a
way that could stop updates or the firewall working, the page lists it
under **Changes Roostack didn't make**:

- software from outside Debian (extra package sources);
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
