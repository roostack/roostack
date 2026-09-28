# Locked out

## You forgot your password

**If there's another Owner**, they can give you a new temporary password:
**Settings → People**, **Change…** next to you, **Give a new temporary
password**.

**If you're the only Owner**, you reset it on the server itself. Log in to
the server (over SSH, or with a keyboard and screen plugged in), then:

```bash
sudo roostack user reset-password <your username>
```

It prints a temporary password. Log in with it, and Roostack asks you to
choose a new one. You're logged out everywhere else.

## You lost your phone (two-factor)

**If there's another Owner**, they can turn off your two-factor: **Settings
→ People**, **Change…** next to you, **Turn off two-factor (lost phone)**.

**If you're the only Owner**, on the server:

```bash
sudo roostack user reset-2fa <your username>
```

Log in with your password, and set two-factor up again with your new phone.

## "Too many wrong codes"

After five wrong two-factor codes, Roostack asks for your password again.
Check your phone's clock is right: authenticator codes depend on it.

## You can't log in to the server either

If remote access is set to **Nobody**, or you can't reach the server over
SSH, plug a keyboard and screen into it and log in there.

!!! tip "Two Owners"

    Making a second person an Owner (someone you trust) means one of you
    can always let the other back in.

??? note "For technical users"

    `roostack user` runs as root or as the `roostack` user, and works on
    the database directly (`/var/lib/roostack/roostack.db`), so it works
    even when Roostack isn't running. Both resets delete the person's
    sessions. It's recorded in the audit log.
