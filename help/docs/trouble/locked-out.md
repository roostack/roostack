# Locked out

## You forgot your password

**If there's another Owner**, they can give you a new temporary password:
**Settings → People**, **Change…** next to you, **Give a new temporary
password**.

**If you're the only Owner**, you'll reset it on the server itself. Log in
to the server (over SSH, or with a keyboard and screen plugged in), then:

```bash
sudo roostack user reset-password <your username>
```

It will print a temporary password. Log in with it, and Roostack will ask
you to choose a new one. You'll be logged out everywhere else.

## You lost your phone (two-factor)

**If there's another Owner**, they can turn off your two-factor: **Settings
→ People**, **Change…** next to you, **Turn off two-factor (lost phone)**.

**If you're the only Owner**, on the server:

```bash
sudo roostack user reset-2fa <your username>
```

Log in with your password, and set two-factor up again with your new phone.

## The sign-in page won't open

If your lab uses [single sign-on](../services/single-sign-on.md),
Roostack's pages send you to the lab's sign-in page. If that page can't be
reached, add `/login?here` to Roostack's address (`https://roost.` and your
lab's address) for Roostack's own login page, or use the
[SSH tunnel](../start/first-run.md).

## "Too many wrong two-factor codes"

After five wrong two-factor codes in a row, Roostack will accept no code
for your account for 15 minutes, even the right one. The message says when
codes will work again. If it happens again, the wait will double each time,
up to a day. The right code starts the count again.

Check your phone's clock is right: authenticator codes depend on it.

If you didn't enter those codes, someone else knows your password. Change
it once you're back in (**Account → Change password**).

To get in sooner, `sudo roostack user reset-2fa <your username>` will turn
two-factor off for your account (and end the wait). Set it up again
straight after logging in.

??? note "For technical users"

    The count is per account, not per login, so logging in again doesn't
    give more tries. It's kept in the database (`totp_failures` and
    `totp_failed_at` in `users`), so a restart doesn't clear it. Codes
    entered on the **Show recovery key** page count too. Each lock is in
    the audit log ("2fa locked"). With five tries per lock and the longest
    lock a day, guessing a six-digit code isn't practical.

## You can't log in to the server either

If remote access is set to **Nobody**, or you can't reach the server over
SSH, plug a keyboard and screen into it and log in there (on a cloud
server, use your provider's web console). Your password works there even
with SSH set to keys only: that only changes SSH.

Lost your SSH key, with SSH set to keys only? Log in to Roostack and choose
**With a key or a password** under **Settings → Remote access**, or, on
the server itself, remove `/etc/ssh/sshd_config.d/10-roostack.conf` and
run `sudo systemctl reload ssh`. Then [set up a new
key](../start/first-run.md#set-up-an-ssh-key) and turn keys only back on.

!!! tip "Two Owners"

    Making a second person an Owner (someone you trust) means one of you
    can always let the other back in.

??? note "For technical users"

    `roostack user` works on the database directly
    (`/var/lib/roostack/roostack.db`), so it works even when Roostack isn't
    running. Started with `sudo`, it runs itself again as the `roostack`
    user, who owns that folder: root never touches files there, in case
    something put a symlink in their place. Both resets delete the
    person's sessions. It's recorded in the audit log.
