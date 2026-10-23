# Services

A **service** is an app Roostack runs for you, such as a password manager or
a photo library. Each one gets its own web address under your domain, is
backed up every night, and is kept up to date.

The **Services** page lists them, with how each is doing:

| Status | Meaning |
|---|---|
| running | Working normally |
| starting | Just started, or restarting after a change; give it a minute |
| stopped | Not running. Roostack will start it again when it next applies changes |
| failed | It stopped with an error. It shows under **Needs attention** on the home page |
| unknown | Roostack couldn't find out, for example because its server can't be reached |

Everyone with an account can see the list and open the services. Owners
and Admins can add, change, and remove them.

- [Add a service](add.md)
- [Change a service](settings.md)
- [Remove a service](remove.md)

## What you can install

| Service | What it's for |
|---|---|
| Vaultwarden | A password manager that works with the Bitwarden apps |
| Forgejo | Your own git hosting, with issues and pull requests, like GitHub |
| Immich | Photo and video backup from your phone, like Google Photos |

More are coming.

!!! note "Private for now"

    Services are reachable from your home network (and over a VPN into it),
    not from the internet. Making a service public comes later.
