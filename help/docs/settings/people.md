# People and access

Everyone who uses Roostack has their own account. With
[single sign-on](../services/single-sign-on.md) they use it for the lab's
services too; otherwise each service (Vaultwarden, Forgejo, …) has its own
accounts.

## Roles

| | Owner | Admin | Member |
|---|:-:|:-:|:-:|
| Open services, and look after their own account in Your account | ✓ | ✓ | ✓ |
| Add, change, and remove services; back up now | ✓ | ✓ | |
| Settings, people, servers, restores, updates | ✓ | | |

Keep at least one **Owner**: Roostack won't let you remove or demote the
last one. Give someone **Admin** if they help look after the services, and
**Member** if they only use them. Members use Your account rather than
Roostack's pages: anyone who opens Roostack's pages as a Member will be
sent there.

## Adding someone

On **Settings → People**, choose **Add someone…**, then type their username
and choose what they can do (Member, Admin, or Owner). Roostack will show a
**temporary password**, once. Give it to them in person or in a message;
they'll choose their own when they first log in.

## Changing someone

Under **Change…** next to a person:

Save role
:   It will change what they can do straight away.

Give a new temporary password
:   For a forgotten password. They'll be logged out everywhere and choose a
    new one when they log in.

Turn off two-factor (lost phone)
:   For a lost or replaced phone. They'll be logged out everywhere; an
    Owner will set two-factor up again at their next login.

Remove
:   They can't log in any more.

## Names, emails and groups

Under **Change…**, a person's **full name** and **email address**: apps
with single sign-on call them by the name and find or make their account by
the email. Their **groups** say which services with single sign-on they can
use.

**Groups**, such as Family or Friends, are made on the same page. A
group's **Change…** sets who's in it and the services it can use.

## Two-factor authentication

Two-factor asks for a six-digit code from an app on your phone as well as
your password. **Owners must use it** because they can change everything,
and so must Admins while Roostack's pages can be reached from the
internet. Anyone else can turn it on in Your account, under **Security**.

## Your account

Everyone has a section of their own, **Your account**, at `my.` and your
lab's address (such as `my.smith.family`), in your lab's look. Before your
lab has an address, it's at `/account` on Roostack's own address. Its tabs:

Services
:   The services you can use, each opening at its address. Those with
    single sign-on sign you in with your Roostack account; those marked
    **Its own sign-in** have their own accounts. Owners and Admins also
    see **Run the lab**, which opens Roostack's pages.

Account
:   Your name and email address, which you change yourself, and your
    username, role, and groups, which an Owner changes. **Light or dark**
    here is for Your account and Roostack's pages alike.

Security
:   Change your password (your other devices will need to sign in again),
    turn on two-factor or move it to a new authenticator app (which asks
    for your password too), and **Where you're signed in**: each browser
    you're signed in on, when it was last used, and **Sign out** for each,
    or everywhere else at once.

Notifications
:   Which emails Roostack sends you (see
    [Notifications](email.md#notifications)).

Owners and Admins choose the colours of Roostack's pages for themselves on
Roostack's **Appearance** page, at the bottom of the sidebar. Their name
there opens Your account.

If you're locked out, see [Locked out](../trouble/locked-out.md).

??? note "For technical users"

    Accounts are in the `users` table of `/var/lib/roostack/roostack.db`,
    and sign-ins in the `sessions` table, with each one's browser and
    system shortened (`device`, such as "Firefox on Linux") and when it
    was last used (`last_seen`, saved at most every 30 seconds). Your
    account's pages have no scripts.
    Passwords are hashed with argon2id (64 MiB, 3 passes); at least 12
    characters. Sessions last 7 days, in an `HttpOnly` cookie; only a
    SHA-256 hash of the token is stored. Each two-factor code works once.
    Everything people change is recorded in the audit log (the
    `audit_log` table).

    People can also be managed from the server's command line: see
    [`roostack user`](../reference/cli.md#people).

## Signing in

People are signed out after a while without activity: 30 minutes, unless
an Owner chooses otherwise under **Signing in to Roostack** at the bottom
of **Settings → People** (15 minutes to 4 hours, or never). Opening a page or
sending a form counts as activity; pages refreshing themselves don't, so
a screen left open goes back to the login page when the time's up.

On the login page, **Keep me signed in** will keep that browser signed in
for 30 days, without the time limit. Use it only on a device that's yours
alone. An Owner can stop offering it, under **Signing in**.

Once your lab has an address, Roostack's pages will sign in through the
lab's sign-in page at `my.` instead, so one sign-in is for Your account,
Roostack's pages, and services with
[single sign-on](../services/single-sign-on.md).
There, Members stay signed in for as long as their sign-in lasts; Owners
and Admins still have the time limit and will then enter their two-factor
code to carry on
([Roostack's own pages](../services/single-sign-on.md#roostacks-own-pages)).
