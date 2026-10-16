#!/usr/bin/env bash
set -euo pipefail

cleanup() {
  ip netns del awgx-s 2>/dev/null || true
  ip netns del awgx-c 2>/dev/null || true
}
trap cleanup EXIT
cleanup

ip netns add awgx-s
ip netns add awgx-c
ip link add veth-s netns awgx-s type veth peer name veth-c netns awgx-c
ip -n awgx-s addr add 10.99.0.1/24 dev veth-s
ip -n awgx-c addr add 10.99.0.2/24 dev veth-c
ip -n awgx-s link set veth-s up
ip -n awgx-c link set veth-c up

sk=$(awg genkey); sp=$(echo "$sk" | awg pubkey)
ck=$(awg genkey); cp=$(echo "$ck" | awg pubkey)
psk=$(awg genpsk); hpk=$(awg genpsk)

common="Jc = 4
Jmin = 40
Jmax = 70
S1 = 15
S2 = 15
S3 = 15
S4 = 15
H1 = 100000-100100
H2 = 200000-200100
H3 = 300000-300100
H4 = 400000-400100
HeaderProtectionKey = $hpk
RandomTrailers = on
DisableCookies = off
ContentPaddingAddition = 0-32
RekeyAfterTime = 100-110
RejectAfterTime = 170-180"

cat > /tmp/s.conf <<CONF
[Interface]
PrivateKey = $sk
ListenPort = 51888
$common

[Peer]
PublicKey = $cp
PresharedKey = $psk
AllowedIPs = 10.98.0.2/32
CONF

cat > /tmp/c.conf <<CONF
[Interface]
PrivateKey = $ck
$common
I1 = <r 32><b 0xdeadbeef><rc 8><rd 4><t>

[Peer]
PublicKey = $sp
PresharedKey = $psk
Endpoint = 10.99.0.1:51888
AllowedIPs = 10.98.0.0/24
PersistentKeepalive = 20-30
CONF

for side in s c; do
  ip -n awgx-$side link add awg0 type amneziawg
  ip netns exec awgx-$side awg setconf awg0 /tmp/$side.conf
  ip -n awgx-$side link set awg0 up
done
ip -n awgx-s addr add 10.98.0.1/24 dev awg0
ip -n awgx-c addr add 10.98.0.2/24 dev awg0

ip netns exec awgx-c ping -c3 -W2 10.98.0.1
ip netns exec awgx-s awg show awg0 dump | head -1 | awk -F'\t' '{print NF" fields"}'
ip netns exec awgx-s awg show awg0 | sed -n '1,30p'
