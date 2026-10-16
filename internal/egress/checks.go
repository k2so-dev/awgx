package egress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/relay"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/state"
)

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	ifaceRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)
)

var reservedNames = []string{"inherit", "drop"}

func ValidName(name string) error {
	if !nameRe.MatchString(name) || name == state.Direct {
		return fmt.Errorf("invalid egress profile name %q", name)
	}
	if slices.Contains(reservedNames, name) {
		return fmt.Errorf("egress profile name %q is reserved", name)
	}
	return nil
}

func Validate(in Input) (issues, warnings []string) {
	bad := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	names := map[string]bool{}
	tables := map[int]bool{}
	var instNames []string
	for _, i := range in.Instances {
		instNames = append(instNames, i.Name)
	}
	for _, p := range in.Config.Profiles {
		if err := ValidName(p.Name); err != nil && slices.Contains(reservedNames, p.Name) {
			warn("%v: inherit and drop mean no profile in routes and fallbacks, add it again under another name and move its routes", err)
		} else if err != nil {
			bad("%v", err)
		}
		if names[p.Name] {
			bad("duplicate profile %s", p.Name)
		}
		names[p.Name] = true
		if p.Table <= netx.TableMin || p.Table > netx.TableMax || tables[p.Table] {
			bad("profile %s: routing table %d is outside %d-%d or reused", p.Name, p.Table, netx.TableMin+1, netx.TableMax)
		}
		tables[p.Table] = true
		switch p.Kind {
		case "iface":
			if len(p.Hops) == 0 {
				bad("profile %s: no interfaces", p.Name)
			}
			for _, h := range p.Hops {
				if !ifaceRe.MatchString(h.Dev) || slices.Contains(instNames, h.Dev) {
					bad("profile %s: invalid interface %q", p.Name, h.Dev)
					continue
				}
				if _, ok := in.Up[h.Dev]; !ok {
					bad("profile %s: interface %s does not exist in the %s namespace, add it with awgx uplink add", p.Name, h.Dev, netx.NS)
				}
				for _, inst := range in.Instances {
					if m := in.MTU[h.Dev]; m > 0 && inst.MTU > m {
						warn("profile %s: peer MTU %d of %s is larger than %s MTU %d; TCP MSS is clamped, but UDP/QUIC packets may be lost: after apply run awgx instance set %s --mtu auto and re-import client configs", p.Name, inst.MTU, inst.Name, h.Dev, m, inst.Name)
					}
				}
			}
		case "socks5", "xui":
			if p.Socks == nil {
				bad("profile %s: no SOCKS5 server", p.Name)
			} else if _, err := ParseSocksAddress(p.Socks.Address); err != nil {
				bad("profile %s: %v", p.Name, err)
			} else if p.Socks.NoUDP {
				warn("profile %s: the SOCKS5 server has no UDP, plain DNS of peers will not resolve through it, use DNS over HTTPS on clients", p.Name)
			}
		default:
			bad("profile %s: unknown type %q", p.Name, p.Kind)
		}
	}
	for _, p := range in.Config.Profiles {
		if p.Fallback != "" && p.Fallback != state.Direct && (!names[p.Fallback] || p.Fallback == p.Name) {
			bad("profile %s: invalid fallback %q", p.Name, p.Fallback)
		} else if chain := in.Chain(p.Name); len(chain) > 1 && chain[len(chain)-1].Fallback == p.Name {
			bad("profile %s: fallback loop", p.Name)
		}
	}
	known := func(name string) bool { return name == "" || name == state.Direct || names[name] }
	for inst, a := range in.Config.Default {
		if !known(a.Profile) {
			bad("instance %s: unknown egress profile %q", inst, a.Profile)
		}
	}
	for peer, a := range in.Config.Peers {
		if !known(a.Profile) {
			bad("peer %s: unknown egress profile %q", peer, a.Profile)
		}
	}
	if len(usedProfiles(in.Config)) > 0 {
		for _, inst := range in.Instances {
			for _, dns := range inst.DNS {
				if dns.IsPrivate() || dns.IsLoopback() || dns.IsLinkLocalUnicast() {
					warn("instance %s: DNS %s is a private address, its queries may bypass egress or not reach it", inst.Name, dns)
				}
			}
		}
	}
	return issues, warnings
}

func usedProfiles(cfg state.EgressConfig) []state.EgressProfile {
	names := map[string]bool{}
	var assigned []string
	for _, a := range cfg.Default {
		assigned = append(assigned, a.Profile)
	}
	for _, a := range cfg.Peers {
		assigned = append(assigned, a.Profile)
	}
	for _, name := range assigned {
		for _, p := range Chain(cfg, name) {
			names[p.Name] = true
		}
	}
	var used []state.EgressProfile
	for _, p := range cfg.Profiles {
		if names[p.Name] {
			used = append(used, p)
		}
	}
	return used
}

func (e *Engine) dnsServer() netip.Addr {
	insts, _ := e.Store.Instances()
	for _, i := range insts {
		for _, d := range i.DNS {
			if d.Is4() {
				return d
			}
		}
	}
	return netip.MustParseAddr("1.1.1.1")
}

func (e *Engine) ProbeHop(ctx context.Context, h state.Hop) error {
	return e.withProbe(ctx, h, func() error {
		return netx.Do(func() error {
			if err := ProbeTCPConnect(ctx, netx.ProbeMark, ProbeTCP); err != nil {
				return err
			}
			return ProbeDNS(ctx, netx.ProbeMark, e.dnsServer())
		})
	})
}

func (e *Engine) withProbe(ctx context.Context, h state.Hop, fn func() error) error {
	if !e.App.InterfaceUp(h.Dev) {
		return fmt.Errorf("interface %s is missing", h.Dev)
	}
	ns := e.App.NS()
	netx.ClearProbe(ctx, ns)
	defer netx.ClearProbe(context.WithoutCancel(ctx), ns)
	if err := netx.AddRoute(ctx, ns, netx.Route{Family: 4, Table: netx.ProbeTable, Type: "unreachable", Metric: KillMetric}); err != nil {
		return err
	}
	if err := netx.AddRoute(ctx, ns, netx.Route{Family: 4, Table: netx.ProbeTable, Dev: h.Dev, Via: v4(h.Via)}); err != nil {
		return err
	}
	if err := netx.AddRule(ctx, ns, netx.Rule{Family: 4, Pref: netx.PrefProbe, FwMark: netx.ProbeMark, Table: netx.ProbeTable}); err != nil {
		return err
	}
	return fn()
}

func (e *Engine) probeRelay(ctx context.Context, p state.EgressProfile) error {
	if p.Socks == nil {
		return errors.New("no SOCKS5 server configured")
	}
	return ProbeSocks(ctx, *relayListener(p).Socks, e.dnsServer())
}

func (e *Engine) dataplaneActive(ctx context.Context) error {
	if _, err := e.App.Run.Run(ctx, "systemctl", "is-active", "--quiet", core.DataplaneUnit); err != nil {
		return fmt.Errorf("%s is not running: systemctl status %s", core.DataplaneUnit, core.DataplaneUnit)
	}
	return nil
}

func (e *Engine) PreChecks(ctx context.Context, cfg state.EgressConfig) ([]string, error) {
	var checks []string
	var errs []error
	if err := e.dataplaneActive(ctx); err != nil {
		return []string{"FAIL " + err.Error()}, err
	}
	health := map[string]Health{}
	for _, p := range usedProfiles(cfg) {
		if p.Relayed() {
			if err := e.probeRelay(ctx, p); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
				checks = append(checks, fmt.Sprintf("FAIL %s: %v", p.Name, err))
				continue
			}
			checks = append(checks, fmt.Sprintf("ok   %s: socks5 %s reachable, TCP and DNS through it work", p.Name, p.Socks.Address))
			continue
		}
		active := -1
		for i, h := range p.Hops {
			if err := e.ProbeHop(ctx, h); err != nil {
				checks = append(checks, fmt.Sprintf("FAIL %s: via %s: %v", p.Name, h.Dev, err))
				continue
			}
			checks = append(checks, fmt.Sprintf("ok   %s: via %s TCP and DNS work", p.Name, h.Dev))
			if active < 0 {
				active = i
			}
		}
		if active < 0 {
			errs = append(errs, fmt.Errorf("%s: no working interface", p.Name))
		} else if active > 0 {
			health[p.Name] = Health{Active: active}
		}
	}
	in, err := e.Input(ctx, cfg)
	if err != nil {
		return checks, err
	}
	if err := netx.CheckRuleset(ctx, e.App.NS(), netx.Ruleset(Render(in).Nft)); err != nil {
		errs = append(errs, fmt.Errorf("nftables: %w", err))
	}
	if len(errs) == 0 {
		e.SetHealth(health)
	}
	return checks, errors.Join(errs...)
}

func (e *Engine) PostChecks(ctx context.Context, cfg state.EgressConfig) ([]string, error) {
	insts, err := e.Store.Instances()
	if err != nil {
		return nil, err
	}
	health := e.Health()
	in, err := e.Input(ctx, cfg)
	if err != nil {
		return nil, err
	}
	nft := Render(in).Nft
	ns := e.App.NS()
	defer netx.ApplyRuleset(context.WithoutCancel(ctx), ns, netx.Ruleset(nft))
	targets := append([]state.EgressProfile{{Name: state.Direct}}, usedProfiles(cfg)...)
	var checks []string
	for _, inst := range insts {
		if !e.App.InterfaceUp(inst.Name) {
			continue
		}
		tp, err := selftest.Start(ctx, e.App, inst, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")})
		if err != nil {
			return checks, fmt.Errorf("test peer: %w", err)
		}
		base := exceptAddr(nft, tp.Addr)
		for _, p := range targets {
			check := func() (string, error) { return e.postCheckRelay(ctx, inst, tp, p, base) }
			if !p.Relayed() && p.Name != state.Direct {
				check = func() (string, error) { return e.postCheck(ctx, inst, tp, p, ActiveHop(p, health[p.Name]).Dev, base) }
			}
			msg, err := check()
			if err != nil {
				tp.Close(ctx)
				return append(checks, fmt.Sprintf("FAIL %s: %v", p.Name, err)), fmt.Errorf("%s: %w", p.Name, err)
			}
			checks = append(checks, "ok   "+p.Name+": "+msg)
		}
		tp.Close(ctx)
	}
	return checks, nil
}

func (e *Engine) probeArgs() []string {
	args := []string{e.App.Paths.Binary, "egress", "probe", "--dns", e.dnsServer().String()}
	for _, t := range ProbeTCP {
		args = append(args, "--tcp", t)
	}
	return args
}

func (e *Engine) postCheck(ctx context.Context, inst state.Instance, tp *selftest.Peer, p state.EgressProfile, dev string, nft netx.Nft) (string, error) {
	ns := e.App.NS()
	if err := netx.ApplyRuleset(ctx, ns, netx.Ruleset(nft)); err != nil {
		return "", err
	}
	rule := netx.Rule{Family: 4, Pref: netx.PrefTest, From: netip.PrefixFrom(tp.Addr, 32), IIf: inst.Name, Table: p.Table}
	if err := netx.AddRule(ctx, ns, rule); err != nil {
		return "", err
	}
	defer netx.DelRule(context.WithoutCancel(ctx), ns, rule)
	target := netip.MustParseAddr("1.1.1.1")
	got, err := netx.RouteDev(ctx, ns, target, tp.Addr, inst.Name)
	if err != nil {
		return "", err
	}
	if got != dev {
		return "", fmt.Errorf("peer traffic leaves via %s instead of %s", got, dev)
	}
	_, tx0 := netx.LinkCounters(ctx, ns, dev)
	if err := tp.Exec(ctx, e.probeArgs()...); err != nil {
		return "", fmt.Errorf("test peer traffic through %s: %w", dev, err)
	}
	if !tp.Handshake(ctx) {
		return "", errors.New("test peer did not complete a handshake")
	}
	_, tx1 := netx.LinkCounters(ctx, ns, dev)
	if tx1 <= tx0 {
		return "", fmt.Errorf("no packets left through %s", dev)
	}
	return fmt.Sprintf("test peer TCP and DNS go out via %s (%d packets)", dev, tx1-tx0), nil
}

func (e *Engine) postCheckRelay(ctx context.Context, inst state.Instance, tp *selftest.Peer, p state.EgressProfile, nft netx.Nft) (string, error) {
	port := netx.RelayBase
	if p.Relayed() {
		port = RelayPort(p)
	}
	with := nft
	with.Tproxy = append([]netx.Tproxy{{Iface: inst.Name, Addrs: []netip.Prefix{netip.PrefixFrom(tp.Addr, 32)},
		Local: validPrefixes(inst.Subnet4.Masked(), inst.Subnet6.Masked()), Port: port}}, nft.Tproxy...)
	if !slices.Contains(with.Relay, port) {
		return "", fmt.Errorf("relay port %d is not configured", port)
	}
	if err := netx.ApplyRuleset(ctx, e.App.NS(), netx.Ruleset(with)); err != nil {
		return "", err
	}
	before := relayCount(port)
	if err := tp.Exec(ctx, e.probeArgs()...); err != nil {
		return "", fmt.Errorf("test peer traffic through the relay :%d: %w", port, err)
	}
	if !tp.Handshake(ctx) {
		return "", errors.New("test peer did not complete a handshake")
	}
	if !relayGrew(port, before) {
		return "", fmt.Errorf("the relay :%d saw no connections from the test peer", port)
	}
	if p.Relayed() {
		return fmt.Sprintf("test peer TCP and DNS go through socks5 %s", p.Socks.Address), nil
	}
	return "test peer TCP and DNS go out directly from the host", nil
}

func relayCount(port int) uint64 {
	st, err := relay.ReadStats(relay.StatsPath)
	if err != nil {
		return 0
	}
	return st[port].TCP + st[port].UDP
}

func relayGrew(port int, before uint64) bool {
	for range 30 {
		if relayCount(port) > before {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func ProbeCommand(ctx context.Context, tcp []string, dns string) error {
	var errs []error
	if len(tcp) > 0 {
		errs = append(errs, ProbeTCPConnect(ctx, 0, tcp))
	}
	if dns != "" {
		a, err := netip.ParseAddr(dns)
		if err != nil {
			return err
		}
		errs = append(errs, ProbeDNS(ctx, 0, a))
	}
	return errors.Join(errs...)
}

func exceptAddr(n netx.Nft, a netip.Addr) netx.Nft {
	ks := make([]netx.Killswitch, len(n.Killswitch))
	for i, k := range n.Killswitch {
		k.Except = append(append([]netip.Addr(nil), k.Except...), a)
		ks[i] = k
	}
	n.Killswitch = ks
	hj := make([]netx.Hijack, len(n.Hijack))
	for i, h := range n.Hijack {
		h.Except = append(append([]netip.Addr(nil), h.Except...), a)
		hj[i] = h
	}
	n.Hijack = hj
	tp := make([]netx.Tproxy, len(n.Tproxy))
	for i, t := range n.Tproxy {
		t.Except = append(append([]netip.Addr(nil), t.Except...), a)
		tp[i] = t
	}
	n.Tproxy = tp
	return n
}
