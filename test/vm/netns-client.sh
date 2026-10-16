#!/usr/bin/env bash
set -eu
conf="$1"
ns="${2:-awgx-client}"
net="${3:-77}"
port=$(grep -m1 '^Endpoint' "$conf" | sed 's/.*://')
ip netns del "$ns" 2>/dev/null || true
ip link del "${ns:0:10}h" 2>/dev/null || true
ip netns add "$ns"
ip link add "${ns:0:10}h" type veth peer name eth0 netns "$ns"
ip addr add 169.254.$net.1/30 dev "${ns:0:10}h"
ip link set "${ns:0:10}h" up
ip -n "$ns" addr add 169.254.$net.2/30 dev eth0
ip -n "$ns" link set eth0 up
ip -n "$ns" link add awgc type amneziawg
grep -v -e '^Address' -e '^DNS' -e '^MTU' "$conf" | sed "s#^Endpoint = .*#Endpoint = 169.254.$net.1:$port#" | ip netns exec "$ns" awg setconf awgc /dev/stdin
addr=$(grep -m1 '^Address' "$conf" | sed 's/Address = //; s/,.*//')
ip -n "$ns" addr add "$addr" dev awgc
ip -n "$ns" link set awgc up
ip -n "$ns" route add 10.0.0.0/8 dev awgc
