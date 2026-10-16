#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
creds="${PVE_CREDS:-$root/pve.local.md}"
host="${PVE_HOST:-192.168.1.160}"
node="${PVE_NODE:-pve}"
user="$(sed -n 2p "$creds")"
secret="$(sed -n 3p "$creds")"
api="https://$host:8006/api2/json"
auth="Authorization: PVEAPIToken=$user=$secret"

req() {
  local method="$1" path="$2"
  shift 2
  curl -sSk -X "$method" -H "$auth" "$@" "$api$path"
}

guard() {
  local id="$1"
  if (( id < 900 || id > 908 )); then
    echo "vmid $id outside 900-908" >&2
    exit 1
  fi
}

wait_task() {
  local upid="$1"
  while :; do
    local st
    st="$(req GET "/nodes/$node/tasks/$upid/status" | jq -r '.data.status + " " + (.data.exitstatus // "")')"
    case "$st" in
      "stopped OK") return 0 ;;
      stopped*) echo "task failed: $st" >&2; return 1 ;;
    esac
    sleep 2
  done
}

upid() { jq -er '.data // error(tostring)'; }

addr() { echo "192.168.1.$((240 + $1 - 900))"; }

declare -A images=(
  [debian-12]="https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2"
  [debian-13]="https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
  [ubuntu-24.04]="https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
  [ubuntu-26.04]="https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img"
)

cmd="${1:-}"
shift || true

case "$cmd" in
  ls)
    req GET /cluster/resources?type=vm | jq -r '.data[] | select(.vmid>=900 and .vmid<=919) | "\(.vmid) \(.name) \(.status)"'
    ;;
  fetch)
    os="$1"
    req POST "/nodes/$node/storage/local/download-url" \
      --data-urlencode "url=${images[$os]}" \
      --data-urlencode "filename=awgx-$os.qcow2" \
      --data-urlencode "content=import" | upid | { read -r u; wait_task "$u"; }
    ;;
  create)
    id="$1" os="$2" name="awgx-$2"
    guard "$id"
    key="$(jq -sRr @uri < "${SSH_PUB:-$HOME/.ssh/id_ed25519.pub}")"
    req POST "/nodes/$node/qemu" \
      --data-urlencode "vmid=$id" \
      --data-urlencode "name=${3:-$name}" \
      --data-urlencode "memory=2048" \
      --data-urlencode "cores=2" \
      --data-urlencode "cpu=host" \
      --data-urlencode "ostype=l26" \
      --data-urlencode "scsihw=virtio-scsi-single" \
      --data-urlencode "scsi0=data:0,import-from=local:import/awgx-$os.qcow2" \
      --data-urlencode "ide2=data:cloudinit" \
      --data-urlencode "boot=order=scsi0" \
      --data-urlencode "serial0=socket" \
      --data-urlencode "agent=enabled=1" \
      --data-urlencode "net0=virtio,bridge=vmbr0" \
      --data-urlencode "ipconfig0=ip=$(addr "$id")/24,gw=192.168.1.1" \
      --data-urlencode "nameserver=1.1.1.1" \
      --data-urlencode "ciuser=awgx" \
      --data-urlencode "sshkeys=$key" | upid | { read -r u; wait_task "$u"; }
    req PUT "/nodes/$node/qemu/$id/resize" --data-urlencode "disk=scsi0" --data-urlencode "size=12G" >/dev/null
    ;;
  start|stop|shutdown)
    guard "$1"
    req POST "/nodes/$node/qemu/$1/status/$cmd" | upid | { read -r u; wait_task "$u"; }
    ;;
  snapshot)
    guard "$1"
    req POST "/nodes/$node/qemu/$1/snapshot" --data-urlencode "snapname=$2" | upid | { read -r u; wait_task "$u"; }
    ;;
  delsnap)
    guard "$1"
    req DELETE "/nodes/$node/qemu/$1/snapshot/$2" | upid | { read -r u; wait_task "$u"; }
    ;;
  rollback)
    guard "$1"
    req POST "/nodes/$node/qemu/$1/snapshot/$2/rollback" | upid | { read -r u; wait_task "$u"; }
    ;;
  destroy)
    guard "$1"
    req DELETE "/nodes/$node/qemu/$1?purge=1&destroy-unreferenced-disks=1" | upid | { read -r u; wait_task "$u"; }
    ;;
  ip)
    guard "$1"
    addr "$1"
    ;;
  *)
    echo "usage: pve.sh ls|fetch OS|create ID OS [NAME]|start|stop|shutdown ID|snapshot ID NAME|delsnap ID NAME|rollback ID NAME|destroy ID|ip ID" >&2
    exit 2
    ;;
esac
