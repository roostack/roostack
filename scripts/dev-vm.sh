#!/usr/bin/env bash
# Development VM: Debian 13 under plain QEMU (no libvirt needed), with KVM on
# Linux or Hypervisor.framework on macOS.
#
#   scripts/dev-vm.sh up        create (first time) and start the VM
#   scripts/dev-vm.sh ssh [cmd] open a shell, or run a command, as user "dev"
#   scripts/dev-vm.sh sync      build and copy the repo to ~/roostack in the VM
#   scripts/dev-vm.sh install   sync, then run scripts/dev-install.sh in the VM
#   scripts/dev-vm.sh test [flags]  run the Go tests in the VM (built here, so
#                               the VM needs no Go); flags go to each test
#                               binary, e.g. -test.run TestApply. Each checkout
#                               (worktree) gets its own folder in the VM, so
#                               several can test on one VM at once
#   scripts/dev-vm.sh ui        tunnel the Roostack UI (loopback-only) to http://127.0.0.1:18481
#   scripts/dev-vm.sh down      shut the VM down
#   scripts/dev-vm.sh destroy   delete the VM disk (keeps the downloaded image)
#   scripts/dev-vm.sh status
#   scripts/dev-vm.sh usb add|remove NAME [SIZE]   plug a virtual USB drive in or out
#
# Ports forwarded to 127.0.0.1 on this machine:
#   2222 -> 22 (ssh), 18480 -> 8480 (Roostack UI), 18080 -> 80, 18443 -> 443,
#   18444 -> 8443 (the first-run setup pages: https://127.0.0.1:18444),
#   51820/udp -> 51820 (Roostack's WireGuard network), 52820/udp -> 51821 (storage for other labs)
#
# More VMs (e.g. a second machine joining the lab): ROOSTACK_VM=2 scripts/dev-vm.sh up
# VMs 1-9 work. VM n uses ssh port 2221+n and UDP 51819+n and 52819+n. Its
# other TCP ports are VM 1's plus (n-1)*10000 for VMs 1-5 (VM 2's UI tunnel is
# 28481, VM 5's 58481), and 6x480, 6x080, 6x443, 6x444, 6x481 for VMs 6-9 with
# x = n-6 (VM 6's UI tunnel is 60481, VM 9's 63481): the 10000 step would pass
# 65535 from VM 6. Another VM reaches VM n's WireGuard at 10.0.2.2:<its UDP
# port> (QEMU's address for this machine).
#
# REGISTRY_DEV_KEY=ed25519:… builds Roostack trusting a test root key for
# the service registry (Makefile; docs/REGISTRY-KEYS.md), for a VM fetching
# from a registry signed with test keys.
#
# The VM's architecture follows this machine's (amd64 on a PC, arm64 on an
# Apple Silicon Mac), so it runs at full speed. ROOSTACK_VM_ARCH=amd64|arm64
# picks the other one, emulated and slow. A VM keeps the architecture it was
# created with. Needs QEMU (Arch: qemu-system-x86_64 or qemu-system-aarch64
# with edk2-aarch64; Debian: qemu-system-x86 or qemu-system-arm with
# qemu-efi-aarch64; macOS: brew install qemu).
#
# Note: QEMU user networking NATs every connection, so inside the VM all
# clients appear to come from 10.0.2.2. Keep that in mind when testing
# client-IP behaviour (docs/ASSUMPTIONS.md C4).
set -euo pipefail

N=${ROOSTACK_VM:-1}
[[ $N =~ ^[1-9]$ ]] || { echo "ROOSTACK_VM must be 1-9" >&2; exit 2; }
SUFFIX=$([[ $N == 1 ]] || echo "-$N")
NAME=roostack-dev$SUFFIX
VM_DIR=${ROOSTACK_VM_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/roostack-dev-vm$SUFFIX}
# tcp_port BASE: VM N's port for VM 1's port BASE (18xxx), as the header says.
tcp_port() {
    if ((N <= 5)); then echo $(($1 + (N - 1) * 10000)); else echo $((60000 + (N - 6) * 1000 + $1 - 18000)); fi
}
UI_PORT=$(tcp_port 18481)
REPO=$(cd "$(dirname "$0")/.." && pwd)
case $(uname -m) in
x86_64 | amd64) HOST_ARCH=amd64 ;;
aarch64 | arm64) HOST_ARCH=arm64 ;;
*) HOST_ARCH=unknown ;;
esac
ARCH=${ROOSTACK_VM_ARCH:-$HOST_ARCH}
if [[ -f $VM_DIR/arch ]]; then
    made=$(cat "$VM_DIR/arch")
    [[ -z ${ROOSTACK_VM_ARCH:-} || $made == "$ARCH" ]] ||
        { echo "This VM is $made; destroy it first to make an $ARCH one" >&2; exit 2; }
    ARCH=$made
elif [[ -f $VM_DIR/disk.qcow2 ]]; then
    ARCH=amd64 # made before VMs recorded their architecture
fi
[[ $ARCH == amd64 || $ARCH == arm64 ]] || { echo "ROOSTACK_VM_ARCH must be amd64 or arm64" >&2; exit 2; }
IMAGE_NAME=debian-13-genericcloud-$ARCH.qcow2
IMAGE_URL=https://cloud.debian.org/images/cloud/trixie/latest/$IMAGE_NAME
SUMS_URL=https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS
# Every VM starts from the same downloaded image, kept with VM 1.
IMAGE_DIR=${XDG_DATA_HOME:-$HOME/.local/share}/roostack-dev-vm
BASE=$IMAGE_DIR/$IMAGE_NAME
DISK=$VM_DIR/disk.qcow2
KEY=$VM_DIR/id_ed25519
PIDFILE=$VM_DIR/qemu.pid
QMP=$VM_DIR/qmp.sock
SSH_PORT=$((2221 + N))
WG_PORT=$((51819 + N))
GUESTS_PORT=$((52819 + N))
MEMORY=${ROOSTACK_VM_MEMORY:-4096}
CPUS=${ROOSTACK_VM_CPUS:-4}
DISK_SIZE=${ROOSTACK_VM_DISK:-40G}

ssh_opts=(-i "$KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
    -o LogLevel=ERROR -o ConnectTimeout=5)

# bytes turns a size such as 40G into bytes (numfmt isn't on macOS).
bytes() {
    local n=${1%[KMGT]} u=${1##*[0-9]}
    case $u in
    K) echo $((n << 10)) ;; M) echo $((n << 20)) ;; G) echo $((n << 30)) ;; T) echo $((n << 40)) ;;
    *) echo "$n" ;;
    esac
}

log() { printf '==> %s\n' "$*" >&2; }
running() { [[ -f $PIDFILE ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }
vm_ssh() { ssh "${ssh_opts[@]}" dev@127.0.0.1 "$@"; }

# qmp sends QMP commands (JSON, one per argument) to the running QEMU. Used
# instead of qemu-img, which isn't always installed.
qmp() {
    python3 - "$QMP" "$@" <<'PY'
import json, socket, sys
s = socket.socket(socket.AF_UNIX)
s.connect(sys.argv[1])
f = s.makefile("rw")
json.loads(f.readline())  # greeting
for cmd in [{"execute": "qmp_capabilities"}] + [json.loads(a) for a in sys.argv[2:]]:
    f.write(json.dumps(cmd) + "\n"); f.flush()
    while True:
        msg = json.loads(f.readline())
        if "event" in msg:
            continue
        if "error" in msg:
            sys.exit("QMP error: " + msg["error"]["desc"])
        break
PY
}

download() {
    [[ -f $BASE ]] && return
    mkdir -p "$IMAGE_DIR"
    log "Downloading Debian 13 cloud image ($ARCH)"
    curl -fL --progress-bar -o "$BASE.part" "$IMAGE_URL"
    curl -fsSL -o "$IMAGE_DIR/SHA512SUMS" "$SUMS_URL"
    want=$(awk -v f="$IMAGE_NAME" '$2 == f {print $1}' "$IMAGE_DIR/SHA512SUMS")
    got=$(shasum -a 512 "$BASE.part" | awk '{print $1}')
    [[ -n $want && $want == "$got" ]] || { echo "checksum mismatch" >&2; exit 1; }
    mv "$BASE.part" "$BASE"
}

create() {
    mkdir -p "$VM_DIR"
    download
    [[ -f $KEY ]] || ssh-keygen -q -t ed25519 -N '' -C roostack-dev-vm -f "$KEY"
    log "Creating disk from the cloud image (resized to $DISK_SIZE on first boot)"
    # A copy-on-write clone where the filesystem can (btrfs, APFS).
    cp --reflink=auto "$BASE" "$DISK" 2>/dev/null || cp -c "$BASE" "$DISK" 2>/dev/null || cp "$BASE" "$DISK"
    echo "$ARCH" >"$VM_DIR/arch"

    # cloud-init seed, served over HTTP to the VM during first boot
    # (NoCloud-net), so no ISO tools are needed.
    mkdir -p "$VM_DIR/seed"
    cat >"$VM_DIR/seed/meta-data" <<EOF
instance-id: $NAME-$(date +%s)
local-hostname: $NAME
EOF
    cat >"$VM_DIR/seed/user-data" <<EOF
#cloud-config
users:
  - name: dev
    groups: [sudo]
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - $(cat "$KEY.pub")
ssh_pwauth: false
package_update: true
# Debian's standard kernel, as on real hardware: the cloud image's kernel
# has no USB drivers (so no USB drives for the backup drive picker).
packages: [curl, ca-certificates, linux-image-$ARCH]
runcmd:
  - [sh, -c, "DEBIAN_FRONTEND=noninteractive apt-get purge -y 'linux-image-*cloud*' && update-grub"]
EOF
    : >"$VM_DIR/seed/vendor-data"
}

start() {
    running && { log "Already running"; return; }
    local first_boot=false seed_pid=""
    if [[ ! -f $DISK ]]; then
        create
        first_boot=true
    fi
    local smbios=()
    if $first_boot; then
        local port=$((18998 + N))
        (cd "$VM_DIR/seed" && exec python3 -m http.server "$port" --bind 127.0.0.1 >"$VM_DIR/seed.log" 2>&1) &
        seed_pid=$!
        smbios=(-smbios "type=1,serial=ds=nocloud;s=http://10.0.2.2:$port/")
    fi
    log "Starting VM ($ARCH, $MEMORY MB, $CPUS CPUs)"
    # Hardware virtualisation when the VM matches this machine, else
    # emulation. arm64 has no PC BIOS: it boots UEFI firmware, which comes
    # with QEMU (found in its data folder).
    local accel=tcg cpu=max
    if [[ $ARCH == "$HOST_ARCH" ]]; then
        case $(uname -s) in
        Linux) accel=kvm cpu=host ;;
        Darwin) accel=hvf cpu=host ;;
        esac
    fi
    local qemu machine
    case $ARCH in
    amd64) qemu=(qemu-system-x86_64) machine="q35,accel=$accel" ;;
    arm64) qemu=(qemu-system-aarch64 -bios edk2-aarch64-code.fd) machine="virt,accel=$accel" ;;
    esac
    # WireGuard's UDP ports are forwarded on all the host's addresses, not
    # just 127.0.0.1: QEMU sends the VM's own packets from a forwarded port
    # through the forward's socket, and one bound to 127.0.0.1 can't reach
    # the internet (a VM controller dialling a cloud server, 2026-10-11).
    # WireGuard answers nobody without a key, so the home network seeing
    # the ports is harmless.
    "${qemu[@]}" \
        -name "$NAME" -machine "$machine" -cpu "$cpu" -m "$MEMORY" -smp "$CPUS" \
        -drive file="$DISK",if=virtio,id=disk0,discard=unmap \
        -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22,hostfwd=tcp:127.0.0.1:$(tcp_port 18480)-:8480,hostfwd=tcp:127.0.0.1:$(tcp_port 18080)-:80,hostfwd=tcp:127.0.0.1:$(tcp_port 18443)-:443,hostfwd=tcp:127.0.0.1:$(tcp_port 18444)-:8443,hostfwd=udp::$WG_PORT-:51820,hostfwd=udp::$GUESTS_PORT-:51821" \
        -device virtio-net-pci,netdev=net0 \
        -device virtio-rng-pci \
        -device qemu-xhci,id=xhci \
        ${smbios[@]+"${smbios[@]}"} \
        -display none -serial file:"$VM_DIR/console.log" \
        -qmp unix:"$QMP",server=on,wait=off \
        -S -daemonize -pidfile "$PIDFILE"
    # Started paused (-S) so the disk can be grown before the guest boots;
    # cloud-init then expands the root filesystem to fill it.
    if $first_boot; then
        qmp "{\"execute\": \"block_resize\", \"arguments\": {\"device\": \"disk0\", \"size\": $(bytes "$DISK_SIZE")}}"
    fi
    qmp '{"execute": "cont"}'

    log "Waiting for SSH (first boot takes a minute or two)"
    for _ in $(seq 1 90); do
        if vm_ssh true 2>/dev/null; then
            if $first_boot; then
                log "Waiting for cloud-init to finish"
                vm_ssh cloud-init status --wait >/dev/null || true
                [[ -n $seed_pid ]] && kill "$seed_pid" 2>/dev/null || true
                # Boot the standard kernel cloud-init installed.
                log "Restarting into Debian's standard kernel"
                vm_ssh sudo systemctl reboot 2>/dev/null || true
                sleep 10
                until vm_ssh true 2>/dev/null; do sleep 3; done
            fi
            log "VM ready: scripts/dev-vm.sh ssh"
            return
        fi
        sleep 5
    done
    [[ -n $seed_pid ]] && kill "$seed_pid" 2>/dev/null || true
    echo "VM did not become reachable; see $VM_DIR/console.log" >&2
    exit 1
}

sync_repo() {
    running || { echo "VM is not running" >&2; exit 1; }
    log "Building for linux/$ARCH"
    GOARCH=$ARCH make -C "$REPO" build ${REGISTRY_DEV_KEY:+REGISTRY_DEV_KEY=$REGISTRY_DEV_KEY} >/dev/null
    log "Copying repo to ~/roostack in the VM"
    # sudo: install leaves a package that root built in dist/.
    vm_ssh 'sudo rm -rf ~/roostack && mkdir -p ~/roostack'
    # No extended attributes or ._ files (macOS adds them; Linux's tar warns).
    COPYFILE_DISABLE=1 tar -C "$REPO" --exclude=.git --exclude=.DS_Store --no-xattrs -cf - . | vm_ssh 'tar -C ~/roostack -xf -'
}

# test_repo runs the test suite in the VM, for the VM's architecture and on
# Linux, which is how to run it from a Mac (parts of Roostack only build
# for Linux). Each package's test binary runs in its own folder, as
# `go test` does, with the VM's restic if it has one (CLAUDE.md "Commands").
# The sources and binaries go to ~/test-<checkout> in the VM, not
# ~/roostack, so sessions testing different worktrees on one VM (or
# syncing for an install meanwhile) don't replace each other's files.
test_repo() {
    running || { echo "VM is not running" >&2; exit 1; }
    local tag out
    tag=test-$(basename "$REPO" | tr -c 'A-Za-z0-9._-\n' -)
    out=$(mktemp -d)
    trap "rm -rf '$out'" EXIT # expanded now: out is gone by exit
    log "Building the tests for linux/$ARCH"
    mkdir "$out/bin"
    (cd "$REPO" && GOOS=linux GOARCH=$ARCH CGO_ENABLED=0 go test -c -o "$out/bin/" ./... >/dev/null)
    # go test -c names each binary after its import path's last element
    # (cmd/roostack: roostack.test), not the package's name (main).
    (cd "$REPO" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}} {{.Dir}}{{end}}' ./...) |
        sed -e "s| $REPO/| |" -e 's|^[^ ]*/||' | grep . >"$out/bin/packages"
    log "Copying the repo and tests to ~/$tag in the VM"
    vm_ssh "rm -rf ~/$tag && mkdir -p ~/$tag/src ~/$tag/bin"
    # No extended attributes or ._ files (macOS adds them; Linux's tar warns).
    COPYFILE_DISABLE=1 tar -C "$REPO" --exclude=.git --exclude=.DS_Store --exclude=./bin --exclude=./dist \
        --no-xattrs -cf - . | vm_ssh "tar -C ~/$tag/src -xf -"
    COPYFILE_DISABLE=1 tar -C "$out/bin" --no-xattrs -cf - . | vm_ssh "tar -C ~/$tag/bin -xf -"
    log "Running the tests in the VM"
    vm_ssh bash -s -- "$tag" "$@" <<'SH'
tag=$1
shift
trap 'rm -rf ~/"$tag"' EXIT
[ -x /usr/bin/restic ] && export ROOSTACK_RESTIC_BIN=/usr/bin/restic ||
    echo "warning: restic isn't installed in the VM, so its tests skip (scripts/dev-vm.sh install)"
failed=()
while read -r name dir; do
    start=$(date +%s%N)
    if out=$(cd ~/"$tag"/src/"$dir" && ~/"$tag"/bin/"$name".test -test.count=1 "$@" 2>&1); then
        printf 'ok   %-40s %ss\n' "$dir" "$((($(date +%s%N) - start) / 1000000000))"
    else
        printf '%s\nFAIL %s\n' "$out" "$dir"
        failed+=("$dir")
    fi
done <~/"$tag"/bin/packages
[ ${#failed[@]} -eq 0 ] || { echo "FAILED: ${failed[*]}"; exit 1; }
SH
}

stop() {
    running || { log "Not running"; return; }
    log "Shutting down"
    qmp '{"execute": "system_powerdown"}' 2>/dev/null || vm_ssh 'sudo systemctl poweroff' 2>/dev/null || true
    for _ in $(seq 1 30); do running || { rm -f "$PIDFILE"; return; }; sleep 2; done
    kill "$(cat "$PIDFILE")" && rm -f "$PIDFILE"
}

# usb_drive plugs a virtual USB drive into the running VM, or unplugs it,
# to test Roostack's backup drive picker: usb add NAME [SIZE], usb remove
# NAME. The drive's disk image stays in the VM's folder, so plugging it in
# again brings back what was written to it.
usb_drive() {
    local action=$1 name=$2 size=$3
    running || { echo "VM is not running" >&2; exit 1; }
    [[ $name =~ ^[a-z0-9]+$ ]] || { echo "usage: usb add|remove NAME [SIZE]" >&2; exit 2; }
    local img="$VM_DIR/usb-$name.img"
    case $action in
    add)
        [[ -f $img ]] || truncate -s "$size" "$img"
        qmp "{\"execute\": \"blockdev-add\", \"arguments\": {\"driver\": \"raw\", \"node-name\": \"usb-$name\", \"file\": {\"driver\": \"file\", \"filename\": \"$img\"}}}" \
            "{\"execute\": \"device_add\", \"arguments\": {\"driver\": \"usb-storage\", \"bus\": \"xhci.0\", \"drive\": \"usb-$name\", \"id\": \"usbdev-$name\", \"removable\": true}}"
        log "USB drive $name plugged in ($(($(wc -c <"$img") / 1000000000)) GB)" ;;
    remove)
        qmp "{\"execute\": \"device_del\", \"arguments\": {\"id\": \"usbdev-$name\"}}"
        sleep 1
        qmp "{\"execute\": \"blockdev-del\", \"arguments\": {\"node-name\": \"usb-$name\"}}"
        log "USB drive $name unplugged" ;;
    *) echo "usage: usb add|remove NAME [SIZE]" >&2; exit 2 ;;
    esac
}

case ${1:-} in
up) start ;;
ssh) shift; vm_ssh -t "$@" ;;
sync) sync_repo ;;
test) shift; test_repo "$@" ;;
install) sync_repo; vm_ssh -t "cd ~/roostack && sudo VERSION=$("$REPO/scripts/build-deb.sh" --version) scripts/dev-install.sh" ;;
ui) log "Roostack UI: http://127.0.0.1:$UI_PORT (Ctrl-C to stop)"
    # Reconnect when the VM restarts (e.g. after OS updates) or the
    # connection drops; notice a dead connection within ~15s.
    while true; do
        vm_ssh -N -o ServerAliveInterval=5 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes \
            -L 127.0.0.1:$UI_PORT:127.0.0.1:8480 || true
        log "Tunnel closed; reconnecting…"
        sleep 3
    done ;;
down) stop ;;
destroy) stop; rm -rf "$DISK" "$VM_DIR/arch" "$VM_DIR/seed" "$VM_DIR/console.log"; log "Disk deleted" ;;
status) if running; then echo "running ($ARCH, pid $(cat "$PIDFILE"))"; else echo "stopped"; fi ;;
usb) usb_drive "${2:-}" "${3:-}" "${4:-8G}" ;;
*) sed -n '2,30p' "$0"; exit 2 ;;
esac
