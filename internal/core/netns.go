package core

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/render"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	DataplaneUnit  = netx.DataplaneUnit
	settingHostNet = "hostnet:"
	hostNetMax     = 256
)

var hostNetBase = netip.MustParseAddr("169.254.200.0")

func (a *App) NS() sysx.Runner {
	return netx.InNS(a.Run)
}

func HostPeer(name string) string {
	if len(name) <= 13 {
		return name + ".h"
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	return fmt.Sprintf("%s%04x.h", name[:9], h.Sum32()&0xffff)
}

type HostNet struct {
	Root  netip.Prefix
	Peer  netip.Prefix
	Table int
}

func hostNet(i int) HostNet {
	b := hostNetBase.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) + uint32(i)*4
	at := func(n uint32) netip.Addr {
		x := v + n
		return netip.AddrFrom4([4]byte{byte(x >> 24), byte(x >> 16), byte(x >> 8), byte(x)})
	}
	return HostNet{Root: netip.PrefixFrom(at(1), 30), Peer: netip.PrefixFrom(at(2), 30), Table: netx.HostTable + i}
}

func (a *App) HostTable(name string) int {
	s, err := a.Store()
	if err != nil {
		return 0
	}
	v, _ := s.Setting(settingHostNet + name)
	i, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return hostNet(i).Table
}

func (a *App) HostNet(name string) (HostNet, error) {
	s, err := a.Store()
	if err != nil {
		return HostNet{}, err
	}
	if v, _ := s.Setting(settingHostNet + name); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return hostNet(i), nil
		}
	}
	used := map[int]bool{}
	insts, err := s.Instances()
	if err != nil {
		return HostNet{}, err
	}
	for _, inst := range insts {
		if v, _ := s.Setting(settingHostNet + inst.Name); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				used[i] = true
			}
		}
	}
	for i := range hostNetMax {
		if !used[i] {
			if err := s.SetSetting(settingHostNet+name, strconv.Itoa(i)); err != nil {
				return HostNet{}, err
			}
			return hostNet(i), nil
		}
	}
	return HostNet{}, errors.New("no free host link address")
}

var (
	rootLL = netip.MustParsePrefix("fe80::1/64")
	peerLL = netip.MustParsePrefix("fe80::2/64")
)

func (a *App) createLink(ctx context.Context, name, tmp, conf string) error {
	if err := netx.EnsureNS(ctx, a.Run); err != nil {
		return err
	}
	a.Run.Run(ctx, "ip", "link", "del", tmp)
	if _, err := a.Run.Run(ctx, "ip", "link", "add", tmp, "type", "amneziawg"); err != nil {
		return err
	}
	steps := func() error {
		if err := (awg.Tool{Run: a.Run}).SetConf(ctx, tmp, conf); err != nil {
			return err
		}
		if _, err := a.Run.Run(ctx, "ip", "link", "set", tmp, "netns", netx.NS); err != nil {
			return err
		}
		if _, err := a.NS().Run(ctx, "ip", "link", "set", tmp, "name", name); err != nil {
			a.NS().Run(ctx, "ip", "link", "del", tmp)
			return err
		}
		return nil
	}
	if err := steps(); err != nil {
		a.Run.Run(ctx, "ip", "link", "del", tmp)
		return err
	}
	return nil
}

func (a *App) UpInstance(ctx context.Context, inst state.Instance) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	peers, err := s.Peers(inst.Name)
	if err != nil {
		return err
	}
	conf := render.Server(inst, peers, time.Now()).Strip().String()
	if !a.InterfaceUp(inst.Name) {
		if err := a.createLink(ctx, inst.Name, fmt.Sprintf("awgxc%d", inst.ListenPort), conf); err != nil {
			return fmt.Errorf("create %s: %w", inst.Name, err)
		}
	} else if err := a.Tool().SyncConf(ctx, inst.Name, conf); err != nil {
		return err
	}
	ns := a.NS()
	for _, c := range [][]string{
		{"ip", "link", "set", inst.Name, "mtu", strconv.Itoa(inst.MTU), "up"},
		{"ip", "-4", "route", "replace", inst.Subnet4.Masked().String(), "dev", inst.Name},
	} {
		if _, err := ns.Run(ctx, c[0], c[1:]...); err != nil {
			return err
		}
	}
	if inst.IPv6() {
		if _, err := ns.Run(ctx, "ip", "-6", "route", "replace", inst.Subnet6.Masked().String(), "dev", inst.Name); err != nil {
			return err
		}
	}
	return a.upHostLink(ctx, inst)
}

func (a *App) upHostLink(ctx context.Context, inst state.Instance) error {
	hn, err := a.HostNet(inst.Name)
	if err != nil {
		return err
	}
	peer := HostPeer(inst.Name)
	if netx.RootLinkExists(inst.Name) && netx.LinkKind(ctx, a.Run, inst.Name) != "veth" {
		return fmt.Errorf("interface %s already exists in the host namespace and is not an awgx host link; an old awg-quick or wg-quick setup? remove it with: awgx setup --remove-old", inst.Name)
	}
	if netx.RootLinkExists(inst.Name) && !netx.LinkExists(peer) {
		a.Run.Run(ctx, "ip", "link", "del", inst.Name)
	}
	if !netx.RootLinkExists(inst.Name) {
		a.NS().Run(ctx, "ip", "link", "del", peer)
		if _, err := a.Run.Run(ctx, "ip", "link", "add", inst.Name, "type", "veth", "peer", "name", peer, "netns", netx.NS); err != nil {
			return err
		}
	}
	mtu := strconv.Itoa(inst.MTU)
	root := [][]string{
		{"ip", "link", "set", inst.Name, "mtu", mtu, "up"},
		{"ip", "-4", "addr", "replace", hn.Root.String(), "dev", inst.Name},
		{"ip", "-4", "addr", "replace", netip.PrefixFrom(inst.Address4(), 32).String(), "dev", inst.Name},
		{"ip", "-4", "route", "replace", inst.Subnet4.Masked().String(), "via", hn.Peer.Addr().String(), "dev", inst.Name, "src", inst.Address4().String()},
	}
	ns := [][]string{
		{"ip", "link", "set", peer, "mtu", mtu, "up"},
		{"ip", "-4", "addr", "replace", hn.Peer.String(), "dev", peer},
		{"ip", "-4", "route", "replace", inst.Address4().String() + "/32", "via", hn.Root.Addr().String(), "dev", peer},
		{"ip", "-4", "route", "replace", "default", "via", hn.Root.Addr().String(), "dev", peer, "table", strconv.Itoa(hn.Table)},
	}
	if inst.IPv6() {
		root = append(root,
			[]string{"ip", "-6", "addr", "replace", rootLL.String(), "dev", inst.Name, "nodad"},
			[]string{"ip", "-6", "addr", "replace", netip.PrefixFrom(inst.Address6(), 128).String(), "dev", inst.Name, "nodad"},
			[]string{"ip", "-6", "route", "replace", inst.Subnet6.Masked().String(), "via", peerLL.Addr().String(), "dev", inst.Name, "src", inst.Address6().String()})
		ns = append(ns,
			[]string{"ip", "-6", "addr", "replace", peerLL.String(), "dev", peer, "nodad"},
			[]string{"ip", "-6", "route", "replace", inst.Address6().String() + "/128", "via", rootLL.Addr().String(), "dev", peer},
			[]string{"ip", "-6", "route", "replace", "default", "via", rootLL.Addr().String(), "dev", peer, "table", strconv.Itoa(hn.Table)})
	}
	for _, c := range root {
		if _, err := a.Run.Run(ctx, c[0], c[1:]...); err != nil {
			return err
		}
	}
	for _, c := range ns {
		if _, err := a.NS().Run(ctx, c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) DownInstance(ctx context.Context, name string) error {
	var errs []error
	if a.InterfaceUp(name) {
		_, err := a.NS().Run(ctx, "ip", "link", "del", name)
		errs = append(errs, err)
	}
	if netx.RootLinkExists(name) && netx.LinkKind(ctx, a.Run, name) == "veth" {
		_, err := a.Run.Run(ctx, "ip", "link", "del", name)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (a *App) RestartInstance(ctx context.Context, inst state.Instance) error {
	if a.InterfaceUp(inst.Name) {
		if _, err := a.NS().Run(ctx, "ip", "link", "del", inst.Name); err != nil {
			return err
		}
	}
	return a.UpInstance(ctx, inst)
}

func (a *App) UpUplink(ctx context.Context, u state.Uplink) error {
	c, err := awgconf.Parse(strings.NewReader(u.Conf))
	if err != nil {
		return err
	}
	if !a.InterfaceUp(u.Name) {
		if err := a.createLink(ctx, u.Name, "awgxu"+strings.TrimPrefix(u.Name, "awg"), c.Strip().String()); err != nil {
			return fmt.Errorf("create %s: %w", u.Name, err)
		}
	} else if err := a.Tool().SyncConf(ctx, u.Name, c.Strip().String()); err != nil {
		return err
	}
	ns := a.NS()
	for _, p := range c.Interface.Address {
		if _, err := ns.Run(ctx, "ip", "addr", "replace", p.String(), "dev", u.Name); err != nil {
			return err
		}
	}
	args := []string{"link", "set", u.Name}
	if c.Interface.MTU > 0 {
		args = append(args, "mtu", strconv.Itoa(c.Interface.MTU))
	}
	_, err = ns.Run(ctx, "ip", append(args, "up")...)
	return err
}

func (a *App) DownUplink(ctx context.Context, name string) error {
	if !a.InterfaceUp(name) {
		return nil
	}
	_, err := a.NS().Run(ctx, "ip", "link", "del", name)
	return err
}

func (a *App) RestartUplink(ctx context.Context, u state.Uplink) error {
	if err := a.DownUplink(ctx, u.Name); err != nil {
		return err
	}
	return a.UpUplink(ctx, u)
}

func (a *App) UpAll(ctx context.Context) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	if err := netx.EnsureNS(ctx, a.Run); err != nil {
		return err
	}
	var errs []error
	ups, err := s.Uplinks()
	errs = append(errs, err)
	for _, u := range ups {
		if err := a.UpUplink(ctx, u); err != nil {
			errs = append(errs, fmt.Errorf("uplink %s: %w", u.Name, err))
		}
	}
	insts, err := s.Instances()
	errs = append(errs, err)
	for _, inst := range insts {
		if err := a.UpInstance(ctx, inst); err != nil {
			errs = append(errs, fmt.Errorf("instance %s: %w", inst.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (a *App) DownLinks(ctx context.Context) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	var names []string
	ups, _ := s.Uplinks()
	for _, u := range ups {
		names = append(names, u.Name)
	}
	insts, _ := s.Instances()
	for _, i := range insts {
		names = append(names, i.Name)
	}
	var errs []error
	for _, n := range names {
		if a.InterfaceUp(n) {
			_, err := a.NS().Run(ctx, "ip", "link", "del", n)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func DataplaneUnitFile(binary string) string {
	return `[Unit]
Description=awgx data plane: network namespace, interfaces and egress relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=` + binary + ` dataplane
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=2
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`
}

func (a *App) InstallDataplane(ctx context.Context) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	path := "/etc/systemd/system/" + DataplaneUnit
	if err := writeFileAtomic(path, []byte(DataplaneUnitFile(a.Paths.Binary)), 0o644); err != nil {
		return err
	}
	s.AddArtifact(path, "file")
	if _, err := a.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := a.Run.Run(ctx, "systemctl", "enable", DataplaneUnit); err != nil {
		return err
	}
	if err := s.AddArtifact("unit:"+DataplaneUnit, "unit"); err != nil {
		return err
	}
	_, err = a.Run.Run(ctx, "systemctl", "restart", DataplaneUnit)
	return err
}
