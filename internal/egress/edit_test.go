package egress

import (
	"net/netip"
	"testing"

	"github.com/k2so-dev/awgx/internal/state"
)

func TestRetarget(t *testing.T) {
	q := state.EgressProfile{Name: "s", Kind: "socks5", DNSHijack: true, Socks: &state.Socks{Address: "1.1.1.1:1080", User: "u", Password: "p", NoUDP: true}}
	Retarget(&q, "socks5", "direct", nil, state.Socks{Address: " 2.2.2.2:1080 ", User: "u", NoUDP: true})
	if !q.DNSHijack || q.Socks.Password != "p" || !q.Socks.NoUDP || q.Socks.Address != "2.2.2.2:1080" {
		t.Fatalf("socks edit: %+v %+v", q, q.Socks)
	}
	Retarget(&q, "socks5", "direct", nil, state.Socks{Address: "2.2.2.2:1080"})
	if q.Socks.User != "" || q.Socks.Password != "" {
		t.Fatalf("auth not cleared: %+v", q.Socks)
	}
	Retarget(&q, "iface", "", []string{"awg1", " ", "awg2"}, state.Socks{})
	if q.DNSHijack || q.Socks != nil || len(q.Hops) != 2 {
		t.Fatalf("to iface: %+v", q)
	}
	q.DNSHijack = true
	Retarget(&q, "iface", "", []string{"awg1"}, state.Socks{})
	if !q.DNSHijack {
		t.Fatal("iface edit dropped dns hijack")
	}
	n := state.EgressProfile{Name: "n"}
	Retarget(&n, "socks5", "", nil, state.Socks{Address: "3.3.3.3:1080"})
	if !n.DNSHijack {
		t.Fatal("new socks5 profile without dns hijack")
	}
	x := state.EgressProfile{Name: "xui", Kind: "xui"}
	if Retarget(&x, "socks5", "", nil, state.Socks{Address: "1.1.1.1:1080"}) == nil || x.Kind != "xui" {
		t.Fatal("xui profile retyped")
	}
}

func TestRetargetKeepsVia(t *testing.T) {
	gw := netip.MustParseAddr("192.168.1.1")
	q := state.EgressProfile{Name: "wan2", Kind: "iface", Hops: []state.Hop{{Dev: "eth1", Via: gw}}}
	Retarget(&q, "iface", "direct", []string{"eth1", "awg1"}, state.Socks{})
	if len(q.Hops) != 2 || q.Hops[0].Via != gw || q.Hops[1].Via.IsValid() || q.Fallback != "direct" {
		t.Fatalf("got %+v", q.Hops)
	}
}
