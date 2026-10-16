package egress

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/relay"
	"github.com/k2so-dev/awgx/internal/state"
)

const KillMetric = 4278198272

type Health struct {
	Active int  `json:"active"`
	Down   bool `json:"down"`
}

type Input struct {
	Instances []state.Instance
	Peers     []state.Peer
	Config    state.EgressConfig
	Uplinks   []state.Uplink
	Health    map[string]Health
	Has6      map[string]bool
	Up        map[string]bool
	MTU       map[string]int
	Root      []netip.Addr
	HostTable map[string]int
}

type Desired struct {
	Policy   netx.Policy
	Nft      netx.Nft
	Relay    relay.Spec
	Profiles []string
}

func profileLine(p state.EgressProfile) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("profile %s %s fallback=%s config=%x", p.Name, p.Kind, fallbackName(p.Fallback), sum[:4])
}

func fallbackName(s string) string {
	if s == "" {
		return "drop"
	}
	return s
}

func (d Desired) Lines() []string {
	out := append(d.Policy.Lines(), d.Profiles...)
	for _, l := range d.Relay.Listeners {
		line := fmt.Sprintf("relay :%d %s", l.Port, l.Name)
		if l.Socks != nil {
			line += " socks5 " + l.Socks.Address
		}
		out = append(out, line)
	}
	for _, k := range d.Nft.Killswitch {
		for _, a := range k.Addrs {
			out = append(out, fmt.Sprintf("killswitch %s %s allow %v%s", k.Iface, a, k.Allow, except(k.Except)))
		}
	}
	for _, h := range d.Nft.Hijack {
		for _, a := range h.Addrs {
			out = append(out, fmt.Sprintf("dns %s %s -> %s%s", h.Iface, a, h.To, except(h.Except)))
		}
	}
	for _, t := range d.Nft.Tproxy {
		for _, a := range t.Addrs {
			out = append(out, fmt.Sprintf("tproxy %s %s -> :%d%s", t.Iface, a, t.Port, except(t.Except)))
		}
	}
	for _, dev := range d.Nft.ClampDevs {
		out = append(out, "mss-clamp "+dev)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func except(as []netip.Addr) string {
	if len(as) == 0 {
		return ""
	}
	return fmt.Sprintf(" except %v", as)
}

func RelayPort(p state.EgressProfile) int {
	if !p.Relayed() || p.Socks == nil {
		return 0
	}
	return netx.RelayBase + p.Table - netx.TableMin
}

func HopDevs(p state.EgressProfile) []string {
	var out []string
	for _, h := range p.Hops {
		out = append(out, h.Dev)
	}
	return out
}

func ActiveHop(p state.EgressProfile, h Health) state.Hop {
	if p.Relayed() {
		return state.Hop{}
	}
	if h.Active >= 0 && h.Active < len(p.Hops) {
		return p.Hops[h.Active]
	}
	return p.Hops[0]
}

func (in Input) Chain(name string) []state.EgressProfile {
	return Chain(in.Config, name)
}

func Chain(cfg state.EgressConfig, name string) []state.EgressProfile {
	var out []state.EgressProfile
	seen := map[string]bool{}
	for name != "" && name != state.Direct && !seen[name] {
		seen[name] = true
		p, ok := cfg.Profile(name)
		if !ok {
			break
		}
		out = append(out, p)
		name = p.Fallback
	}
	return out
}

func (in Input) Effective(name string) (state.EgressProfile, bool) {
	chain := in.Chain(name)
	for _, p := range chain {
		if !in.Health[p.Name].Down || p.Fallback == "" {
			return p, true
		}
	}
	if len(chain) > 0 && chain[len(chain)-1].Fallback != state.Direct {
		return chain[0], true
	}
	return state.EgressProfile{}, false
}

type divert struct {
	port int
	kind string
}

type group struct {
	addrs  []netip.Prefix
	except []netip.Addr
	allow  []string
}

func relayListener(p state.EgressProfile) relay.Listener {
	l := relay.Listener{Port: RelayPort(p), Name: p.Name}
	if p.Socks != nil {
		l.Socks = &relay.Socks{Address: p.Socks.Address, User: p.Socks.User, Password: p.Socks.Password, NoUDP: p.Socks.NoUDP}
	}
	return l
}

func Render(in Input) Desired {
	var d Desired
	_, echo := in.Up[netx.EchoDev]
	clamp := map[string]bool{}
	listeners := map[int]relay.Listener{}
	if len(in.Instances) > 0 {
		listeners[netx.RelayBase] = relay.Listener{Port: netx.RelayBase, Name: state.Direct}
	}
	use := func(name string) {
		for _, prof := range in.Chain(name) {
			if _, ok := listeners[RelayPort(prof)]; !ok && RelayPort(prof) > 0 {
				listeners[RelayPort(prof)] = relayListener(prof)
			}
			for _, dev := range HopDevs(prof) {
				clamp[dev] = true
			}
		}
	}
	portOf := func(p state.EgressProfile, on bool) int {
		if !on {
			return netx.RelayBase
		}
		return RelayPort(p)
	}
	for _, p := range in.Config.Profiles {
		d.Profiles = append(d.Profiles, profileLine(p))
		for _, f := range []int{4, 6} {
			d.Policy.Routes = append(d.Policy.Routes, netx.Route{Family: f, Table: p.Table, Type: "unreachable", Metric: KillMetric})
		}
		if p.Relayed() {
			continue
		}
		hop := ActiveHop(p, in.Health[p.Name])
		if !in.Up[hop.Dev] {
			continue
		}
		d.Policy.Routes = append(d.Policy.Routes, netx.Route{Family: 4, Table: p.Table, Dev: hop.Dev, Via: v4(hop.Via)})
		if hop.Via.IsValid() && hop.Via.Is6() {
			d.Policy.Routes = append(d.Policy.Routes, netx.Route{Family: 6, Table: p.Table, Dev: hop.Dev, Via: hop.Via})
		} else if !hop.Via.IsValid() && in.Has6[hop.Dev] {
			d.Policy.Routes = append(d.Policy.Routes, netx.Route{Family: 6, Table: p.Table, Dev: hop.Dev})
		}
	}
	var local []netip.Prefix
	for _, inst := range in.Instances {
		local = append(local, validPrefixes(inst.Subnet4.Masked(), inst.Subnet6.Masked())...)
	}
	for _, inst := range in.Instances {
		subnets := validPrefixes(inst.Subnet4.Masked(), inst.Subnet6.Masked())
		def := in.Config.Default[inst.Name]
		defEff, defOn := in.Effective(def.Profile)
		use(def.Profile)
		kill := map[string]*group{}
		hijack := map[string]*group{}
		tproxy := map[divert]*group{}
		add := func(g *group, allow []string, addrs []netip.Prefix, except []netip.Addr) *group {
			if g == nil {
				g = &group{allow: allow}
			}
			g.addrs = append(g.addrs, addrs...)
			g.except = append(g.except, except...)
			return g
		}
		var explicit []netip.Addr
		for _, p := range in.Peers {
			if p.Instance != inst.Name {
				continue
			}
			a, ok := in.Config.Peers[p.Name]
			if !ok || a.Profile == "" {
				continue
			}
			addrs := validAddrs(p.Address4, p.Address6)
			explicit = append(explicit, addrs...)
			use(a.Profile)
			eff, on := in.Effective(a.Profile)
			if on != defOn || eff.Name != defEff.Name {
				table := netx.MainTable
				if on {
					table = eff.Table
				}
				for _, addr := range addrs {
					d.Policy.Rules = append(d.Policy.Rules, netx.Rule{Family: family(addr), Pref: netx.PrefPeer,
						From: netip.PrefixFrom(addr, addr.BitLen()), IIf: inst.Name, Table: table})
				}
			}
			hosts := hostPrefixes(addrs)
			if a.Killswitch && a.Profile != state.Direct {
				kill["peer:"+eff.Name] = add(kill["peer:"+eff.Name], allowed(eff, on), hosts, nil)
			}
			if on && eff.DNSHijack {
				hijack["peer"] = add(hijack["peer"], nil, hosts, nil)
			}
			if port := portOf(eff, on); port > 0 {
				k := divert{port, "peer"}
				tproxy[k] = add(tproxy[k], nil, hosts, nil)
			}
		}
		if def.Killswitch && def.Profile != "" && def.Profile != state.Direct {
			kill["default"] = add(kill["default"], allowed(defEff, defOn), subnets, explicit)
		}
		if defOn && defEff.DNSHijack {
			hijack["default"] = add(hijack["default"], nil, subnets, explicit)
		}
		if port := portOf(defEff, defOn); port > 0 {
			k := divert{port, "default"}
			tproxy[k] = add(tproxy[k], nil, subnets, explicit)
		}
		for _, to := range local {
			d.Policy.Rules = append(d.Policy.Rules, netx.Rule{Family: family(to.Addr()), Pref: netx.PrefLocal,
				To: to, IIf: inst.Name, Table: netx.MainTable, SuppressDefault: true})
		}
		if defOn {
			for _, s := range subnets {
				d.Policy.Rules = append(d.Policy.Rules, netx.Rule{Family: family(s.Addr()), Pref: netx.PrefDefault,
					From: s, IIf: inst.Name, Table: defEff.Table})
			}
		}
		for _, name := range sortedKeys(kill) {
			g := kill[name]
			d.Nft.Killswitch = append(d.Nft.Killswitch, netx.Killswitch{Iface: inst.Name, Host: core.HostPeer(inst.Name), Addrs: g.addrs, Except: g.except, Allow: g.allow})
		}
		for _, key := range sortedKeys(hijack) {
			g := hijack[key]
			for _, dns := range firstPerFamily(inst.DNS) {
				d.Nft.Hijack = append(d.Nft.Hijack, netx.Hijack{Iface: inst.Name, Addrs: g.addrs, Except: g.except, To: dns})
			}
		}
		skip := local
		if t := in.HostTable[inst.Name]; t > 0 {
			skip = slices.Clone(local)
			for _, a := range in.Root {
				if slices.ContainsFunc(local, func(p netip.Prefix) bool { return p.Contains(a) }) {
					continue
				}
				to := netip.PrefixFrom(a, a.BitLen())
				skip = append(skip, to)
				d.Policy.Rules = append(d.Policy.Rules, netx.Rule{Family: family(a), Pref: netx.PrefHost, To: to, IIf: inst.Name, Table: t})
			}
		}
		for _, k := range slices.SortedFunc(maps.Keys(tproxy), func(a, b divert) int { return cmp.Or(a.port-b.port, strings.Compare(a.kind, b.kind)) }) {
			g := tproxy[k]
			d.Nft.Tproxy = append(d.Nft.Tproxy, netx.Tproxy{Iface: inst.Name, Addrs: g.addrs, Except: g.except, Local: skip, Port: k.port, Echo: echo && k.port == netx.RelayBase})
			l := listeners[k.port]
			l.Sources = append(l.Sources, relay.Match{Addrs: g.addrs, Except: g.except})
			listeners[k.port] = l
		}
		for _, f := range []int{4, 6} {
			d.Policy.Rules = append(d.Policy.Rules,
				netx.Rule{Family: f, Pref: netx.PrefTproxy, IIf: inst.Name, FwMark: netx.TproxyMark, Table: netx.TproxyTable},
				netx.Rule{Family: f, Pref: netx.PrefEcho, IIf: inst.Name, FwMark: netx.EchoMark, Table: netx.EchoTable})
		}
		d.Nft.HostLinks = append(d.Nft.HostLinks, core.HostPeer(inst.Name))
	}
	if len(in.Instances) > 0 {
		for _, f := range []int{4, 6} {
			d.Policy.Routes = append(d.Policy.Routes, netx.Route{Family: f, Table: netx.TproxyTable, Type: "local", Dev: "lo"})
			if echo {
				d.Policy.Routes = append(d.Policy.Routes, netx.Route{Family: f, Table: netx.EchoTable, Dev: netx.EchoDev})
			}
		}
	}
	for _, port := range slices.Sorted(maps.Keys(listeners)) {
		d.Relay.Listeners = append(d.Relay.Listeners, listeners[port])
		d.Nft.Relay = append(d.Nft.Relay, port)
	}
	d.Nft.ClampDevs = sortedKeys(clamp)
	for _, u := range in.Uplinks {
		if u.Routed {
			d.Nft.NoMasq = append(d.Nft.NoMasq, u.Name)
		}
	}
	d.Nft.Instances = in.Instances
	return d
}

func allowed(p state.EgressProfile, on bool) []string {
	if !on {
		return nil
	}
	return HopDevs(p)
}

func validPrefixes(ps ...netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range ps {
		if p.IsValid() {
			out = append(out, p)
		}
	}
	return out
}

func validAddrs(as ...netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, a := range as {
		if a.IsValid() {
			out = append(out, a)
		}
	}
	return out
}

func hostPrefixes(as []netip.Addr) []netip.Prefix {
	out := make([]netip.Prefix, len(as))
	for i, a := range as {
		out[i] = netip.PrefixFrom(a, a.BitLen())
	}
	return out
}

func v4(a netip.Addr) netip.Addr {
	if a.IsValid() && a.Is4() {
		return a
	}
	return netip.Addr{}
}

func family(a netip.Addr) int {
	if a.Is4() {
		return 4
	}
	return 6
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstPerFamily(addrs []netip.Addr) []netip.Addr {
	var out []netip.Addr
	var has4, has6 bool
	for _, a := range addrs {
		if a.Is4() && !has4 {
			out, has4 = append(out, a), true
		}
		if a.Is6() && !has6 {
			out, has6 = append(out, a), true
		}
	}
	return out
}
