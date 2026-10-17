# First-run setup

The first time you open Roostack, it asks a few questions to get your
server ready. It takes about ten minutes. You can leave your lab's address and the
backups for later, but Roostack will remind you until they're done.

## Before you start

You'll need:

- The **setup code**. The installer shows it when it finishes, and
  Roostack writes it in its log (*For technical users* below says where).
  It stops anyone else on your network from setting up Roostack before you
  do.
- An **authenticator app** on your phone, such as Google Authenticator,
  Microsoft Authenticator, or the one in your password manager.
- Nothing, for a free roost.ac address. To use **your own domain**
  instead: the domain and a login for the company that runs its DNS.
  [Your lab's address](../settings/domain.md) explains both.
- If you want to set up backups now: a **USB drive**, a second disk, or an
  online storage account.

!!! note "Opening Roostack for the first time"

    For now, you open Roostack the first time through an SSH connection to
    the server (see *For technical users* below). A simpler way, from any
    device on your home network, is still being built.

## 1. Create your account

Enter the setup code and choose **Continue**. Roostack then asks
**What's this server for?**:

A new lab
:   Choose a username and a password of at least 12 characters. You can
    also give your lab a name, like "The Smiths' lab". This account is the
    **Owner**: the one person who can change everything. Carry on below.

Add it to a lab you already have
:   For a second server: paste a join code from your lab, and this server
    joins it. See [Add a server](../servers/add.md). The rest of this page
    doesn't apply: you manage it from your lab.

Restore a lab from its backups
:   If you're rebuilding a lab you had before (see
    [Restore a whole lab](../trouble/restore-lab.md)).

Nothing is chosen for you, since each is hard to undo.

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

Then say whether the server is **at home**, behind a router, or **on the
internet**, like a cloud server (a VPS). Roostack suggests an answer: a
server with a public address of its own looks like a cloud server. On the
internet, services start public and Roostack's own pages are at
`roost.<your domain>`; [Where your lab is](../settings/domain.md#where-your-lab-is)
explains the difference.

## 4. Who can log in to this server?

This is about **SSH**, a way for someone to log in to the server from
another computer to fix things Roostack can't. Roostack's firewall blocks
everything else apart from your services' web addresses.

Only from my home network *(recommended at home)*
:   From computers at home, or over a VPN.

From anywhere *(recommended on a cloud server)*
:   At home, only reachable from the internet if your router sends port 22
    to this server. Anyone can then try to log in, so use SSH keys, not
    passwords. On a cloud server, this is how you log in.

Nobody
:   If something goes wrong that Roostack can't fix, you'll need a keyboard
    and screen plugged into the server.

If you're connected over SSH right now, Roostack shows it, and warns you if
your choice would cut that connection.

Then choose **how they log in** (not asked for "Nobody", or on a server
with no SSH server):

With an SSH key only *(recommended)*
:   Passwords are refused, so nobody can guess one, and nobody can log in as
    `root`: log in as yourself and use `sudo`. Roostack offers this once
    it has seen someone log in with a key, as an account that can use
    `sudo`, so turning passwords off can't lock you out. Until then, see
    [Set up an SSH key](#set-up-an-ssh-key), then choose **check again**.

With a key or a password
:   SSH works as before. Anyone who can reach it can try to guess a
    password, so Roostack shows a warning on the home page until it's keys
    only.

With "Nobody", it's keys only straight away: nothing can reach SSH, so
there's nobody to lock out, and if you open SSH later passwords are
already off. Change any of this later in **Settings → Remote access**
(for each server).

### Set up an SSH key

An SSH key is a pair of files on your own computer: a private key that
never leaves it, and a public key you copy to the server. On your computer
(macOS, Linux, or Windows' PowerShell):

1. **Make a key**, if you don't have one (press Enter to accept where it
   goes, and choose a passphrase):

    ```bash
    ssh-keygen -t ed25519
    ```

2. **Copy it to the server**, using your account's name and the server's
   address (while it still takes passwords):

    ```bash
    ssh-copy-id you@192.168.1.20
    ```

    No `ssh-copy-id` (Windows)? Show the public key with
    `type $env:USERPROFILE\.ssh\id_ed25519.pub`, then on the server add
    that line to `~/.ssh/authorized_keys`.

3. **Log in with it** once: `ssh you@192.168.1.20` shouldn't ask for the
   server's password (it may ask for the key's passphrase). Then choose
   **check again** on the Remote access page.

The account must be able to use `sudo`, since `root` can't log in with
keys only. On a cloud server where you only log in as `root`, make an
account first, give it `sudo` and your key, then log in as it:

```bash
adduser you
usermod -aG sudo you
install -d -m 700 -o you -g you /home/you/.ssh
install -m 600 -o you -g you ~/.ssh/authorized_keys /home/you/.ssh/
```

## 5. Your lab's address

Your services each get their own web address, with a secure (HTTPS)
certificate. Choose one of:

- **A free roost.ac address**: type a name, such as `smiths`, and choose
  **Continue to Roostack Cloud**. Sign up or log in there and confirm; it
  brings you back, and setup carries on by itself. Services are at
  `photos.smiths.roost.ac`.
- **Your own domain**, such as `smith.family`: Roostack needs to prove to
  the certificate company that the domain is yours, so it asks for a login
  to your DNS provider and checks it works before saving it.

Choose **Set this up later** to skip this for now. Your services will still
run, but they can't be reached on the web until your lab has an address.
See [Your lab's address](../settings/domain.md).

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
    | Remote access | The nftables firewall, applied by `roostack-agent` and saved in `/etc/roostack/firewall.nft`; keys only in `/etc/ssh/sshd_config.d/10-roostack.conf` (at once, not at the restart) |
    | Domain | `/var/lib/roostack/lab.yaml`; the DNS credentials as Podman secrets |
    | Backups | The recovery key in `/var/lib/roostack/backup-key`; the destination in the database |

    Settings from steps 3 to 6 take effect together at the restart at the
    end, so you don't watch Roostack restart after every step.

    **Keys only** is one file, `/etc/ssh/sshd_config.d/10-roostack.conf`,
    with `PasswordAuthentication no`, `KbdInteractiveAuthentication no`
    and `PermitRootLogin no`. Debian's `sshd_config` reads that folder
    first and sshd keeps the first value it reads, so it wins over later
    files such as cloud images' `50-cloud-init.conf`. `roostack-agent`
    checks each change with `sshd -t` (putting the old file back if sshd
    refuses it) and then reloads SSH; logged-in sessions stay connected.
    What Roostack shows and warns about is what sshd really does, from
    `sshd -T`, so another file turning passwords back on is caught too
    (a `Match` block isn't). The key check reads sshd's `Accepted
    publickey` lines from the journal, only those journald says came from
    root running sshd (`_UID=0`, `_EXE=/usr/lib/openssh/sshd-session`),
    which nobody else can fake; the account must still exist, not be
    root, and be in the `sudo` group.
