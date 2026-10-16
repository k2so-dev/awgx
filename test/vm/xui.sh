#!/usr/bin/env bash
set -u
pass=0
fail=0
ok() { echo "PASS $1"; pass=$((pass + 1)); }
bad() { echo "FAIL $1"; fail=$((fail + 1)); }
ns=xc

awgx egress use xui --killswitch >/dev/null
awgx egress apply >/dev/null 2>&1

ip netns del $ns 2>/dev/null
ip link del xch 2>/dev/null
awgx peer rm xc --yes >/dev/null 2>&1
awgx peer add xc --no-qr >/dev/null
awgx peer export xc --format conf > /tmp/xc.conf
port=$(grep -m1 '^Endpoint' /tmp/xc.conf | sed 's/.*://')
ip netns add $ns
ip link add xch type veth peer name eth0 netns $ns
ip addr add 169.254.89.1/30 dev xch
ip link set xch up
ip -n $ns addr add 169.254.89.2/30 dev eth0
ip -n $ns link set eth0 up
ip -n $ns link add awgc type amneziawg
grep -v -e '^Address' -e '^DNS' -e '^MTU' /tmp/xc.conf | sed "s#^Endpoint = .*#Endpoint = 169.254.89.1:$port#" | ip netns exec $ns awg setconf awgc /dev/stdin
ip -n $ns addr add "$(grep -m1 '^Address' /tmp/xc.conf | sed 's/Address = //; s/,.*//')" dev awgc
ip -n $ns link set awgc mtu 1280 up
ip -n $ns route add default dev awgc

probe() { ip netns exec $ns awgx egress probe --tcp 1.1.1.1:443 --dns "$1" >/dev/null 2>&1; }
inbound_up() { awgx xui status --json | sed -n 's/.*"up": \([0-9]*\).*/\1/p'; }
sport=$(awgx egress ls | sed -n 's/^xui .*127\.0\.0\.1:\([0-9]*\).*/\1/p')
rport=$(ip netns exec awgx nft list chain inet awgx divert | sed -n 's/.*tproxy ip to 127\.0\.0\.1:\([0-9]*\).*/\1/p' | grep -v '^20000$' | head -1)

probe 1.1.1.1 && ok "peer TCP and DNS work through 3x-ui" || bad "peer has no traffic through 3x-ui"
host=$(grep -m1 '^Address' /tmp/xc.conf | sed 's/Address = //; s/,.*//; s#\.[0-9]*/.*#.1#')
ip netns exec $ns ping -c 2 -W 2 -q "$host" >/dev/null 2>&1 && ok "killswitch peer reaches the server at $host" || bad "server address $host is unreachable"
probe 8.8.8.8 && ok "hardcoded DNS 8.8.8.8 answered" || bad "hardcoded DNS failed"
[ -n "$rport" ] && ok "peer traffic is diverted into the relay port $rport" || bad "no relay rule for xui"
sleep 12
[ "${up0:=$(inbound_up)}" -gt 0 ] && ok "3x-ui inbound counts traffic ($up0 bytes up)" || bad "inbound counters stay at 0"
ss -Htln "sport = :$sport" | grep -q "127.0.0.1:$sport" && ! ss -Htln "sport = :$sport" | grep -vq "127.0.0.1:$sport" && ok "SOCKS inbound listens on 127.0.0.1 only" || bad "SOCKS inbound is exposed"
ip netns exec awgx nft list chain inet awgx input | grep -q "th dport .*$rport.* meta mark != 0x00005a18 drop" && ok "relay port is reachable only through TPROXY" || bad "relay port is not guarded"

systemctl stop x-ui
probe 1.1.1.1 && bad "traffic leaked while 3x-ui is stopped" || ok "killswitch: no traffic while 3x-ui is stopped"
for _ in $(seq 1 15); do awgx egress ls | grep -q "^xui .* down" && break; sleep 10; done
awgx egress ls | grep -q "^xui .* down" && ok "watchdog marked xui down" || bad "watchdog did not notice"
probe 1.1.1.1 && bad "traffic leaked after watchdog" || ok "still dropped after watchdog"
systemctl start x-ui
for _ in $(seq 1 15); do probe 1.1.1.1 && break; sleep 5; done
probe 1.1.1.1 && ok "recovered after x-ui start" || bad "no recovery after x-ui start"
for _ in $(seq 1 12); do awgx egress ls | grep -q "^xui .* ok" && break; sleep 10; done
awgx egress ls | grep -q "^xui .* ok" && ok "watchdog marked xui healthy" || bad "health not restored"

ip netns del $ns
ip link del xch 2>/dev/null
awgx peer rm xc --yes >/dev/null
awgx egress use direct >/dev/null
awgx egress apply >/dev/null 2>&1
echo "RESULT pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
