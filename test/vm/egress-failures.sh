#!/usr/bin/env bash
set -u
uplink="${1:-awg1}"
pass=0
fail=0
ok() { echo "PASS $1"; pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }

netstate() {
  ip -n awgx -4 rule | grep -v 'lookup 51799'
  ip -n awgx -6 rule | grep -v 'lookup 51799'
  ip -n awgx -4 route show table all | grep -v -e '^local' -e '^broadcast' -e 'table 51799'
  ip -n awgx -6 route show table all | grep -v -e '^local' -e '^multicast' -e '^anycast' -e 'table 51799' | sed 's/expires [0-9]*sec//'
  ip netns exec awgx nft list table inet awgx
  cat /run/awgx/relay.json
  systemctl list-units --plain --no-legend 'awgx-egress-rollback*'
}

same() {
  netstate > /tmp/eg-now.txt
  if diff -q /tmp/eg-base.txt /tmp/eg-now.txt >/dev/null; then ok "$1"; else bad "$1"; diff /tmp/eg-base.txt /tmp/eg-now.txt | head -20; fi
}

client() {
  ns=egc
  ip netns del $ns 2>/dev/null
  ip link del egch 2>/dev/null
  awgx peer rm eg --yes >/dev/null 2>&1
  awgx peer add eg --no-qr >/dev/null
  awgx peer export eg --format conf > /tmp/eg.conf
  port=$(grep -m1 '^Endpoint' /tmp/eg.conf | sed 's/.*://')
  ip netns add $ns
  ip link add egch type veth peer name eth0 netns $ns
  ip addr add 169.254.88.1/30 dev egch
  ip link set egch up
  ip -n $ns addr add 169.254.88.2/30 dev eth0
  ip -n $ns link set eth0 up
  ip -n $ns link add awgc type amneziawg
  grep -v -e '^Address' -e '^DNS' -e '^MTU' /tmp/eg.conf | sed "s#^Endpoint = .*#Endpoint = 169.254.88.1:$port#" | ip netns exec $ns awg setconf awgc /dev/stdin
  ip -n $ns addr add "$(grep -m1 '^Address' /tmp/eg.conf | sed 's/Address = //; s/,.*//')" dev awgc
  ip -n $ns link set awgc mtu 1280 up
  ip -n $ns route add default dev awgc
}

reach() { ip netns exec egc awgx egress probe --tcp 1.1.1.1:443 --dns 1.1.1.1 >/dev/null 2>&1; }

tx() { ip netns exec awgx cat /sys/class/net/$uplink/statistics/tx_packets; }

via_uplink() {
  before=$(tx)
  reach || return 1
  [ "$(tx)" -gt "$before" ]
}

wait_for() {
  for _ in $(seq 1 "$1"); do
    eval "$2" && return 0
    sleep 1
  done
  return 1
}

awgx egress ls --json | grep -q '"name": "up"' || awgx egress add up --type iface --dev "$uplink" >/dev/null
awgx egress use direct >/dev/null
awgx egress use --inherit --peer eg >/dev/null 2>&1
awgx egress apply >/dev/null 2>&1
client
reach && ok "client reaches the internet directly" || bad "client has no internet"
ip netns exec egc ping -c 2 -W 2 -q 1.1.1.1 >/dev/null && ok "ping works on direct" || bad "ping fails on direct"
netstate > /tmp/eg-base.txt

awgx egress use up >/dev/null
if awgx egress apply >/tmp/eg-out.txt 2>&1; then
  via_uplink && ok "applied change routes peer traffic via $uplink" || bad "traffic does not use $uplink"
else
  bad "apply failed: $(tail -3 /tmp/eg-out.txt)"
fi
awgx egress rollback >/dev/null 2>&1
same "rollback without an id restores the exact previous state"
reach && ok "client works after the rollback" || bad "client broken after the rollback"

awgx egress use up >/dev/null
for delay in 0.2 0.6 1.5; do
  awgx egress apply >/dev/null 2>&1 &
  pid=$!
  sleep "$delay"
  kill -9 $pid 2>/dev/null
  wait $pid 2>/dev/null
  systemctl restart awgx-dataplane
  sleep 5
  awgx egress status --json | grep -q '"drift"' && bad "cli killed after ${delay}s: live state differs from the applied snapshot" || ok "cli killed after ${delay}s: live state matches the applied snapshot"
done
awgx egress use direct >/dev/null
awgx egress apply >/dev/null 2>&1
same "direct again after the killed applies: exact baseline"

ip netns exec awgx nft -f - <<'NFT'
table inet egtest {
  chain forward {
    type filter hook forward priority -10; policy accept;
    iifname "awg0" oifname "awg1" drop
  }
}
NFT
awgx egress use up >/dev/null
awgx egress apply >/tmp/eg-out.txt 2>&1 && bad "apply passed with broken forwarding" || {
  grep -q "post-check failed, the change stays applied" /tmp/eg-out.txt && ok "post-check failure reported" || bad "unexpected: $(tail -2 /tmp/eg-out.txt)"
}
ip netns exec awgx nft delete table inet egtest
via_uplink && ok "post-check failure: the change stays applied" || bad "post-check failure: the change is not applied"
awgx egress rollback >/dev/null 2>&1
same "post-check failure: rollback restores the previous state"

ip -n awgx link add egdummy type dummy && ip -n awgx link set egdummy up
awgx egress add dead --type iface --dev egdummy >/dev/null
awgx egress use dead >/dev/null
awgx egress apply >/tmp/eg-out.txt 2>&1 && bad "apply passed through a dead interface" || {
  grep -q "nothing was changed" /tmp/eg-out.txt && ok "pre-check failure detected" || bad "unexpected: $(tail -2 /tmp/eg-out.txt)"
}
awgx egress use direct >/dev/null
awgx egress rm dead >/dev/null
ip -n awgx link del egdummy
same "pre-check failure: nothing changed"

awgx egress use up --killswitch >/dev/null
awgx egress apply >/dev/null 2>&1 && ok "apply with the killswitch" || bad "apply with the killswitch"
via_uplink && ok "applied change routes via $uplink" || bad "applied change does not use $uplink"
ip -n awgx link set "$uplink" down
reach && bad "killswitch leaked traffic while $uplink is down" || ok "killswitch drops traffic while $uplink is down"
systemctl restart awgx-dataplane
wait_for 60 via_uplink && ok "traffic returns to $uplink after it comes back" || bad "no recovery after $uplink came back"

awgx egress set up --fallback direct >/dev/null
awgx egress use up >/dev/null
awgx egress apply >/dev/null 2>&1
ip -n awgx link set "$uplink" down
wait_for 60 reach && ok "with fallback direct traffic moves to direct" || bad "no fallback to direct"
systemctl restart awgx-dataplane
wait_for 60 via_uplink && ok "recovered after uplink restart" || bad "no recovery"

id=$(awgx egress status --json | grep -m1 '"id"' | tr -dc 0-9)
awgx egress use direct >/dev/null
awgx egress apply >/dev/null 2>&1
awgx egress rollback "$id" >/dev/null 2>&1 && via_uplink && ok "manual rollback to snapshot $id" || bad "manual rollback to snapshot $id"

awgx egress use direct >/dev/null
awgx egress apply >/dev/null 2>&1
awgx egress set up --fallback "" >/dev/null
same "back to direct: exact baseline"

ip netns del egc
ip link del egch 2>/dev/null
awgx peer rm eg --yes >/dev/null
echo "RESULT pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
