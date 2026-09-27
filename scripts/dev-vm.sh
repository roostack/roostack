#!/usr/bin/env bash
# Development VM: Debian 13 under plain QEMU/KVM (no libvirt needed).
#
#   scripts/dev-vm.sh up        create (first time) and start the VM
#   scripts/dev-vm.sh ssh [cmd] open a shell, or run a command, as user "dev"
#   scripts/dev-vm.sh sync      build and copy the repo to ~/mylab in the VM
#   scripts/dev-vm.sh install   sync, then run scripts/dev-install.sh in the VM
#   scripts/dev-vm.sh ui        tunnel the MyLab UI (loopback-only) to http://127.0.0.1:18481
#   scripts/dev-vm.sh down      shut the VM down
#   scripts/dev-vm.sh destroy   delete the VM disk (keeps the downloaded image)
#   scripts/dev-vm.sh status
#
# Ports forwarded to 127.0.0.1 on this machine:
#   2222 -> 22 (ssh), 18480 -> 8480 (MyLab UI), 18080 -> 80, 18443 -> 443,
#   51820/udp -> 51820 (MyLab's WireGuard network)
#
# More VMs (e.g. a second machine joining the lab): MYLAB_VM=2 scripts/dev-vm.sh up
# VM n uses ssh port 2221+n, the other TCP ports plus (n-1)*10000, and UDP
# 51819+n. Another VM reaches VM 1's WireGuard at 10.0.2.2:51820 (QEMU's
# address for this machine).
#
# Note: QEMU user networking NATs every connection, so inside the VM all
# clients appear to come from 10.0.2.2. Keep that in mind when testing
# client-IP behaviour (docs/ASSUMPTIONS.md C4).
set -euo pipefail

N=${MYLAB_VM:-1}
[[ $N =~ ^[1-9]$ ]] || { echo "MYLAB_VM must be 1-9" >&2; exit 2; }
SUFFIX=$([[ $N == 1 ]] || echo "-$N")
NAME=mylab-dev$SUFFIX
VM_DIR=${MYLAB_VM_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/mylab-dev-vm$SUFFIX}
OFF=$(((N - 1) * 10000))
REPO=$(cd "$(dirname "$0")/.." && pwd)
IMAGE_URL=https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2
SUMS_URL=https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS
# Every VM starts from the same downloaded image, kept with VM 1.
IMAGE_DIR=${XDG_DATA_HOME:-$HOME/.local/share}/mylab-dev-vm
BASE=$IMAGE_DIR/debian-13-genericcloud-amd64.qcow2
DISK=$VM_DIR/disk.qcow2
KEY=$VM_DIR/id_ed25519
PIDFILE=$VM_DIR/qemu.pid
QMP=$VM_DIR/qmp.sock
SSH_PORT=$((2221 + N))
WG_PORT=$((51819 + N))
MEMORY=${MYLAB_VM_MEMORY:-4096}
CPUS=${MYLAB_VM_CPUS:-4}
DISK_SIZE=${MYLAB_VM_DISK:-40G}

ssh_opts=(-i "$KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
    -o LogLevel=ERROR -o ConnectTimeout=5)

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
    log "Downloading Debian 13 cloud image"
    curl -fL --progress-bar -o "$BASE.part" "$IMAGE_URL"
    curl -fsSL -o "$IMAGE_DIR/SHA512SUMS" "$SUMS_URL"
    want=$(awk '$2 == "debian-13-genericcloud-amd64.qcow2" {print $1}' "$IMAGE_DIR/SHA512SUMS")
    got=$(sha512sum "$BASE.part" | awk '{print $1}')
    [[ -n $want && $want == "$got" ]] || { echo "checksum mismatch" >&2; exit 1; }
    mv "$BASE.part" "$BASE"
}

create() {
    mkdir -p "$VM_DIR"
    download
    [[ -f $KEY ]] || ssh-keygen -q -t ed25519 -N '' -C mylab-dev-vm -f "$KEY"
    log "Creating disk from the cloud image (resized to $DISK_SIZE on first boot)"
    cp --reflink=auto "$BASE" "$DISK"

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
packages: [curl, ca-certificates]
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
    log "Starting VM ($MEMORY MB, $CPUS CPUs)"
    qemu-system-x86_64 \
        -name "$NAME" -machine q35,accel=kvm -cpu host -m "$MEMORY" -smp "$CPUS" \
        -drive file="$DISK",if=virtio,id=disk0,discard=unmap \
        -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$SSH_PORT-:22,hostfwd=tcp:127.0.0.1:$((18480 + OFF))-:8480,hostfwd=tcp:127.0.0.1:$((18080 + OFF))-:80,hostfwd=tcp:127.0.0.1:$((18443 + OFF))-:443,hostfwd=udp:127.0.0.1:$WG_PORT-:51820" \
        -device virtio-net-pci,netdev=net0 \
        -device virtio-rng-pci \
        "${smbios[@]}" \
        -display none -serial file:"$VM_DIR/console.log" \
        -qmp unix:"$QMP",server=on,wait=off \
        -S -daemonize -pidfile "$PIDFILE"
    # Started paused (-S) so the disk can be grown before the guest boots;
    # cloud-init then expands the root filesystem to fill it.
    if $first_boot; then
        local bytes
        bytes=$(numfmt --from=iec "$DISK_SIZE")
        qmp "{\"execute\": \"block_resize\", \"arguments\": {\"device\": \"disk0\", \"size\": $bytes}}"
    fi
    qmp '{"execute": "cont"}'

    log "Waiting for SSH (first boot takes a minute or two)"
    for _ in $(seq 1 90); do
        if vm_ssh true 2>/dev/null; then
            if $first_boot; then
                log "Waiting for cloud-init to finish"
                vm_ssh cloud-init status --wait >/dev/null || true
                [[ -n $seed_pid ]] && kill "$seed_pid" 2>/dev/null || true
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
    log "Building"
    make -C "$REPO" build >/dev/null
    log "Copying repo to ~/mylab in the VM"
    vm_ssh 'rm -rf ~/mylab && mkdir -p ~/mylab'
    tar -C "$REPO" --exclude=.git -cf - . | vm_ssh 'tar -C ~/mylab -xf -'
}

stop() {
    running || { log "Not running"; return; }
    log "Shutting down"
    qmp '{"execute": "system_powerdown"}' 2>/dev/null || vm_ssh 'sudo systemctl poweroff' 2>/dev/null || true
    for _ in $(seq 1 30); do running || { rm -f "$PIDFILE"; return; }; sleep 2; done
    kill "$(cat "$PIDFILE")" && rm -f "$PIDFILE"
}

case ${1:-} in
up) start ;;
ssh) shift; vm_ssh -t "$@" ;;
sync) sync_repo ;;
install) sync_repo; vm_ssh -t 'cd ~/mylab && sudo scripts/dev-install.sh' ;;
ui) log "MyLab UI: http://127.0.0.1:$((18481 + OFF)) (Ctrl-C to stop)"
    # Reconnect when the VM restarts (e.g. after OS updates) or the
    # connection drops; notice a dead connection within ~15s.
    while true; do
        vm_ssh -N -o ServerAliveInterval=5 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes \
            -L 127.0.0.1:$((18481 + OFF)):127.0.0.1:8480 || true
        log "Tunnel closed; reconnecting…"
        sleep 3
    done ;;
down) stop ;;
destroy) stop; rm -rf "$DISK" "$VM_DIR/seed" "$VM_DIR/console.log"; log "Disk deleted" ;;
status) if running; then echo "running (pid $(cat "$PIDFILE"))"; else echo "stopped"; fi ;;
*) sed -n '2,18p' "$0"; exit 2 ;;
esac
