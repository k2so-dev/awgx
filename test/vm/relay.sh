#!/usr/bin/env bash
set -u
pass=0
fail=0
ok() { echo "PASS $1"; pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }
dir="$(dirname "$0")"
source <(sed -n '/^client() {/,/^}/p' "$dir/egress-failures.sh")
probe() { ip netns exec egc awgx egress probe --tcp 1.1.1.1:443 --dns 1.1.1.1 >/dev/null 2>&1; }
tcp() { python3 -c "import json;print({s['port']:s['tcp'] for s in json.load(open('/run/awgx/relay-stats.json'))}.get($1,0))"; }
apply() { awgx egress apply >/dev/null 2>&1; }
pub=$(ip -4 route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')

client
awgx egress use direct >/dev/null
awgx egress use direct --peer eg >/dev/null
apply
probe && ok "explicit direct peer with a direct default has internet" || bad "explicit direct peer has no internet"
ip netns exec egc ping -c 2 -W 2 -q 1.1.1.1 >/dev/null && ok "ping through the relay" || bad "ping fails"
ip netns exec egc ping -c 2 -W 2 -q "$pub" >/dev/null && ok "ping to the host address $pub" || bad "host address $pub unreachable"

ip netns exec egc python3 -c "
import socket
s = socket.create_connection(('1.1.1.1', 443), timeout=5)
s.settimeout(None)
try:
    print('closed' if s.recv(1) == b'' else 'data')
except Exception:
    print('reset')
" > /tmp/hold.txt 2>&1 &
sleep 2
[ "$(tcp 20000)" -ge 1 ] && ok "long TCP flow is relayed directly" || bad "no direct relay flow"
awgx egress use xui --peer eg >/dev/null
apply
sleep 2
grep -q reset /tmp/hold.txt && ok "moving the peer to xui closes its direct flows" || bad "direct flow survived the move: $(cat /tmp/hold.txt)"
probe && ok "peer works through xui after the move" || bad "peer broken after the move"

awgx egress use --inherit --peer eg >/dev/null
apply
ip netns del egc
ip link del egch 2>/dev/null
awgx peer rm eg --yes >/dev/null
echo "RESULT pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
