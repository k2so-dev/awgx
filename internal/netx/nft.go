package netx

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

const Table = "awgx"

type Killswitch struct {
	Iface  string
	Host   string
	Addrs  []netip.Prefix
	Except []netip.Addr
	Allow  []string
}

type Hijack struct {
	Iface  string
	Addrs  []netip.Prefix
	Except []netip.Addr
	To     netip.Addr
}

type Tproxy struct {
	Iface  string
	Addrs  []netip.Prefix
	Except []netip.Addr
	Local  []netip.Prefix
	Port   int
	Echo   bool
}

type Nft struct {
	Instances  []state.Instance
	HostLinks  []string
	ClampDevs  []string
	NoMasq     []string
	Killswitch []Killswitch
	Hijack     []Hijack
	Tproxy     []Tproxy
	Relay      []int
}

func Ruleset(n Nft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {}\ndelete table inet %s\n", Table, Table)
	fmt.Fprintf(&b, "table inet %s {\n", Table)
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n")
	if ports := ports(n.Relay); ports != "" {
		fmt.Fprintf(&b, "\t\tmeta l4proto { tcp, udp } th dport %s meta mark != 0x%x drop\n", ports, TproxyMark)
	}
	b.WriteString("\t}\n")
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	if len(n.ClampDevs) > 0 {
		fmt.Fprintf(&b, "\t\toifname %s tcp flags syn tcp option maxseg size set rt mtu\n", set(n.ClampDevs))
		fmt.Fprintf(&b, "\t\tiifname %s tcp flags syn tcp option maxseg size set rt mtu\n", set(n.ClampDevs))
	}
	for _, i := range n.Instances {
		if i.MTU <= 0 {
			continue
		}
		fmt.Fprintf(&b, "\t\tiifname %q meta nfproto ipv4 tcp flags syn tcp option maxseg size > %d tcp option maxseg size set %d\n", i.Name, i.MTU-40, i.MTU-40)
		fmt.Fprintf(&b, "\t\tiifname %q meta nfproto ipv6 tcp flags syn tcp option maxseg size > %d tcp option maxseg size set %d\n", i.Name, i.MTU-60, i.MTU-60)
		fmt.Fprintf(&b, "\t\toifname %q tcp flags syn tcp option maxseg size set rt mtu\n", i.Name)
	}
	for _, k := range n.Killswitch {
		allow := []string{k.Iface}
		if k.Host != "" {
			allow = append(allow, k.Host)
		}
		for _, family := range []string{"ip", "ip6"} {
			if m := match(family, k.Addrs, k.Except); m != "" {
				fmt.Fprintf(&b, "\t\tiifname %q %s oifname != %s drop\n", k.Iface, m, set(append(allow, k.Allow...)))
			}
		}
	}
	b.WriteString("\t}\n")
	if len(n.Hijack) > 0 {
		b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
		for _, h := range n.Hijack {
			family := "ip"
			if h.To.Is6() {
				family = "ip6"
			}
			m := match(family, h.Addrs, h.Except)
			if m == "" {
				continue
			}
			for _, proto := range []string{"udp", "tcp"} {
				fmt.Fprintf(&b, "\t\tiifname %q %s %s dport 53 dnat %s to %s\n", h.Iface, m, proto, family, h.To)
			}
		}
		b.WriteString("\t}\n")
	}
	if len(n.Tproxy) > 0 {
		b.WriteString("\tchain divert {\n\t\ttype filter hook prerouting priority dstnat + 10; policy accept;\n")
		for _, t := range n.Tproxy {
			for _, family := range []string{"ip", "ip6"} {
				m := match(family, t.Addrs, t.Except)
				if m == "" {
					continue
				}
				to := "127.0.0.1"
				if family == "ip6" {
					to = "[::1]"
				}
				if local := daddrs(family, t.Local); local != "" {
					m += " " + family + " daddr != " + local
				}
				fmt.Fprintf(&b, "\t\tiifname %q %s fib daddr type != local meta l4proto { tcp, udp } tproxy %s to %s:%d meta mark set 0x%x accept\n", t.Iface, m, family, to, t.Port, TproxyMark)
				if t.Echo {
					icmp := "icmp"
					if family == "ip6" {
						icmp = "icmpv6"
					}
					fmt.Fprintf(&b, "\t\tiifname %q %s fib daddr type != local %s type echo-request meta mark set 0x%x accept\n", t.Iface, m, icmp, EchoMark)
				}
			}
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	for _, i := range n.Instances {
		skip := set(append(append([]string{i.Name, EchoDev}, n.HostLinks...), n.NoMasq...))
		fmt.Fprintf(&b, "\t\tip saddr %s oifname != %s masquerade\n", i.Subnet4.Masked(), skip)
		if i.IPv6() {
			fmt.Fprintf(&b, "\t\tip6 saddr %s oifname != %s masquerade\n", i.Subnet6.Masked(), skip)
		}
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

func ports(ps []int) string {
	var out []string
	for _, p := range ps {
		if p > 0 {
			out = append(out, strconv.Itoa(p))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return elems(out)
}

func daddrs(family string, prefixes []netip.Prefix) string {
	var out []string
	for _, p := range prefixes {
		if p.IsValid() && p.Addr().Is6() == (family == "ip6") {
			out = append(out, p.Masked().String())
		}
	}
	if len(out) == 0 {
		return ""
	}
	return elems(out)
}

func set(items []string) string {
	if len(items) == 1 {
		return fmt.Sprintf("%q", items[0])
	}
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "{ " + strings.Join(q, ", ") + " }"
}

func match(family string, prefixes []netip.Prefix, except []netip.Addr) string {
	is6 := family == "ip6"
	var in, out []string
	for _, p := range prefixes {
		if p.IsValid() && p.Addr().Is6() == is6 {
			if p.IsSingleIP() {
				in = append(in, p.Addr().String())
			} else {
				in = append(in, p.String())
			}
		}
	}
	for _, a := range except {
		if a.IsValid() && a.Is6() == is6 {
			out = append(out, a.String())
		}
	}
	if len(in) == 0 {
		return ""
	}
	m := family + " saddr " + elems(in)
	if len(out) > 0 {
		m += " " + family + " saddr != " + elems(out)
	}
	return m
}

func elems(s []string) string {
	slices.Sort(s)
	s = slices.Compact(s)
	if len(s) == 1 {
		return s[0]
	}
	return "{ " + strings.Join(s, ", ") + " }"
}

func ApplyRuleset(ctx context.Context, run sysx.Runner, ruleset string) error {
	_, err := run.RunInput(ctx, []byte(ruleset), "nft", "-f", "-")
	return err
}

func CheckRuleset(ctx context.Context, run sysx.Runner, ruleset string) error {
	_, err := run.RunInput(ctx, []byte(ruleset), "nft", "-c", "-f", "-")
	return err
}

func RemoveTable(ctx context.Context, run sysx.Runner) error {
	if !TableExists(ctx, run) {
		return nil
	}
	_, err := run.Run(ctx, "nft", "delete", "table", "inet", Table)
	return err
}

func TableExists(ctx context.Context, run sysx.Runner) bool {
	_, err := run.Run(ctx, "nft", "list", "table", "inet", Table)
	return err == nil
}
