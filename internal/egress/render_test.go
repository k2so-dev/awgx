package egress

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/state"
)

func input() Input {
	inst := state.Instance{
		Name:       "awg0",
		ListenPort: 40000,
		Subnet4:    netip.MustParsePrefix("10.66.1.0/24"),
		Subnet6:    netip.MustParsePrefix("fd00:1::/64"),
		DNS:        []netip.Addr{netip.MustParseAddr("1.1.1.1")},
	}
	peer := func(name, a4, a6 string) state.Peer {
		return state.Peer{Instance: "awg0", Name: name, Address4: netip.MustParseAddr(a4), Address6: netip.MustParseAddr(a6)}
	}
	return Input{
		Instances: []state.Instance{inst},
		Peers:     []state.Peer{peer("a", "10.66.1.2", "fd00:1::2"), peer("b", "10.66.1.3", "fd00:1::3")},
		Config: state.EgressConfig{
			Profiles: []state.EgressProfile{
				{Name: "up", Kind: "iface", Table: 51701, Hops: []state.Hop{{Dev: "awg1"}, {Dev: "awg2"}}, Fallback: state.Direct},
				{Name: "xr", Kind: "socks5", Table: 51702, Socks: &state.Socks{Address: "127.0.0.1:1080"}, DNSHijack: true},
			},
			Default: map[string]state.Assign{"awg0": {Profile: state.Direct}},
			Peers:   map[string]state.Assign{},
		},
		Health: map[string]Health{},
		Up:     map[string]bool{"awg1": true, "awg2": true},
		Has6:   map[string]bool{},
	}
}

func has(t *testing.T, lines []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(lines, w) {
			t.Errorf("missing %q in:\n%s", w, strings.Join(lines, "\n"))
		}
	}
}

func hasNot(t *testing.T, lines []string, prefix string) {
	t.Helper()
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			t.Errorf("unexpected %q", l)
		}
	}
}

func TestDirectOnly(t *testing.T) {
	d := Render(input())
	l := d.Lines()
	hasNot(t, l, "rule -4 pref 4100")
	hasNot(t, l, "rule -4 pref 4900")
	has(t, l,
		"route -4 default dev awg1 table 51701", "route -6 unreachable default table 51701 metric 4278198272",
		"relay :20000 direct",
		"tproxy awg0 10.66.1.0/24 -> :20000",
		"tproxy awg0 fd00:1::/64 -> :20000",
		"rule -4 pref 4010 iif awg0 fwmark 0x5a18 lookup 51798",
		"route -4 local default dev lo table 51798",
		"rule -4 pref 4000 to 10.66.1.0/24 iif awg0 lookup main suppress_prefixlength 0",
	)
	r := netx.Ruleset(d.Nft)
	for _, w := range []string{
		`meta l4proto { tcp, udp } th dport 20000 meta mark != 0x5a18 drop`,
		`iifname "awg0" ip saddr 10.66.1.0/24 ip daddr != 10.66.1.0/24 fib daddr type != local meta l4proto { tcp, udp } tproxy ip to 127.0.0.1:20000 meta mark set 0x5a18 accept`,
		`ip saddr 10.66.1.0/24 oifname != { "awg0", "awgx-echo", "awg0.h" } masquerade`,
	} {
		if !strings.Contains(r, w) {
			t.Errorf("ruleset misses %q:\n%s", w, r)
		}
	}
}

func TestInstanceDefault(t *testing.T) {
	in := input()
	in.Config.Default["awg0"] = state.Assign{Profile: "up"}
	in.Config.Peers["b"] = state.Assign{Profile: state.Direct}
	l := Render(in).Lines()
	has(t, l,
		"rule -4 pref 4000 to 10.66.1.0/24 iif awg0 lookup main suppress_prefixlength 0",
		"rule -6 pref 4000 to fd00:1::/64 iif awg0 lookup main suppress_prefixlength 0",
		"rule -4 pref 4900 from 10.66.1.0/24 iif awg0 lookup 51701",
		"rule -4 pref 4100 from 10.66.1.3/32 iif awg0 lookup main",
		"rule -6 pref 4100 from fd00:1::3/128 iif awg0 lookup main",
		"tproxy awg0 10.66.1.3/32 -> :20000",
		"mss-clamp awg1", "mss-clamp awg2",
	)
	hasNot(t, l, "rule -4 pref 4100 from 10.66.1.2")
	hasNot(t, l, "tproxy awg0 10.66.1.0/24")
}

func TestPeerSocksWithHijackAndKillswitch(t *testing.T) {
	in := input()
	in.Config.Peers["a"] = state.Assign{Profile: "xr", Killswitch: true}
	d := Render(in)
	has(t, d.Lines(),
		"rule -4 pref 4100 from 10.66.1.2/32 iif awg0 lookup 51702",
		"relay :20002 xr socks5 127.0.0.1:1080",
		"tproxy awg0 10.66.1.2/32 -> :20002",
		"tproxy awg0 10.66.1.0/24 -> :20000 except [10.66.1.2 fd00:1::2]",
		"dns awg0 10.66.1.2/32 -> 1.1.1.1",
		"killswitch awg0 10.66.1.2/32 allow []",
	)
	r := netx.Ruleset(d.Nft)
	for _, w := range []string{
		`iifname "awg0" ip saddr 10.66.1.2 oifname != { "awg0", "awg0.h" } drop`,
		`iifname "awg0" ip saddr 10.66.1.2 udp dport 53 dnat ip to 1.1.1.1`,
		`th dport { 20000, 20002 } meta mark != 0x5a18 drop`,
		`tproxy ip to 127.0.0.1:20002 meta mark set 0x5a18 accept`,
	} {
		if !strings.Contains(r, w) {
			t.Errorf("ruleset misses %q:\n%s", w, r)
		}
	}
}

func TestFailover(t *testing.T) {
	in := input()
	in.Config.Default["awg0"] = state.Assign{Profile: "up", Killswitch: true}
	in.Health["up"] = Health{Active: 1}
	l := Render(in).Lines()
	has(t, l, "route -4 default dev awg2 table 51701", "killswitch awg0 10.66.1.0/24 allow [awg1 awg2]")

	in.Health["up"] = Health{Down: true}
	d := Render(in)
	hasNot(t, d.Lines(), "rule -4 pref 4900")
	has(t, d.Lines(), "killswitch awg0 10.66.1.0/24 allow []", "tproxy awg0 10.66.1.0/24 -> :20000")
	if !strings.Contains(netx.Ruleset(d.Nft), `ip saddr 10.66.1.0/24 oifname != { "awg0", "awg0.h" } drop`) {
		t.Error("killswitch must hold when failing over to direct")
	}
}

func TestDownDevHasNoRoute(t *testing.T) {
	in := input()
	in.Up["awg1"] = false
	hasNot(t, Render(in).Lines(), "route -4 default dev awg1")
}

func TestDefaultKillswitchExceptsExplicitPeers(t *testing.T) {
	in := input()
	in.Config.Default["awg0"] = state.Assign{Profile: "xr", Killswitch: true}
	in.Config.Peers["b"] = state.Assign{Profile: state.Direct}
	d := Render(in)
	r := netx.Ruleset(d.Nft)
	for _, w := range []string{
		`iifname "awg0" ip saddr 10.66.1.0/24 ip saddr != 10.66.1.3 oifname != { "awg0", "awg0.h" } drop`,
		`iifname "awg0" ip6 saddr fd00:1::/64 ip6 saddr != fd00:1::3 oifname != { "awg0", "awg0.h" } drop`,
		`iifname "awg0" ip saddr 10.66.1.0/24 ip saddr != 10.66.1.3 udp dport 53 dnat ip to 1.1.1.1`,
		`iifname "awg0" ip saddr 10.66.1.0/24 ip saddr != 10.66.1.3 ip daddr != 10.66.1.0/24 fib daddr type != local meta l4proto { tcp, udp } tproxy ip to 127.0.0.1:20002`,
		`iifname "awg0" ip saddr 10.66.1.3 ip daddr != 10.66.1.0/24 fib daddr type != local meta l4proto { tcp, udp } tproxy ip to 127.0.0.1:20000`,
	} {
		if !strings.Contains(r, w) {
			t.Errorf("ruleset misses %q:\n%s", w, r)
		}
	}
}

func TestPeerKillswitchOnDefaultProfile(t *testing.T) {
	in := input()
	in.Config.Default["awg0"] = state.Assign{Profile: "up", Killswitch: true}
	in.Config.Peers["a"] = state.Assign{Profile: "up", Killswitch: true}
	r := netx.Ruleset(Render(in).Nft)
	for _, w := range []string{
		`iifname "awg0" ip saddr 10.66.1.0/24 ip saddr != 10.66.1.2 oifname != { "awg0", "awg0.h", "awg1", "awg2" } drop`,
		`iifname "awg0" ip saddr 10.66.1.2 oifname != { "awg0", "awg0.h", "awg1", "awg2" } drop`,
	} {
		if !strings.Contains(r, w) {
			t.Errorf("ruleset misses %q:\n%s", w, r)
		}
	}
}

func TestFallbackChainIsPrepared(t *testing.T) {
	in := input()
	in.Config.Profiles[0].Fallback = "xr"
	in.Config.Default["awg0"] = state.Assign{Profile: "up"}
	d := Render(in)
	has(t, d.Lines(), "relay :20002 xr socks5 127.0.0.1:1080", "mss-clamp awg1")
	if got := usedProfiles(in.Config); len(got) != 2 {
		t.Errorf("used profiles %v", got)
	}
}

func TestDownHopFailsClosed(t *testing.T) {
	in := input()
	in.Up = map[string]bool{}
	in.Config.Default["awg0"] = state.Assign{Profile: "xr"}
	lines := Render(in).Lines()
	has(t, lines,
		"route -4 unreachable default table 51702 metric 4278198272",
		"route -6 unreachable default table 51702 metric 4278198272",
	)
	hasNot(t, lines, "route -4 default dev awg1")
}

func TestSocksChangeShowsInPlan(t *testing.T) {
	a, b := input(), input()
	b.Config.Profiles[1].Socks = &state.Socks{Address: "127.0.0.1:1081"}
	if slices.Equal(Render(a).Lines(), Render(b).Lines()) {
		t.Fatal("a SOCKS address change produced no plan line")
	}
}

func TestFitUplinkMTU(t *testing.T) {
	for _, tc := range []struct {
		mtu, link, want int
		warn            bool
	}{
		{1416, 1400, 1316, true},
		{1300, 1400, 1300, false},
		{0, 1500, 1416, false},
	} {
		c := awgconf.Config{Interface: awgconf.Interface{MTU: tc.mtu}, Peers: []awgconf.Peer{{Endpoint: "203.0.113.9:443"}}}
		c.Interface.Obfuscation.S4 = 24
		w := FitUplinkMTU(&c, tc.link)
		if c.Interface.MTU != tc.want || (w != "") != tc.warn {
			t.Errorf("mtu %d link %d: got %d %q", tc.mtu, tc.link, c.Interface.MTU, w)
		}
	}
}

func TestFallbackLoopFailsClosed(t *testing.T) {
	in := input()
	in.Config.Profiles[0].Fallback = "xr"
	in.Config.Profiles[1].Fallback = "up"
	in.Config.Default["awg0"] = state.Assign{Profile: "up"}
	in.Health["up"] = Health{Down: true}
	in.Health["xr"] = Health{Down: true}
	has(t, Render(in).Lines(), "rule -4 pref 4900 from 10.66.1.0/24 iif awg0 lookup 51701")
	if issues, _ := Validate(in); !slices.ContainsFunc(issues, func(s string) bool { return strings.Contains(s, "fallback loop") }) {
		t.Errorf("loop not reported: %v", issues)
	}
}

func TestRelayDown(t *testing.T) {
	in := input()
	in.Config.Peers["a"] = state.Assign{Profile: "xr"}
	in.Health["xr"] = Health{Down: true}
	has(t, Render(in).Lines(), "tproxy awg0 10.66.1.2/32 -> :20002")

	in.Config.Profiles[1].Fallback = "up"
	d := Render(in)
	has(t, d.Lines(), "rule -4 pref 4100 from 10.66.1.2/32 iif awg0 lookup 51701")
	hasNot(t, d.Lines(), "tproxy awg0 10.66.1.2/32")
}

func TestRelayPorts(t *testing.T) {
	p := state.EgressProfile{Name: "x", Kind: "xui", Table: 51705, Socks: &state.Socks{Address: "127.0.0.1:30000", User: "u", Password: "p"}}
	if RelayPort(p) != 20005 {
		t.Fatalf("port %d", RelayPort(p))
	}
	l := relayListener(p)
	if l.Socks == nil || l.Socks.User != "u" || l.Port != 20005 {
		t.Fatalf("listener %+v", l)
	}
	if RelayPort(state.EgressProfile{Kind: "iface", Table: 51701}) != 0 {
		t.Fatal("iface profiles have no relay port")
	}
}

func TestHostAddresses(t *testing.T) {
	in := input()
	in.Root = []netip.Addr{netip.MustParseAddr("10.66.1.1"), netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("2001:db8::5")}
	in.HostTable = map[string]int{"awg0": netx.HostTable}
	d := Render(in)
	has(t, d.Lines(),
		"rule -4 pref 4005 to 203.0.113.5/32 iif awg0 lookup 51800",
		"rule -6 pref 4005 to 2001:db8::5/128 iif awg0 lookup 51800")
	hasNot(t, d.Lines(), "rule -4 pref 4005 to 10.66.1.1/32")
	if rs := netx.Ruleset(d.Nft); !strings.Contains(rs, "ip daddr != { 10.66.1.0/24, 203.0.113.5/32 }") {
		t.Errorf("host address is not excluded from tproxy:\n%s", rs)
	}
}

func TestExplicitSameAsDefault(t *testing.T) {
	in := input()
	in.Config.Peers["a"] = state.Assign{Profile: state.Direct}
	d := Render(in)
	if rs := netx.Ruleset(d.Nft); !strings.Contains(rs, `iifname "awg0" ip saddr 10.66.1.2 ip daddr != 10.66.1.0/24 fib daddr type != local meta l4proto { tcp, udp } tproxy ip to 127.0.0.1:20000`) {
		t.Errorf("explicit direct peer is not diverted:\n%s", rs)
	}
	direct := d.Relay.Listeners[0]
	for _, a := range []string{"10.66.1.2", "10.66.1.3"} {
		if !direct.Owns(netip.MustParseAddr(a)) {
			t.Errorf("direct relay does not own %s", a)
		}
	}
	in.Config.Peers["a"] = state.Assign{Profile: "xr"}
	d = Render(in)
	if d.Relay.Listeners[0].Owns(netip.MustParseAddr("10.66.1.2")) || !d.Relay.Listeners[1].Owns(netip.MustParseAddr("10.66.1.2")) {
		t.Errorf("peer a moved to xr but relay ownership did not follow: %+v", d.Relay.Listeners)
	}
}

func TestEchoNeedsDevice(t *testing.T) {
	routes := func(in Input) int {
		n := 0
		for _, r := range Render(in).Policy.Routes {
			if r.Dev == netx.EchoDev {
				n++
			}
		}
		return n
	}
	in := input()
	if n := routes(in); n != 0 {
		t.Fatalf("echo routes without the device: %d", n)
	}
	in.Up[netx.EchoDev] = true
	if n := routes(in); n != 2 {
		t.Fatalf("echo routes: %d", n)
	}
}
