#!/usr/bin/env bash
set -u
bin="${1:-/tmp/awgx}"
dir="$(dirname "$0")"
"$dir/fs-snapshot.sh" > /tmp/before.txt
forward="$(cat /proc/sys/net/ipv4/ip_forward)"
start=$(date +%s)
"$bin" install --yes || { echo "RESULT install failed"; exit 1; }
"$bin" peer add t1 --no-qr >/dev/null || { echo "RESULT peer add failed"; exit 1; }
"$bin" peer add t2 --i1 quic --sni example.com --no-qr >/dev/null 2>&1 || { echo "RESULT peer add quic failed"; exit 1; }
echo "install+peers: $(( $(date +%s) - start ))s, kernel $(uname -r), module $(cat /sys/module/amneziawg/version)"
awgx doctor >/dev/null; echo "doctor exit $?"
awgx purge --yes >/dev/null || { echo "RESULT purge failed"; exit 1; }
"$dir/fs-snapshot.sh" > /tmp/after.txt
leftovers="$(diff /tmp/before.txt /tmp/after.txt | grep '^[<>]' | grep -v 'scope transient')"
[ "$(cat /proc/sys/net/ipv4/ip_forward)" = "$forward" ] || leftovers="$leftovers ip_forward"
if [ -n "$leftovers" ]; then
  echo "RESULT leftovers: $leftovers"
  exit 1
fi
echo "RESULT ok"
