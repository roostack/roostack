#!/bin/bash
# Starts an offline VM for the key ceremonies (docs/REGISTRY-KEYS.md and
# docs/RELEASING.md), with scripts/ceremony.sh to run inside it.
#
#   make build && scripts/ceremony-vm.sh [USB-DISK...]
#   e.g. scripts/ceremony-vm.sh /dev/sdb /dev/sdc
#
# Needs QEMU with KVM and its GTK window (Arch: qemu-system-x86 and
# qemu-ui-gtk; Debian: qemu-system-x86 and qemu-system-gui), e2fsprogs,
# gpg, curl and sudo (to give the VM the USB disks).
#
# The VM has no network device and no disk of its own: it boots Debian's
# live image (downloaded once, its signature checked against Debian's CD
# signing keys) and runs in RAM. It sees three things:
#   - "tools", read-only: bin/roostack, ceremony.sh, and the two docs, for
#     the first ceremony and for the later procedures by hand;
#   - "out", writable: where ceremony.sh leaves the public keys and the CI
#     secrets, for this script to copy out when the VM stops;
#   - the USB disks given, as whole USB drives, for the offline copies
#     (unmounted on the host while the VM runs).
# The disks and what comes out are kept in /dev/shm (RAM), so no key
# reaches the host's disk or its snapshots, and go at the next reboot;
# Debian's image is kept in ~/.cache/roostack-ceremony, to download once.
# Tested on 2026-10-27 with throwaway keys and emulated USB drives
# (QEMU_EXTRA, below), then used for the first ceremony.
set -euo pipefail

REPO=$(cd "$(dirname "$(readlink -f "$0")")/.." && pwd)
BIN=${ROOSTACK_BIN:-$REPO/bin/roostack}
WORK=${CEREMONY_WORK:-/dev/shm/roostack-ceremony}
CACHE=${XDG_CACHE_HOME:-$HOME/.cache}/roostack-ceremony
LIVE=https://cdimage.debian.org/debian-cd/current-live/amd64/iso-hybrid
# Debian's CD signing keys (https://www.debian.org/CD/verify).
DEBIAN_CD_KEYS=(DF9B9C49EAA9298432589D76DA87E80D6294BE9B 10460DAD76165AD81FBC0CE9988021A964E6EA7D)

die() { printf 'Stopped: %s\n' "$*" >&2; exit 1; }

# A window for the VM: without one QEMU falls back to a VNC server.
if [ -z "${QEMU_EXTRA:-}" ] && ! qemu-system-x86_64 -display help 2>/dev/null | grep -qx gtk; then
	die "QEMU has no GTK window here: install it (Arch: qemu-ui-gtk; Debian: qemu-system-gui)"
fi

# The roostack binary: a Linux amd64 build, from a commit you've reviewed.
[ -x "$BIN" ] || die "no $BIN: run make build in $REPO first"
file -b "$BIN" | grep -q 'ELF 64-bit.*x86-64' || die "$BIN isn't a Linux amd64 binary"
echo "roostack: $BIN"
echo "  built from: $(git -C "$REPO" describe --always --dirty 2>/dev/null || echo unknown) (the checkout now; make build again if it moved)"
echo "  sha256: $(sha256sum "$BIN" | cut -d' ' -f1)"

# Debian's live image, checked: the signature on SHA512SUMS by one of the
# pinned keys, then the image's line in it.
mkdir -p "$CACHE"
cd "$CACHE"
ISO=$(curl -fsS "$LIVE/" | grep -oE 'debian-live-[0-9.]+-amd64-standard\.iso' | head -1)
[ -n "$ISO" ] || die "couldn't find the live image at $LIVE"
if [ ! -f "$ISO" ]; then
	curl -fL --progress-bar -o "$ISO.part" "$LIVE/$ISO"
	mv "$ISO.part" "$ISO"
fi
curl -fsS -o SHA512SUMS "$LIVE/SHA512SUMS"
curl -fsS -o SHA512SUMS.sign "$LIVE/SHA512SUMS.sign"
gnupg=$(mktemp -d)
trap 'rm -rf "$gnupg"' EXIT
GNUPGHOME=$gnupg gpg -q --keyserver hkps://keyring.debian.org --recv-keys "${DEBIAN_CD_KEYS[@]}"
signer=$(GNUPGHOME=$gnupg gpg --status-fd 1 --verify SHA512SUMS.sign SHA512SUMS 2>/dev/null | awk '/VALIDSIG/ {print $NF}')
printf '%s\n' "${DEBIAN_CD_KEYS[@]}" | grep -qx "$signer" || die "SHA512SUMS isn't signed by Debian's CD signing key"
grep " $ISO\$" SHA512SUMS | sha512sum -c --quiet || die "$ISO doesn't match Debian's SHA512SUMS"
echo "$ISO: checked (signed by $signer)"
ISO=$CACHE/$ISO

mkdir -p -m 700 "$WORK"
cd "$WORK"

# The tools disk, read-only.
rm -rf tools tools.img
mkdir tools
cp "$BIN" tools/roostack
cp "$REPO/scripts/ceremony.sh" tools/
cp "$REPO/docs/REGISTRY-KEYS.md" "$REPO/docs/RELEASING.md" tools/
mkfs.ext4 -q -L tools -d tools tools.img 128M

# The out disk, owned by the live system's user (uid 1000). Kept if it's
# there, so a second run doesn't lose a ceremony's results.
if [ ! -f out.img ]; then
	mkfs.ext4 -q -L out -E root_owner=1000:1000 out.img 32M
fi

# The USB disks, each given to the VM whole, behind an emulated USB drive
# (Arch packages QEMU's USB passthrough separately, as qemu-hw-usb-host).
# cache=none keeps their data out of the host's page cache.
usb=()
n=0
for dev in "$@"; do
	[ -b "$dev" ] || die "$dev isn't a block device"
	[ "$(lsblk -dno TYPE "$dev")" = disk ] || die "$dev: give the whole disk (e.g. /dev/sdb), not a partition"
	[ "$(lsblk -dno TRAN "$dev")" = usb ] || die "$dev isn't a USB disk"
	[ "$(lsblk -dbno SIZE "$dev")" -gt 0 ] ||
		die "$dev has no medium (0 bytes), as after an eject: unplug it, plug it back in, and only unmount it"
	[ -z "$(lsblk -no MOUNTPOINTS "$dev" | tr -d '[:space:]')" ] || die "$dev is mounted: unmount it first"
	echo "$dev: $(lsblk -dno MODEL,SIZE "$dev" | xargs); giving you access to it for the VM"
	sudo chown "$USER" "$dev"
	n=$((n + 1))
	usb+=(-drive "if=none,id=usb$n,file=$dev,format=raw,cache=none" -device "usb-storage,bus=xhci.0,drive=usb$n")
done
[ ${#usb[@]} -gt 0 ] || echo "No USB disks given: the offline copies will have to come from out/ afterwards."

cat <<EOF

In the VM (it boots to a console as the user "user"; press Enter at the menu):
  sudo mkdir -p /mnt/tools /mnt/out
  sudo mount -o ro LABEL=tools /mnt/tools
  sudo mount LABEL=out /mnt/out
  bash /mnt/tools/ceremony.sh

EOF
# QEMU_EXTRA: more options, for testing this script (e.g. -display none).
qemu-system-x86_64 ${QEMU_EXTRA:--display gtk} -enable-kvm -cpu host -smp 2 -m 4096 \
	-nic none \
	-cdrom "$ISO" -boot d \
	-drive file=tools.img,format=raw,if=virtio,readonly=on \
	-drive file=out.img,format=raw,if=virtio \
	-device qemu-xhci,id=xhci "${usb[@]}"

# Give the USB disks back to root, as udev had them.
for dev in "$@"; do sudo chown root "$dev"; done

# Copy out what ceremony.sh left.
rm -rf out
mkdir -m 700 out
debugfs -R "rdump / $WORK/out" out.img 2>/dev/null
rm -rf out/lost+found
chmod -R go-rwx out
cat <<EOF

The VM has stopped. From the out disk, in $WORK/out:
$(cd out && find . -type f | sort | sed 's|^\./|  |')

  public/     the public halves, safe to share: commit them (REGISTRY-KEYS.md
              steps 7, 8 and 11; RELEASING.md steps 7 and 8)
  to-github/  the CI secrets: put each file into GitHub as the secret it's
              named after (wl-copy < FILE, or xclip -selection clipboard),
              then delete it. Everything here goes at the next reboot:
                rm -rf $WORK
EOF
