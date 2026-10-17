# Install Roostack

## What you need

- **A computer that stays on**, with nothing else on it: Roostack looks
  after the whole computer, including its firewall and updates. A small,
  quiet PC such as an Intel NUC is ideal. Roostack runs on x86-64
  computers (most PCs), and on 64-bit ARM computers that run Debian's own
  Debian 13, such as some ARM mini PCs and ARM cloud servers. Raspberry Pi
  comes later.
- **Debian 13**, freshly installed. It's free, and made for computers that
  run for years without anyone looking after them.
- **Enough disk space** for what you'll keep: your photos and videos need
  the most. A second disk or a USB drive is good for
  [backups](../backups/index.md).
- A connection to your home network, preferably by cable.

## Install Debian

Roostack runs on **Debian 13** ("trixie"), installed fresh, with nothing
else on it. If your computer already runs Debian 13 for nothing else, skip
to [Install Roostack](#install-roostack). Debian's own
[installation guide](https://www.debian.org/releases/trixie/amd64/) covers
every step in detail (for an ARM computer, its
[ARM64 guide](https://www.debian.org/releases/trixie/arm64/)); this is the
short version.

=== "On your own computer"

    1. **Download** the installer from
       [debian.org/download](https://www.debian.org/download): the small
       "netinst" image for 64-bit PCs (`debian-13.…-amd64-netinst.iso`).
       It downloads the rest while it installs, so the computer needs a
       network connection.
    2. **Copy it onto a USB stick** of 1 GB or more, with a program such as
       [balenaEtcher](https://etcher.balena.io) or Rufus (on Windows).
       Everything on the stick is erased. Debian's guide explains other ways:
       [Preparing a USB stick](https://www.debian.org/releases/trixie/amd64/ch04s03.en.html#usb-copy-isohybrid).
    3. **Start the computer from the stick.** Plug it in, turn the
       computer on, and press the key for its boot menu (often F12, F11,
       F10 or Esc; it's shown briefly on the screen) to choose the stick.
       See [Booting from a USB stick](https://www.debian.org/releases/trixie/amd64/ch05s01.en.html#usb-boot-x86).
    4. **Answer the installer's questions.** Most defaults are right. The
       ones that matter for Roostack:

        Hostname
        :   The computer's name, such as `nuc`. Roostack calls the server
            by it (you can rename it in Roostack later).

        Root password
        :   **Leave it empty.** Your own account then gets `sudo`, which is
            how you'll install Roostack.

        Partitioning
        :   **Guided – use entire disk**, with **All files in one
            partition**. This erases the disk. Keep a second disk or USB
            drive for [backups](../backups/index.md) rather than
            splitting this one. See
            [Partitioning](https://www.debian.org/releases/trixie/amd64/ch06s03.en.html#di-partition).

        Software selection
        :   **Untick** "Debian desktop environment" (and GNOME): the server
            needs no screen. **Tick** "SSH server" and "standard system
            utilities". See
            [Installing additional software](https://www.debian.org/releases/trixie/amd64/ch06s03.en.html#di-install-software).

    5. **Remove the stick** when the installer says it's finished, and let
       the computer start Debian.
    6. **Give it a fixed address.** Log in, and find its address on your
       home network with `ip -br addr` (such as `192.168.1.20`). In your
       router's settings, reserve that address for it (often called a
       *DHCP reservation*), so it doesn't change.

=== "On a cloud server (VPS)"

    Most cloud providers (such as Hetzner, DigitalOcean or OVHcloud) offer
    Debian 13 when you create a server:

    1. **Create the server** in the provider's control panel, choosing
       **Debian 13** as its image (or operating system). A small x86-64
       server is enough to start with.
    2. **Add your SSH key** when asked, so you can log in without a
       password.
    3. **Log in** with SSH, as the provider shows (often as `root`, or as
       `debian`).
    4. **Check its firewall**, if the provider has one: allow SSH (TCP 22)
       for you. A server that joins a lab needs nothing else open; one that
       [runs the lab](../servers/index.md) also needs UDP 51820, and one
       offering [storage for other labs](../servers/storage-for-others.md)
       UDP 51821.

    If the provider doesn't offer Debian 13, Debian publishes its own
    [cloud images](https://cloud.debian.org/images/cloud/) (in the
    `trixie` folder: `genericcloud` suits most providers that accept
    uploaded images).

    !!! note "A cloud server isn't at home"

        Roostack's services are private: only for devices on your home
        network (or a VPN into it). So a cloud server works best as an
        extra server in a lab that has one at home, for example to keep
        your backups away from home. Making services public comes later.

## Install Roostack

With Debian 13 installed, log in on the computer (or over SSH) and run:

```bash
curl -fsSL https://dl.roostack.cloud/install.sh | sudo sh
```

It adds Roostack's package source to Debian and installs Roostack from it,
with what it needs (Podman, restic, WireGuard and a few others). This
takes a few minutes. From then on, Roostack is updated with the rest of
the system.

- Logged in as `root` (as on some cloud servers)? Leave out `sudo`.
- `curl: command not found`? Run `sudo apt install curl` first.
- It stops without changing anything if the computer isn't running
  Debian 13, or isn't a 64-bit PC or ARM computer.

??? note "What the command does, and doing it by hand"

    The installer is a short script you can read before running it:

    ```bash
    curl -fsSL https://dl.roostack.cloud/install.sh | less
    ```

    It downloads nothing but Roostack's release key, and does the same as
    these steps, which you can follow instead:

    1. **Get Roostack's release key and check it.** Packages from Roostack
       are signed with this key, so check it really is Roostack's before
       telling Debian to trust it (this needs `gpg`: `sudo apt install gpg`):

        ```bash
        curl -fsSLO https://apt.roostack.cloud/roostack-archive-keyring.gpg
        gpg --show-keys roostack-archive-keyring.gpg
        ```

        The long line of letters and digits it shows is the key's
        fingerprint. It must be exactly:

        ```text
        RELEASE-KEY-FINGERPRINT
        ```

        If it isn't, stop: that file isn't Roostack's key. If it is,
        install it where Debian keeps such keys:

        ```bash
        sudo install -m 644 roostack-archive-keyring.gpg /usr/share/keyrings/
        ```

        (The script checks the key differently, against the SHA-256 of the
        whole file written into it, since a fresh Debian has no `gpg`.)

    2. **Add Roostack's package source**, signed by that key only:

        ```bash
        sudo tee /etc/apt/sources.list.d/roostack.sources <<'EOF'
        # Roostack's own packages (SPEC.md §7), installed by the roostack package.
        # Suites is Roostack's release series: patch releases in it install by
        # themselves each night (52roostack-unattended-upgrades). When the Owner
        # accepts a new series on the Updates page, Roostack changes Suites to it.
        # You may point URIs at a mirror; keep Signed-By, which checks that the
        # packages are Roostack's.
        Types: deb
        URIs: https://apt.roostack.cloud
        Suites: SUITE
        Components: main
        Architectures: amd64 arm64
        Signed-By: /usr/share/keyrings/roostack-archive-keyring.gpg
        EOF
        ```

        Copy it whole, comments included: the package brings the same
        file, and apt asks about it if they differ. `Suites` is Roostack's
        release series: Roostack installs new versions in it by itself, and
        a new series waits until you accept it.

    3. **Install Roostack:**

        ```bash
        sudo apt update
        sudo apt install roostack
        ```

## When it's installed

At the end, the installer shows how to open Roostack's **setup page**:

- an address such as `https://192.168.1.20:8443`, to open in a browser on
  a computer or phone **on the same network** as the server;
- the **fingerprint** of the page's certificate, to check in the browser
  (below);
- your **setup code**, which you'll type on the setup page.

To see it again:

```bash
sudo /usr/share/roostack/installed-message
```

The setup page is only there until setup is finished, and for 24 hours at
most once your account exists. Then it closes, and Roostack tells you
where to find it from then on.

### Check the setup page's certificate

Your browser warns that the connection isn't private (or that there's a
security risk). That's expected: the server has no name yet, so the page
uses a certificate the server made for itself, which the browser can't
check. **You** check it instead, so you know it's your server answering,
not something else on the network:

1. In the warning, open its details and view the certificate (in Chrome
   and Edge, click the error code under the warning; in Firefox,
   **Advanced** then **View Certificate**; in Safari, **Show Details**
   then **view the certificate**).
2. Find its **SHA-256 fingerprint** and compare it with the one the
   installer showed: pairs of letters and digits, such as `3F:A2:…`.
   Checking the first and last few pairs is enough.
3. If they're the same, go on to the page (**Proceed**, **Accept the
   risk and continue** or **visit this website**). If they're not, don't
   type anything there: use the SSH way below instead, and ask someone
   technical to look at your network.

On a phone the certificate can be hard to find: a computer is easier.

### Not on the same network?

On a cloud server, or if the page doesn't open, reach the setup page
through SSH instead. From your computer (Windows, macOS or Linux), run
this in a terminal and leave it open, with your own username and the
server's address:

```bash
ssh -L 8480:127.0.0.1:8480 you@192.168.1.20
```

Then open <http://127.0.0.1:8480> in your browser. The installer shows
this command with the address filled in.

Then carry on with [First-run setup](first-run.md).

??? note "For technical users"

    **What the installer adds:** Roostack's release key,
    `/usr/share/keyrings/roostack-archive-keyring.gpg`, and its package
    source, `/etc/apt/sources.list.d/roostack.sources`. The package brings
    the same two files and looks after them from then on.

    **What the package installs:** `/usr/bin/roostack` and
    `/usr/bin/roostack-agent`, their systemd units, and the Containerfile for
    Roostack's Caddy image. The services you can install aren't in it: Roostack
    gets them from the [service registry](../settings/updates.md#services-you-can-install). It depends on `podman (>= 5.4)`, `passt`, `nftables`,
    `uidmap`, `crun`, `catatonit`, `dbus-user-session`, `restic`,
    `unattended-upgrades` and `wireguard-tools`.

    **What installing sets up** (`postinst`, safe to run again with
    `dpkg-reconfigure roostack`):

    - a system user `roostack` (home `/var/lib/roostack/home`, no login
      shell) with subordinate IDs 200000–265535 for rootless containers,
      and in the `render` and `video` groups (for GPUs);
    - lingering for `roostack`, so its containers start at boot with nobody
      logged in;
    - `/var/lib/roostack` and `/srv/roostack` (owned by `roostack`, mode
      0750) and a starting `lab.yaml`;
    - a drop-in, `/etc/systemd/system/roostack.service.d/user.conf`, giving
      the service the `roostack` user's runtime directory;
    - it enables and starts `roostack-agent` and `roostack`.

    **The setup page on the home network** is served by `roostack` on port
    8443, over HTTPS, while first-run setup lasts: only the first-run pages,
    only to private addresses (the firewall opens the port to the home
    network only), and only to an address in the browser, never a name. Its
    certificate is made when Roostack first starts
    (`/var/lib/roostack/setup-tls.crt`, its key `setup-tls.key`), and the
    log shows the address and fingerprint too
    (`journalctl -u roostack | grep "setup pages"`). When setup is finished,
    or 24 hours after the Owner was made, the listener stops, the port
    closes, the certificate is deleted and its sessions are signed out.
    Compute the fingerprint yourself with
    `openssl x509 -in /var/lib/roostack/setup-tls.crt -noout -fingerprint -sha256`.

    **Building the package** from the source: `make build`, then
    `scripts/build-deb.sh`, which writes `dist/roostack_<version>_amd64.deb`.
    For ARM, `GOARCH=arm64 make build` and the same script, which writes
    `…_arm64.deb`. Install a package file with
    `sudo apt install ./roostack_<version>_amd64.deb`. To try `install.sh`
    against a test repository, set `ROOSTACK_REPO` (its URL),
    `ROOSTACK_SUITE` and `ROOSTACK_KEY_SHA256` (the SHA-256 of its
    `roostack-archive-keyring.gpg`).

    See [Where files live](../reference/files.md) for everything Roostack
    creates.
