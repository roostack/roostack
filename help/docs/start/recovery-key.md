# Your recovery key

Your backups are encrypted: scrambled so that nobody who gets hold of them
(the online storage company, or someone who finds your USB drive) can read
them. The **recovery key** is what unscrambles them.

!!! danger "Without it, your backups can't be opened"

    If the server is lost or breaks, you need the recovery key to get your
    data back. Nobody else has a copy, and Roostack can't reset it.

## When you get it

Roostack makes your recovery key when you set up your first backup (in the
setup wizard, or later on the **Backups** page), and shows it straight
away. It asks you to confirm you've saved it before carrying on. Adding more backups, or changing where they go, doesn't change
the key: it opens all of them.

## Keeping it safe

- **Print it** (there's a **Print** button), or write it down, and keep it
  somewhere safe, away from the server: if the house burns down, the key
  mustn't burn with it.
- Or save it in your **password manager**.
- If someone else set up Roostack for you, make sure **you** have a copy
  too, and they do.

## When you'll need it

- To [restore a whole lab](../trouble/restore-lab.md) on a new computer,
  after the old one broke or was lost.
- That's the only time. Everyday backups and restores on a working server
  don't ask for it: Roostack keeps a copy on the server itself.

## Seeing it again

Lost your copy? On the **Backups** page, under **Recovery key**, the Owner
can choose **Show recovery key…**. Roostack asks for your password and a
code from your authenticator app first, even though you're logged in, so
that someone who finds your computer logged in can't read it. Each time
it's shown is recorded in the audit log.

After five wrong passwords or codes, Roostack logs you out. Wrong codes
here also count towards the limit on your account (see
[Locked out](../trouble/locked-out.md#too-many-wrong-two-factor-codes)).

??? note "For technical users"

    The key is kept in `/var/lib/roostack/backup-key` (mode 0600, owned by
    `roostack`) and is the password of every
    [restic](https://restic.net) repository Roostack creates. Other servers
    in the lab never see it: each server's repository also gets that
    server's own key, and the controller uses the recovery key to create
    and prune them.

    Showing it again (`/backups/recovery-key`) needs the Owner's password
    and an unused two-factor code, is never cached by the browser
    (`Cache-Control: no-store`), and is recorded in the audit log (the
    `audit_log` table: "recovery key", "shown" or "wrong password or
    code"). The setup page that first shows it (`/setup/recovery-key`)
    works only until the key's been confirmed.

    The key file is left out of Roostack's own backups (a backup can't
    contain the key needed to open it), so the copy you keep is the only
    one outside the server.

    With restic installed, you can read your backups without Roostack at
    all:

    ```bash
    restic -r <repository> snapshots   # enter the recovery key when asked
    ```
