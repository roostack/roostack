# When things go wrong

## Needs attention

When something needs you, the home page says so under **Needs attention**,
with a link to where it's fixed. When there's nothing, it says
**Everything's working**. Red items are problems; amber ones are worth a
look soon; the rest are just letting you know.

The ones you're most likely to see:

Backups aren't set up
:   Until they are, a failed disk means losing everything. Set up
    [your backup plan](../backups/index.md).

The last backup didn't finish / No backup has finished in the last two days
:   The message says why. Often a backup drive that isn't plugged in, or a
    full disk. **View backups** shows the details; fix the cause, then
    **Back up now**.

Backups are only stored on …
:   Your plan has no copy away from home. Add one: see
    [Where backups can go](../backups/places.md).

The last change didn't finish
:   Something went wrong applying a change, such as a service that
    couldn't be downloaded. The message says what went wrong, and
    **View services** shows which service it was.

*Service* didn't finish setting up
:   A setup step failed. The service's page shows what went wrong, with
    **Try again**. See [Add a service](../services/add.md#finishing-setup).

Devices at home can't find your services yet
:   See [Reaching your services from home](../settings/domain.md#reaching-your-services-from-home).

Your lab has no address yet
:   Its services run, but can't be reached on the web until it has one: see
    [Your lab's address](../settings/domain.md).

*Server*'s firewall is off until you choose who can log in to it over SSH
:   Choose, and the firewall comes on: see
    [First-run setup](../start/first-run.md#4-who-can-log-in-to-this-server).

Changes Roostack didn't make
:   See [Updates and restarts](../settings/updates.md#changes-roostack-didnt-make).

## A service shows a chicken instead of itself

When a service can't answer, its address shows a picture and a short
explanation instead. What they mean:

Asleep on the job (502)
:   The service isn't answering: it's stopped, or still starting after a
    restart. Check it on its page in Roostack (the link is on the page,
    when you're at home); **Restart** often helps.

Still hatching (503)
:   It's starting up. The page tries again by itself.

Taking its sweet time (504)
:   It took too long to answer, often because it's busy with something big.
    Try again in a moment.

Too many chickens (403, 429)
:   Your address sent too many requests, or failed to log in too often, so
    it's blocked or slowed down for a while. If it wasn't you, wait and try
    again.

Nobody roosts here (404)
:   No service uses this address. Check the link, or the service's address
    on its page in Roostack.

At home, the page also has **Details**: the error itself, for working out
what went wrong. Visitors from outside see only the picture and the words.

## Other problems

- [Locked out](locked-out.md): a forgotten password or a lost phone.
- [Restore a whole lab](restore-lab.md): the server died, or was lost.
- A service's data is broken: [Get something back](../backups/restore.md).

??? note "For technical users"

    Start with the logs:

    ```bash
    journalctl -u roostack -u roostack-agent --since today
    ```

    A service's containers, and their units:

    ```bash
    sudo -u roostack XDG_RUNTIME_DIR=/run/user/$(id -u roostack) systemctl --user status 'roostack-<service>-*'
    sudo journalctl _UID=$(id -u roostack) -t roostack-<service>-<container>
    ```

    `roostack apply -dry-run` shows whether the server matches its
    settings. See [Where files live](../reference/files.md) and
    [Command line](../reference/cli.md).
