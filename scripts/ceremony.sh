#!/bin/bash
# The first key ceremony, run inside the offline VM (scripts/ceremony-vm.sh):
# the service registry's root and signing keys and their first statement
# (docs/REGISTRY-KEYS.md, steps 2-5) and the release key (docs/RELEASING.md,
# "The ceremony", steps 1-5), in one sitting. Every command is printed
# before it runs. Used on 2026-10-27; the later procedures (a new signing
# key, renewing the subkey) are done by hand in the same VM, from the docs
# on its tools disk, with an offline copy on one of its USB drives.
#
# What it leaves behind:
#   - on each offline USB drive: roostack-offline-keys-<date>.tar.gpg, every
#     private key and the revocation certificate, encrypted with your
#     passphrase;
#   - on the "out" disk, for the host:
#       public/     the public halves, safe to share and to commit
#       to-github/  the two CI secrets: paste each into GitHub, then delete
#
# Everything else lives in RAM (/dev/shm) and goes when the VM powers off.
set -euo pipefail
umask 077

TOOLS=$(dirname "$(readlink -f "$0")")
ROOSTACK=$TOOLS/roostack
OUT=${OUT:-/mnt/out}
W=${W:-/dev/shm/ceremony}
KEY_UID='Roostack releases <releases@roostack.cloud>'
DATE=$(date -u +%F)
BUNDLE=roostack-offline-keys-$DATE.tar.gpg

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
run() { printf '+ %s\n' "$*" >&2; "$@"; }
die() { printf 'Stopped: %s\n' "$*" >&2; exit 1; }

# gpg with the passphrase given on fd 3, never on the command line.
gpgp() { gpg --batch --pinentry-mode loopback --passphrase-fd 3 "$@" 3<<<"$PASS"; }

# Offline means no network device at all, not just unplugged.
if [ -z "${CEREMONY_ALLOW_NETWORK:-}" ] && ip -o link show | grep -qv ': lo:'; then
	die "this machine has a network device; start the VM with ceremony-vm.sh (-nic none)"
fi
[ -x "$ROOSTACK" ] || die "no roostack binary beside this script"
mountpoint -q "$OUT" 2>/dev/null || [ -n "${CEREMONY_ALLOW_NETWORK:-}" ] ||
	die "$OUT isn't mounted: sudo mount LABEL=out $OUT"
[ -e "$W" ] && die "$W already exists: power off and start again for a clean ceremony"
mkdir -m 700 "$W"
cd "$W"

step "1/7 Your passphrase"
echo "One passphrase protects the release key's primary key and the offline"
echo "bundle. Make it in your password manager now (20+ characters) and paste it."
while :; do
	read -rsp "Passphrase: " PASS; echo
	read -rsp "Again: " again; echo
	[ "$PASS" = "$again" ] || { echo "They differ; again."; continue; }
	[ ${#PASS} -ge 16 ] || { echo "At least 16 characters, please."; continue; }
	break
done
unset again

step "2/7 The registry's root key"
run "$ROOSTACK" registry keygen -out root.key >root.pub
echo "Root public key: $(cat root.pub)"

step "3/7 The registry's signing key"
run "$ROOSTACK" registry keygen -out signing.key >signing.pub
echo "Signing public key: $(cat signing.pub)"

step "4/7 The statement naming the signing key (serial 1)"
run "$ROOSTACK" registry delegate -root root.key -serial 1 -out keys "$(cat signing.pub)"
grep -qF "$(cat signing.pub)" keys/keys.json || die "keys.json doesn't name the signing key"
grep -q '"serial": 1' keys/keys.json || die "keys.json's serial isn't 1"
cat keys/keys.json

step "5/7 The release key"
export GNUPGHOME=$W/gnupg
mkdir -m 700 "$GNUPGHOME"
run gpg --batch --passphrase '' --quick-generate-key "$KEY_UID" ed25519 cert never
FPR=$(gpg --list-keys --with-colons "$KEY_UID" | awk -F: '/^fpr/ {print $10; exit}')
run gpg --batch --passphrase '' --quick-add-key "$FPR" ed25519 sign 3y
SUB=$(gpg --list-keys --with-colons "$FPR" | awk -F: '/^fpr/ {n++; if (n == 2) print $10}')
[ -n "$FPR" ] && [ -n "$SUB" ] || die "couldn't read the key's fingerprints"
# The CI copy: only the signing subkey ("!"), without a passphrase.
run gpg --armor --export "$FPR" >roostack-release.asc
run gpg --armor --export-secret-subkeys "$SUB!" >signing-subkey.asc
# Then the passphrase goes on, and the full secret key is exported with it.
echo "+ gpg --passwd $FPR (with your passphrase)"
gpgp --passwd "$FPR"
echo "+ gpg --armor --export-secret-keys $FPR > roostack-release-secret.asc"
gpgp --armor --export-secret-keys "$FPR" >roostack-release-secret.asc
REV=$GNUPGHOME/openpgp-revocs.d/$FPR.rev
[ -s "$REV" ] || die "no revocation certificate at $REV"
cp "$REV" roostack-release-revocation.rev
# Check the passphrase really is on: exporting without it must fail.
if gpg --batch --pinentry-mode loopback --passphrase '' --armor --export-secret-keys "$FPR" >/dev/null 2>&1; then
	die "the primary key has no passphrase"
fi
gpg --fingerprint "$FPR"
{
	echo "Roostack release key, made $DATE"
	echo "Primary: $FPR"
	echo "Signing subkey: $SUB"
} >fingerprint.txt

step "6/7 The offline bundle"
FILES=(root.key root.pub signing.pub keys roostack-release.asc roostack-release-secret.asc
	roostack-release-revocation.rev fingerprint.txt)
echo "+ tar c ${FILES[*]} | gpg --symmetric --cipher-algo AES256 > $BUNDLE"
tar cf - "${FILES[@]}" | gpgp --symmetric --cipher-algo AES256 -o "$BUNDLE"
gpgp -d "$BUNDLE" 2>/dev/null | tar tf - >bundle.list
diff -q <(tar cf - "${FILES[@]}" | tar tf -) bundle.list >/dev/null || die "the bundle doesn't open to the same files"
echo "Checked: it opens with your passphrase and holds:"
sed 's/^/  /' bundle.list

step "7/7 The out disk (for the host)"
run mkdir -p "$OUT/public" "$OUT/to-github"
run cp -r root.pub signing.pub keys roostack-release.asc fingerprint.txt "$OUT/public/"
run cp signing.key "$OUT/to-github/REGISTRY_SIGNING_KEY"
run cp signing-subkey.asc "$OUT/to-github/ROOSTACK_RELEASE_SIGNING_KEY"
run cp "$BUNDLE" "$OUT/"
sync

# The two offline copies: each a USB drive passed through to this VM.
usb_copy() {
	local n=$1 part mnt=/mnt/usb$1
	while :; do
		echo
		lsblk -o NAME,SIZE,TRAN,MODEL,FSTYPE,LABEL,MOUNTPOINTS | grep -v '^loop\|^sr\|^vd\|^fd'
		read -rp "Offline copy $n: the partition to use (e.g. sda1), or 'later': " part
		[ "$part" = later ] && { echo "Not copied: $BUNDLE is also on the out disk; copy it from the host."; return; }
		[ -b "/dev/$part" ] || { echo "No /dev/$part."; continue; }
		[ -n "$(lsblk -no FSTYPE "/dev/$part")" ] || {
			echo "/dev/$part has no filesystem. To make one (erasing it): sudo mkfs.ext4 -L roostack-keys /dev/$part"
			continue
		}
		break
	done
	run sudo mkdir -p "$mnt"
	run sudo mount "/dev/$part" "$mnt"
	run sudo cp "$BUNDLE" "$mnt/"
	sync
	run sudo umount "$mnt"
	# Read it back from the drive, as step 5 of REGISTRY-KEYS.md asks.
	run sudo mount -o ro "/dev/$part" "$mnt"
	sudo cmp "$BUNDLE" "$mnt/$BUNDLE" || die "the copy on /dev/$part differs"
	sudo cat "$mnt/$BUNDLE" | gpgp -d 2>/dev/null | tar tf - | diff -q - bundle.list >/dev/null ||
		die "the copy on /dev/$part doesn't open"
	run sudo umount "$mnt"
	echo "Offline copy $n checked: same bytes, and it opens with your passphrase."
}
step "Offline copies"
usb_copy 1
usb_copy 2

unset PASS
cat <<EOF

$(printf '\033[1m')Done.$(printf '\033[0m') Write these down with today's date ($DATE):
  Registry root key:   $(cat root.pub)
  Registry signing key: $(cat signing.pub)
  Release key:         $FPR
  Signing subkey:      $SUB

Keep the passphrase in your password manager: without it the offline copies
and the release key's primary key can't be opened.

Now: sudo umount $OUT && sudo poweroff
Everything here was in RAM and goes with it.
EOF
