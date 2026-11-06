# Settings

**Settings** is for the Owner. It opens on an overview: a card for each
area, with how it stands and a coloured dot when something needs a look.

| Area | What's there |
|---|---|
| **People and access** | [People](people.md) who can log in, [single sign-on](../services/single-sign-on.md), and [remote access](../start/first-run.md#4-who-can-log-in-to-this-server) (who can log in to the server over SSH) |
| **Network** | [Your domain](domain.md) and its certificates, and how devices at home reach your services |
| **Each server** | [Updates and restarts](updates.md), time zone and remote access, per server |
| **Servers** | The computers in your lab: see [Servers](../servers/index.md) |
| **Storage for others** | Space for a friend's backups: see [Storage for other labs](../servers/storage-for-others.md) |
| **Name and look** | Your lab's name, logo, and colours (below) |

Your own account (password, two-factor authentication, colours) is under
**Account**, for everyone.

## Name and look

**Settings → Name and look** is how your lab shows itself: on the sign-in
page for services, on the public [status page](../servers/monitoring.md), and in
Roostack's own pages.

Name
:   What your lab is called, at the top of every page and to other labs
    you share backup storage with. Roostack will restart to use a new
    name, which will take a few seconds.

Logo
:   A square **mark**, shown beside the name in place of Roostack's hen
    and as the browser tab's icon; or a **wide logo** with the name in it,
    shown in its place, up to six times as wide as it is tall (at most 80
    pixels high on the sign-in and status pages, and 40 in Roostack's
    sidebar), with an optional version for dark mode and tab icon. Without
    a dark version, a wide logo will sit on a light panel when the device
    is in dark mode so that dark lettering stays readable. Or none:
    Roostack's hen. PNG or SVG, up to 200 KB; SVGs with scripts are
    refused.

Colours
:   One of Roostack's themes, for the sign-in and status pages, and for
    Roostack's pages for anyone who hasn't chosen their own under
    **Account → Appearance**. Light or dark follows each visitor's device.

??? note "For technical users"

    The files are in `/var/lib/roostack/sso/` (`look-wide.svg` and so
    on), served at `/brand/<part>` on Roostack's address and `/look/<part>`
    on the sign-in page's, and copied beside the status page's
    `index.html`. The choices are the `signin.*` settings in
    `/var/lib/roostack/roostack.db`.

- [People and access](people.md)
- [Your domain](domain.md)
- [Updates and restarts](updates.md)
