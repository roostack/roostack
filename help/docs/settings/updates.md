# Updates and restarts

## Updates

Every night, after the backup, each server installs **Debian's security and
bug-fix updates**. **Settings → Updates and restarts** shows when it last
ran and what it did; if it failed, **What happened** shows the details.

**Check for updates now** will run it straight away.

## Roostack's own updates

Roostack updates itself the same way. Its releases come in **series**:
0.4.0, 0.4.1, and 0.4.2 are the 0.4 series and only fix things. Each server
installs the fixes in its series by itself, with that night's updates. The
page's **Roostack** line says which series the server is on.

A new series (0.5) can bring new features and change how things work, so
it will wait for you. When one is out, **Needs attention** on the home page
will say so (and the Monday summary email, if you get it), and this page
will say **Roostack 0.5.0 is available**, with a link to [what's
new](../whats-new.md) in it. Choose **Update to Roostack 0.5.0**, and the
server will move to it with its next updates: tonight, after the backup, or
straight away with **Check for updates now**. If the server's last backup
didn't work or is more than a day old, it will wait for one that worked,
and the page will say so. Each server in a lab has its own Updates page;
update each one.

??? note "For technical users"

    Roostack comes from its own apt repository, `https://apt.roostack.cloud`,
    one suite per series. The package installs its source,
    `/etc/apt/sources.list.d/roostack.sources` (`Suites:` is the series),
    and the key it's checked with,
    `/usr/share/keyrings/roostack-archive-keyring.gpg`.
    `/etc/apt/apt.conf.d/52roostack-unattended-upgrades` lets
    unattended-upgrades install packages whose origin is `Roostack`.

    Roostack learns about new series from `https://apt.roostack.cloud/series`
    (from the address in its source, so a mirror works), a few minutes after
    starting and every six hours. That list only prompts the offer: moving to
    a series changes `Suites:` and runs `apt-get update`, which checks the new
    suite's signature; if that fails, the source goes back as it was. Then
    `apt-get install roostack` keeps the source as it is on disk
    (`--force-confold`), so a mirror you set in `URIs:` stays.

    You can point `URIs:` at a mirror of the repository. Keep `Signed-By:`:
    with it, apt takes only packages signed with Roostack's key, and Roostack
    doesn't count the source as software from outside Debian.

Services have their own versions, from the service registry (see
[Services you can install](#services-you-can-install)). Each service's page
offers its updates, and you choose there whether it updates by itself:
see [Change a service](../services/settings.md#updates).

## Restarts

Some updates, such as ones for the system's core (the *kernel*), only take
effect after the server restarts. When one is needed, the page will say so
and list the updates waiting for it. You can choose **Restart now**.

Under **When updates need a restart**:

Restart automatically at *time* *(recommended)*
:   The server will restart at the time you choose (4 am unless you change
    it), after backups have finished. Services will be unavailable for a
    minute or two.

Only tell me
:   Roostack will wait for you to choose **Restart now**. Until then,
    security fixes for the kernel won't take effect.

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
  aren't built for the processor of any of your servers. In a lab mixing
  processors (a PC and a Raspberry Pi), a service built for only one of
  them is offered for the servers it can run on.

**Check now** will check straight away.

If the list gets more than two weeks old, Roostack will warn you under
**Needs attention**: the registry publishes a new list every day, so an old
one means Roostack can't reach it, and doesn't know about fixes or versions
withdrawn since. You can still install services from the old list.

**Check the service registry for services and fixes** is on unless you
turn it off. Off, you can't install services from the registry, and Roostack
won't hear when a version is withdrawn because it's unsafe. Services you've
installed will keep running: each runs from the lab's own copy of its
package.

**Let Roostack stop a service the registry says is dangerous** is on unless
you turn it off. When a version turns out to be malicious or is being
attacked with no fix yet, the registry can say so, and Roostack will stop
any service running that version until you update it or start it again. It
will keep everything the service stores. Off, Roostack will only warn you,
and the service will keep running. What each kind of withdrawal means is
under
[Change a service](../services/settings.md#when-the-registry-withdraws-a-version).

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

    Turning stops off saves `registry.no_stops: true` in `lab.yaml`.

## Changes Roostack didn't make

Roostack looks after the whole server. If someone changes it by hand in a
way that could stop updates or the firewall working, the page will list it
under **Changes Roostack didn't make**:

- software from outside Debian (extra package sources; a cloud provider's
  own copy of Debian, such as `mirrors.linode.com`, counts as Debian);
- packages held back from updates, which then don't get security fixes;
- another firewall, whose rules apply as well as Roostack's;
- Roostack's own firewall missing.

If the changes were on purpose, choose **I made these changes**. Roostack
will stop warning about them until something else changes.

??? note "For technical users"

    Updates run through `roostack-agent`, as a transient systemd unit
    (`roostack-os-updates`): `apt-get update`, then `unattended-upgrade -v`,
    with its output streamed to the page from
    `/run/roostack-agent/os-updates.log`. The unit doesn't depend on the
    agent, because updating Roostack restarts both its processes. The unit
    writes its exit status to `/run/roostack-agent/os-updates.exit`, and
    Roostack, once restarted, waits for the run to end and records it (the
    page then says Roostack updated itself, with the whole run's log). A
    restart is needed when
    `/run/reboot-required` exists (the packages are in
    `/run/reboot-required.pkgs`). All maintenance (backups, updates,
    restarts, restores) goes through one queue, one job at a time, so a
    restart never interrupts a backup.

    Host checks: apt sources that aren't Debian's mirrors, `apt-mark`
    holds, nftables tables other than Roostack's (or ufw/firewalld), and
    whether Roostack's `inet roostack` table is loaded.
