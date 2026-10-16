package netx

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/k2so-dev/awgx/internal/state"
)

func TestRuleset(t *testing.T) {
	r := Ruleset(Nft{Instances: []state.Instance{{
		Name:       "awg0",
		ListenPort: 40000,
		Subnet4:    netip.MustParsePrefix("10.66.1.0/24"),
		Subnet6:    netip.MustParsePrefix("fd12:3456:789a::/64"),
	}}})
	for _, want := range []string{
		"delete table inet awgx",
		`ip saddr 10.66.1.0/24 oifname != { "awg0", "awgx-echo" } masquerade`,
		`ip6 saddr fd12:3456:789a::/64 oifname != { "awg0", "awgx-echo" } masquerade`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("ruleset misses %q:\n%s", want, r)
		}
	}
}

func TestRulesetIPv6Off(t *testing.T) {
	r := Ruleset(Nft{Instances: []state.Instance{{Name: "awg0", ListenPort: 40000, Subnet4: netip.MustParsePrefix("10.66.1.0/24")}}})
	if strings.Contains(r, "ip6 saddr") {
		t.Fatalf("ip6 masquerade without an IPv6 subnet:\n%s", r)
	}
}

func TestRulesetRelayGuard(t *testing.T) {
	out := Ruleset(Nft{Relay: []int{20000, 20003, 0}})
	if strings.Contains(out, "divert") || !strings.Contains(out, "th dport { 20000, 20003 } meta mark != 0x5a18 drop") {
		t.Fatal(out)
	}
}
