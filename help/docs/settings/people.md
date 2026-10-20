# People and access

Everyone who uses Roostack has their own account. With
[single sign-on](../services/single-sign-on.md) they use it for the lab's
services too; otherwise each service (Vaultwarden, Forgejo, …) has its own
accounts.

## Roles

| | Owner | Admin | Member |
|---|:-:|:-:|:-:|
| See services and open them | ✓ | ✓ | ✓ |
| Add, change and remove services; back up now | ✓ | ✓ | |
| Settings, people, servers, restores, updates | ✓ | | |

Keep at least one **Owner**: Roostack won't let you remove or demote the
last one. Give someone **Admin** if they help look after the services, and
**Member** if they only use them.

## Adding someone

On **Settings → People**, choose **Add someone…**, then type their username
and choose what they can do (Member, Admin or Owner). Roostack
shows a **temporary password**, once. Give it to them in person or in a
message; they choose their own when they first log in.

## Changing someone

Under **Change…** next to a person:

Save role
:   Changes what they can do, straight away.

Give a new temporary password
:   For a forgotten password. They're logged out everywhere, and choose a
    new one when they log in.

Turn off two-factor (lost phone)
:   For a lost or replaced phone. They're logged out everywhere; an Owner
    sets two-factor up again at their next login.

Remove
:   They can't log in any more.

## Names, emails and groups

Under **Change…**, a person's **full name** and **email address**: apps
with single sign-on call them by the name, and find or make their account
by the email. Their **groups** say which services with single sign-on they
can use.

**Groups**, such as Family or Friends, are made on the same page. A
group's **Change…** sets who's in it and the services it can use.

## Two-factor authentication

Two-factor asks for a six-digit code from an app on your phone as well as
your password. **Owners must use it**, because they can change everything.
Admins and Members are reminded to turn it on, under **Account**.

## Your own account

Under **Account**, everyone can:

- **change their password** (this logs out their other devices);
- **turn on two-factor**;
- choose **light or dark**, and a **colour theme**.

If you're locked out, see [Locked out](../trouble/locked-out.md).

??? note "For technical users"

    Accounts are in the `users` table of `/var/lib/roostack/roostack.db`.
    Passwords are hashed with argon2id (64 MiB, 3 passes); at least 12
    characters. Sessions last 7 days, in an `HttpOnly` cookie; only a
    SHA-256 hash of the token is stored. Each two-factor code works once.
    Everything people change is recorded in the audit log (the
    `audit_log` table).

    People can also be managed from the server's command line: see
    [`roostack user`](../reference/cli.md#people).

## Signing in

People are signed out after a while without activity: 30 minutes, unless
an Owner chooses otherwise under **Signing in** at the bottom of
**Settings → People** (15 minutes to 4 hours, or never). Opening a page or
sending a form counts as activity; pages refreshing themselves don't, so
a screen left open goes back to the login page when the time's up.

On the login page, **Keep me signed in** keeps that browser signed in for
30 days, without the time limit. Use it only on a device that's yours
alone. An Owner can stop offering it, under **Signing in**.
