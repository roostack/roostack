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

!!! info "Easier installs are coming"

    Roostack isn't published yet, so for now you install it from a package
    file built from its source. Soon you'll be able to add Roostack's
    package source and install it with one command.

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

With Debian 13 installed, and logged in on the computer (or over SSH):

1. Copy the Roostack package file for your kind of computer to it:
   `roostack_<version>_amd64.deb` for a PC, `roostack_<version>_arm64.deb`
   for an ARM computer. Not sure? `dpkg --print-architecture` says which.
2. Install it. Debian fetches what Roostack needs (Podman, restic,
   WireGuard and a few others) at the same time:

    ```bash
    sudo apt install ./roostack_<version>_amd64.deb
    ```

    (with `arm64` in the name on an ARM computer).

    Logged in as `root` (as on some cloud servers)? Leave out `sudo`.

3. At the end, the installer shows your **setup code** and how to open
   Roostack's setup page. To see it again:

    ```bash
    sudo /usr/share/roostack/installed-message
    ```

Then carry on with [First-run setup](first-run.md).

??? note "For technical users"

    **What the package installs:** `/usr/bin/roostack` and
    `/usr/bin/roostack-agent`, their systemd units, the service catalog in
    `/usr/share/roostack/catalog`, and the Containerfile for Roostack's
    Caddy image. It depends on `podman (>= 5.4)`, `passt`, `nftables`,
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

    **Building the package** from the source: `make build`, then
    `scripts/build-deb.sh`, which writes `dist/roostack_<version>_amd64.deb`.
    For ARM, `GOARCH=arm64 make build` and the same script, which writes
    `…_arm64.deb`.

    See [Where files live](../reference/files.md) for everything Roostack
    creates.
