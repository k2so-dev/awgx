package netx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/k2so-dev/awgx/internal/sysx"
)

type Uplink struct {
	Dev     string     `json:"dev"`
	Gateway string     `json:"gateway"`
	Source  netip.Addr `json:"source"`
	MTU     int        `json:"mtu"`
}

type route struct {
	Dst     string `json:"dst"`
	Dev     string `json:"dev"`
	Gateway string `json:"gateway"`
	PrefSrc string `json:"prefsrc"`
}

type link struct {
	IfName   string `json:"ifname"`
	MTU      int    `json:"mtu"`
	LinkInfo struct {
		InfoKind string `json:"info_kind"`
	} `json:"linkinfo"`
}

func DefaultUplink(ctx context.Context, run sysx.Runner) (Uplink, error) {
	var u Uplink
	out, err := run.Run(ctx, "ip", "-j", "route", "get", "1.1.1.1")
	if err != nil {
		return u, err
	}
	var routes []route
	if err := json.Unmarshal(out, &routes); err != nil || len(routes) == 0 {
		return u, fmt.Errorf("no default route")
	}
	u.Dev = routes[0].Dev
	u.Gateway = routes[0].Gateway
	u.Source, _ = netip.ParseAddr(routes[0].PrefSrc)
	links, err := Links(ctx, run)
	if err != nil {
		return u, err
	}
	for _, l := range links {
		if l.Name == u.Dev {
			u.MTU = l.MTU
		}
	}
	return u, nil
}

type Link struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	MTU  int    `json:"mtu"`
}

func Links(ctx context.Context, run sysx.Runner) ([]Link, error) {
	out, err := run.Run(ctx, "ip", "-j", "-d", "link", "show")
	if err != nil {
		return nil, err
	}
	var raw []link
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	links := make([]Link, len(raw))
	for i, l := range raw {
		links[i] = Link{Name: l.IfName, Kind: l.LinkInfo.InfoKind, MTU: l.MTU}
	}
	return links, nil
}

func Routes(ctx context.Context, run sysx.Runner) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, family := range []string{"-4", "-6"} {
		b, err := run.Run(ctx, "ip", "-j", family, "route", "show", "table", "all")
		if err != nil {
			return nil, err
		}
		var routes []route
		if err := json.Unmarshal(b, &routes); err != nil {
			return nil, err
		}
		for _, r := range routes {
			if p, err := netip.ParsePrefix(r.Dst); err == nil {
				out = append(out, p)
			} else if a, err := netip.ParseAddr(r.Dst); err == nil {
				out = append(out, netip.PrefixFrom(a, a.BitLen()))
			}
		}
	}
	return out, nil
}

type LinkState struct {
	Name  string
	Up    bool
	Has6  bool
	Addrs []netip.Addr
	MTU   int
}

type addrLink struct {
	IfName    string   `json:"ifname"`
	Flags     []string `json:"flags"`
	OperState string   `json:"operstate"`
	MTU       int      `json:"mtu"`
	AddrInfo  []struct {
		Family string `json:"family"`
		Local  string `json:"local"`
		Scope  string `json:"scope"`
	} `json:"addr_info"`
}

func LinkStates(ctx context.Context, run sysx.Runner) (map[string]LinkState, error) {
	out, err := run.Run(ctx, "ip", "-j", "addr", "show")
	if err != nil {
		return nil, err
	}
	var raw []addrLink
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	m := map[string]LinkState{}
	for _, l := range raw {
		s := LinkState{Name: l.IfName, MTU: l.MTU}
		s.Up = slices.Contains(l.Flags, "UP") && l.OperState != "DOWN" && l.OperState != "LOWERLAYERDOWN"
		for _, a := range l.AddrInfo {
			addr, err := netip.ParseAddr(a.Local)
			if err != nil {
				continue
			}
			s.Addrs = append(s.Addrs, addr)
			if a.Family == "inet6" && a.Scope == "global" {
				s.Has6 = true
			}
		}
		m[l.IfName] = s
	}
	return m, nil
}

func RouteDev(ctx context.Context, run sysx.Runner, dst, from netip.Addr, iif string) (string, error) {
	out, err := run.Run(ctx, "ip", "-j", "route", "get", dst.String(), "from", from.String(), "iif", iif)
	if err != nil {
		return "", err
	}
	var routes []route
	if err := json.Unmarshal(out, &routes); err != nil || len(routes) == 0 {
		return "", fmt.Errorf("no route to %s from %s", dst, from)
	}
	return routes[0].Dev, nil
}

func LinkCounters(ctx context.Context, run sysx.Runner, name string) (rx, tx uint64) {
	l, err := readLink(ctx, run, name)
	if err != nil {
		return 0, 0
	}
	return l.Stats.Rx.Packets, l.Stats.Tx.Packets
}

func HasIPv6Route(ctx context.Context, run sysx.Runner) bool {
	out, err := run.Run(ctx, "ip", "-6", "route", "show", "default")
	return err == nil && strings.TrimSpace(string(out)) != ""
}
