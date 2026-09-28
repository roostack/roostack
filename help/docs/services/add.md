# Add a service

1. On **Services**, choose **Add a service**. You'll see everything you can
   install; ones you already have are marked **Installed**.
2. Choose **Install** next to the one you want.
3. Fill in the form:

    Run it on
    :   Which of your servers runs it, if you have more than one. Its data
        stays on that server, and is backed up there.

    Web address
    :   The first part of its address, such as `passwords` for
        `passwords.smith.family`. Letters, digits and dashes.

    Settings
    :   Anything the service needs to know from the start, such as a name
        for its first administrator account. Passwords you type here are
        kept locked away on the server, never in a file anyone can read;
        ones only needed for setup (such as the first administrator's) are
        deleted once setup has finished.

4. Choose **Install**.

Roostack downloads the service, starts it and gives it its web address.
Downloading can take a few minutes the first time. You can watch it on the
home page; the service shows **starting**, then **running**.

## Finishing setup

Some services need a step after they start, such as creating their first
administrator account. The service's page says **Finishing setup** while
Roostack does this, which can take a few minutes after installing.

If a setup step fails, the page shows what went wrong, with **Try again**.
If it was something you typed (an invalid username, say), open **Change
what you entered, then try again**, correct it, and choose **Try again with
these**. Passwords you leave empty stay as you typed them before.

## Opening it

Once it's running, open its address from the service's page (or the home
page). The first visit may take a few seconds while its secure certificate
is ready.

??? note "For technical users"

    Installing writes the service's *desired state* to
    `/var/lib/roostack/services/<name>.yaml`: its package version, the server
    it runs on (`host`), its web address and a local port (allocated once,
    from 20000–29999), and its settings. Secrets (generated or typed) become
    Podman secrets named `roostack-<name>-<setting>`, never files; the
    package's `install_only` ones are deleted once the install hooks
    succeed.

    Roostack then applies the change on the service's server: it generates
    the Quadlet files in `~roostack/.config/containers/systemd/`
    and a Caddy snippet in `/var/lib/roostack/caddy/sites/<name>.caddy`,
    reloads systemd and starts the units. The apply's log shows on the home
    page while it runs.

    Setup steps ("install hooks") run with `podman exec` inside the
    service's containers, retried every 10 seconds for up to 10 minutes
    while the app starts. Values you typed reach them only as environment
    variables, passed on standard input. Their progress is in the database
    (`settings` table, `hooks.<name>.install`).

    See exactly what a service generates with
    [`roostack render`](../reference/cli.md#looking-at-packages).
