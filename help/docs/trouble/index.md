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

Your domain isn't set up
:   Services can't get secure addresses until it is: see
    [Your domain](../settings/domain.md).

*Server*'s firewall is off until you choose who can log in to it over SSH
:   Choose, and the firewall comes on: see
    [First-run setup](../start/first-run.md#4-who-can-log-in-to-this-server).

Changes Roostack didn't make
:   See [Updates and restarts](../settings/updates.md#changes-roostack-didnt-make).

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
