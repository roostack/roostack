# People and access

Everyone who uses Roostack has their own account. These are only for
Roostack itself: each service (Vaultwarden, Forgejo, …) has its own
accounts too.

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

On **Settings → People**, add their username and choose a role. Roostack
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
