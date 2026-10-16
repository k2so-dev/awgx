# Egress design notes

## Data plane

All awg interfaces, instances and uplinks, live in the network namespace `awgx` (`/run/netns/awgx`). Each one is created in the host namespace under a temporary name, configured, moved into `awgx` and renamed, so its UDP socket stays in the host namespace: clients and upstream servers still talk to the host address, but peer packets never cross the host routing, forwarding or firewall. The host firewall sees only the UDP listen ports and the connections the relay opens, so ufw, firewalld, docker, `nft flush ruleset` and `ip_forward=0` on the host change nothing for peers.

`awgx-dataplane.service` runs `awgx dataplane`: it creates the namespace (`ip_forward=1`, `rp_filter=0`, TCP settings copied from the host), brings up instances and uplinks, applies the egress snapshot and shaping, then runs the relay. `SIGHUP` reloads `/run/awgx/relay.json`; counters go to `/run/awgx/relay-stats.json` every second. `awgx-serve` starts after it.

### Host access

Every instance gets a veth pair. The host end is named like the instance (`awg0`) and holds the instance address (`10.x.y.1/32`, the IPv6 one, `fe80::1`); the namespace end is `awg0.h` with a /30 from 169.254.200.0/22 and `fe80::2`. The host routes the instance subnets back through it. Peers reach the instance address and every address of the host through this link with their own source address, so services on `10.x.y.1` and firewall rules like `ufw allow in on awg0` work as before, and services the host firewall blocks stay blocked for peers too. The namespace table `51800 + index` holds `default via` the host end; rules at `pref 4005` send `to <host address> iif awg0` there. The data plane checks the host addresses every 5 s and updates the rules when they change.

### Relay

Direct traffic and SOCKS5 or 3x-ui profiles go through the relay built into awgx. nftables `tproxy` in the namespace diverts TCP and UDP of the peers to a relay port: 20000 for direct, `20000 + table - 51700` for a profile. The relay accepts in the namespace and connects from the host namespace, directly or through SOCKS5 CONNECT and UDP ASSOCIATE, so outgoing connections are ordinary local sockets of the host. TCP is copied with `splice`. UDP is full cone per peer source; replies leave through transparent sockets bound to the remote address. The relay refuses loopback, link-local, multicast and host addresses; host addresses reach the host through the veth instead.

ICMP echo of direct peers goes through the TUN device `awgx-echo`: nftables marks echo requests with 0x5a19, table 51797 routes them into the TUN, the relay sends a real ping from the host and writes the reply back with the original destination as source. Ping through SOCKS5 and 3x-ui profiles is not emulated, because it would leave the host outside the proxy. Ping through `iface` profiles is routed by the kernel.

Relay restarts drop open TCP connections; clients reconnect.

## Model

- Draft: `egress_profiles` table plus `instances.egress/killswitch` and `peers.egress/killswitch` columns. Edited by `egress add/set/rm/use`, `peer add/set --egress`, `xui connect`.
- Applied: the newest `egress_snapshots` row with status `confirmed` (`pending` rows of older versions count too). Live network state is always rendered from it; the draft never reaches the kernel without `apply`.
- Snapshot status: `confirmed` once stored, `failed` when converging it failed and the previous snapshot was restored. `rolled_back` and `pending` only exist in databases of older versions. The last 20 are kept.

## Rendering

Pure function `egress.Render(Input) Desired`, unit tested. Everything below lives in the `awgx` namespace.

| Item | Value |
| --- | --- |
| Routing tables | 51701-51796 per profile, 51797 `default dev awgx-echo`, 51798 `local default dev lo` for TPROXY, 51799 for probes, 51800+ per instance to the host |
| `pref 4000` | `to <instance subnets> iif awg0 lookup main suppress_prefixlength 0` (peer to peer, instance addresses) |
| `pref 4005` | `to <host address> iif awg0 lookup <host table>` |
| `pref 4010` | `fwmark 0x5a18 iif awg0 lookup 51798` |
| `pref 4020` | `fwmark 0x5a19 iif awg0 lookup 51797` |
| `pref 4050` | `fwmark 0x5a17 lookup 51799`, only during probes |
| `pref 4100` | per-peer overrides `from <addr> iif awg0` |
| `pref 4900` | instance default `from <subnet> iif awg0` |
| nftables | `input`: relay ports only for packets with mark 0x5a18; `forward`: MSS clamp on egress devices, killswitch drops; `prerouting` (dstnat): DNS dnat; `divert` (dstnat + 10): TPROXY to relay ports and echo marks; `postrouting`: masquerade except host links and routed uplinks |

All rules carry `protocol 158`; rules without it are left alone.

Killswitch and DNS redirection match the peer subnet with explicit peers excepted, so peers added later are covered without a reconcile. The killswitch lets traffic to the host link through. A profile without a usable path keeps only its `unreachable` route, so its peers lose traffic until the watchdog switches to the fallback; a profile without `--fallback` fails closed.

## Apply order

1. Validate the draft, pre-check every used profile (fwmark probe through table 51799: TCP 1.1.1.1:443 or 8.8.8.8:443 and a DNS query; SOCKS5 CONNECT and UDP ASSOCIATE for socks5 and xui), check that the data plane runs, `nft -c` the ruleset. Failure: nothing changed.
2. Store the snapshot as applied.
3. Converge: write the relay spec and reload the relay, routes (replace, then delete extras), rules (add, then delete extras), nftables (atomic).
4. Post-check: a test peer in a network namespace sends TCP and DNS through every used profile. For `iface` profiles the route lookup and the device counters must agree; for relayed ones the relay connection counters of that port must grow. A failed post-check leaves the change applied and reports it; `awgx egress rollback` (or Roll back in the web UI) re-applies the snapshot before it.

Egress changes touch only the `awgx` namespace, and the host rules (`pref 4005`) come before every profile rule, so a broken profile cannot cut SSH or the web UI, not even through the tunnel. That is why there is no confirm window.

Every add and delete is idempotent, so a concurrent data plane start and the watchdog converge to the same state. Reconfiguring a running interface uses `awg syncconf`, which keeps the sessions of the peers.

## 3x-ui

Verified against 3x-ui v3.8.5 and v3.10.2; 3.x is required.

- Panel URL from `x-ui setting -show` (`port`, `webBasePath`, SSL line), always on 127.0.0.1.
- `Authorization: Bearer` API tokens with scopes `admin`, `monitor` and `node-sync`. awgx only lists, adds, updates and deletes inbounds, which `node-sync` allows. Tokens created in the panel are `admin`; recent 3x-ui makes a `node-sync` one with `/usr/local/x-ui/x-ui setting -getApiToken -tokenName awgx -tokenScope node-sync`. `x-ui setting -getApiToken -tokenName cli-fallback` regenerates the `cli-fallback` token (or creates it on a panel without tokens).
- Cookie login (`POST login` with CSRF from `GET csrf-token`) for panels without tokens.
- If `webDomain` is set in the panel, its value is sent as `Host`, otherwise 3x-ui answers 403 to requests for 127.0.0.1. 401 means a bad token, 403 a token without rights.
- Inbound: protocol `mixed` (SOCKS5 with UDP) on 127.0.0.1 and a free port, a generated user and password, sniffing http/tls/quic, tag `awgx-egress`. Endpoints `panel/api/inbounds/{list,add,update/:id,del/:id}`.
- The relay port of the `xui` profile connects to it, so Xray sees the real destination and routing rules, sniffing and balancers of 3x-ui apply as usual. DNS hijack runs first (dstnat), so redirected DNS goes through Xray too.
- Watchdog: SOCKS5 TCP and DNS probes through the inbound plus the 3x-ui API, so a dead Xray or outbound marks xui down and the fallback takes over. Without `--fallback`, connecting with the killswitch off sets the fallback to direct.
- `xui disconnect` moves the routes to direct and applies; the inbound is deleted (or, with `--keep-inbound`, kept on 127.0.0.1 and forgotten) right after the apply. If converging fails and the previous snapshot is restored, 3x-ui stays connected and xui goes back into the draft.
- Major versions above 3 are refused as untested; the running egress is left alone.
