# First-run setup

The first time you open Roostack, it asks a few questions to get your
server ready. It takes about ten minutes. You can leave the domain and the
backups for later, but Roostack will remind you until they're done.

## Before you start

You'll need:

- The **setup code**. The installer shows it when it finishes, and
  Roostack writes it in its log (*For technical users* below says where).
  It stops anyone else on your network from setting up Roostack before you
  do.
- An **authenticator app** on your phone, such as Google Authenticator,
  Microsoft Authenticator, or the one in your password manager.
- If you want a web address for your services: your **domain** and a login
  for the company that runs its DNS. [Your domain](../settings/domain.md)
  explains what that means.
- If you want to set up backups now: a **USB drive**, a second disk, or an
  online storage account.

!!! note "Opening Roostack for the first time"

    For now, you open Roostack the first time through an SSH connection to
    the server (see *For technical users* below). A simpler way, from any
    device on your home network, is still being built.

## 1. Create your account

Enter the setup code, then choose a username and a password of at least 12
characters. You can also give your lab a name, like "The Smiths' lab". This
account is the **Owner**: the one person who can change everything.

Under the form there's **Restore a lab from its backups…**. Choose that
instead if you're rebuilding a lab you had before (see
[Restore a whole lab](../trouble/restore-lab.md)).

## 2. Turn on two-factor authentication

The Owner can change everything, so Roostack asks for a second proof that
it's you each time you log in: a six-digit code from your phone.

1. Open your authenticator app and add a new account.
2. Scan the square code on the screen, or type the key shown under it.
3. Enter the six-digit code the app shows.

Keep your phone safe. If you lose it, see
[Locked out](../trouble/locked-out.md).

## 3. Where is this server?

Choose the server's time zone. Roostack uses it for everything it shows and
schedules, such as the nightly backup at 2 am. If your device is in a
different time zone, Roostack points it out.

## 4. Who can log in to this server?

This is about **SSH**, a way for someone to log in to the server from
another computer to fix things Roostack can't. Roostack's firewall blocks
everything else apart from your services' web addresses.

Only from my home network *(recommended)*
:   From computers at home, or over a VPN.

From anywhere
:   Only reachable from the internet if your router sends port 22 to this
    server. Anyone can then try to log in, so use SSH keys, not passwords.

Nobody
:   If something goes wrong that Roostack can't fix, you'll need a keyboard
    and screen plugged into the server.

If you're connected over SSH right now, Roostack shows it, and warns you if
your choice would cut that connection.

## 5. Your domain

Your services each get their own web address under your domain, such as
`photos.smith.family`, with a secure (HTTPS) certificate. Roostack needs to
prove to the certificate company that the domain is yours, so it asks for a
login to your DNS provider and checks it works before saving it.

Choose **Set this up later** to skip this for now. Your services will still
run, but you can't open them by name until a domain is set up. See
[Your domain](../settings/domain.md).

## 6. Your backup plan

Choose where your backups go: a USB drive, a second disk, online storage,
or a friend's Roostack. Roostack lists the drives it can see, best first.
[Your backup plan](../backups/index.md) explains the choices.

Choose **Set up backups later** to skip this for now.

## 7. Save your recovery key

If you set up backups, Roostack shows your **recovery key**. It's the only
way to open your backups if this server is lost. Write it down or save it
in your password manager, then confirm you have. See
[Your recovery key](recovery-key.md).

## When you finish

Roostack restarts once to use your new settings: this takes a few seconds,
and the page carries on by itself. Then you'll see the home page, ready for
[your first service](../services/add.md).

??? note "For technical users"

    **Opening the UI the first time.** The UI listens on `127.0.0.1:8480`
    on the server only. Until a domain is set up, reach it with an SSH
    tunnel from your computer, then open <http://127.0.0.1:8480>:

    ```bash
    ssh -L 8480:127.0.0.1:8480 you@your-server
    ```

    **The setup code** is shown when the package finishes installing (run
    `sudo /usr/share/roostack/installed-message` to see it again), printed in
    the log (`journalctl -u roostack`) and
    saved in `/var/lib/roostack/setup-token` (readable by root and the
    `roostack` user only). It's deleted once the Owner exists.

    **What each step changes:**

    | Step | Where it ends up |
    |---|---|
    | Account | The `users` table in `/var/lib/roostack/roostack.db` (the password as an argon2id hash) |
    | Time zone | The host's time zone, set by `roostack-agent` with `timedatectl` |
    | Remote access | The nftables firewall, applied by `roostack-agent` and saved in `/etc/roostack/firewall.nft` |
    | Domain | `/var/lib/roostack/lab.yaml`; the DNS credentials as Podman secrets |
    | Backups | The recovery key in `/var/lib/roostack/backup-key`; the destination in the database |

    Settings from steps 3 to 6 take effect together at the restart at the
    end, so you don't watch Roostack restart after every step.
