# Install Roostack

## What you need

- **A computer that stays on**, with nothing else on it: Roostack looks
  after the whole computer, including its firewall and updates. A small,
  quiet PC such as an Intel NUC is ideal. Roostack runs on x86-64
  computers (most PCs); Raspberry Pi and other ARM computers come later.
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

## Install it

With Debian 13 installed, and logged in on the computer (or over SSH):

1. Copy the Roostack package file (`roostack_<version>_amd64.deb`) to the
   computer.
2. Install it. Debian fetches what Roostack needs (Podman, restic,
   WireGuard and a few others) at the same time:

    ```bash
    sudo apt install ./roostack_<version>_amd64.deb
    ```

3. At the end, the installer shows how to open Roostack's setup page, and
   where to find the **setup code**.

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

    See [Where files live](../reference/files.md) for everything Roostack
    creates.
