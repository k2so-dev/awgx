package selftest

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	testNS    = "awgx-selftest"
	testVethH = "awgxst0"
	testVethN = "awgxst1"
	testIface = "awgxst"
)

var (
	testHostAddr = netip.MustParsePrefix("169.254.231.1/30")
	testNSAddr   = netip.MustParsePrefix("169.254.231.2/30")
)

type Peer struct {
	app    *core.App
	inst   state.Instance
	Addr   netip.Addr
	public awgconf.Key
}

func Run(ctx context.Context, app *core.App, inst state.Instance) error {
	unlock, err := app.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return Check(ctx, app, inst)
}

func Check(ctx context.Context, app *core.App, inst state.Instance) error {
	p, err := Start(ctx, app, inst, []netip.Prefix{inst.Subnet4})
	if err != nil {
		return err
	}
	defer p.Close(ctx)
	if err := p.Exec(ctx, "ping", "-c", "3", "-i", "0.3", "-W", "3", inst.Address4().String()); err != nil {
		return fmt.Errorf("test peer could not reach %s through the tunnel: %w", inst.Address4(), err)
	}
	if !p.Handshake(ctx) {
		return fmt.Errorf("no handshake recorded for the test peer")
	}
	return nil
}

func Start(ctx context.Context, app *core.App, inst state.Instance, allowed []netip.Prefix) (_ *Peer, err error) {
	run := func(name string, args ...string) error {
		_, e := app.Run.Run(ctx, name, args...)
		return e
	}
	priv, err := awgconf.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	psk, err := awgconf.NewRandomKey()
	if err != nil {
		return nil, err
	}
	p := &Peer{app: app, inst: inst, Addr: core.ReservedTestAddress(inst), public: priv.Public()}
	p.Close(ctx)
	defer func() {
		if err != nil {
			p.Close(ctx)
		}
	}()
	tool := app.Tool()
	if err := tool.SetPeer(ctx, inst.Name, priv.Public(), psk, []netip.Prefix{netip.PrefixFrom(p.Addr, 32)}); err != nil {
		return nil, fmt.Errorf("add test peer: %w", err)
	}
	steps := [][]string{
		{"ip", "netns", "add", testNS},
		{"ip", "link", "add", testVethH, "type", "veth", "peer", "name", testVethN, "netns", testNS},
		{"ip", "addr", "add", testHostAddr.String(), "dev", testVethH},
		{"ip", "link", "set", testVethH, "up"},
		{"ip", "-n", testNS, "addr", "add", testNSAddr.String(), "dev", testVethN},
		{"ip", "-n", testNS, "link", "set", testVethN, "up"},
		{"ip", "-n", testNS, "link", "set", "lo", "up"},
	}
	for _, s := range steps {
		if err := run(s[0], s[1:]...); err != nil {
			return nil, err
		}
	}
	if err := run("ip", "-n", testNS, "link", "add", testIface, "type", "amneziawg"); err != nil {
		return nil, fmt.Errorf("create test interface: %w", err)
	}
	client := awgconf.Config{
		Interface: awgconf.Interface{PrivateKey: priv, Obfuscation: inst.Obfuscation},
		Peers: []awgconf.Peer{{
			PublicKey:           inst.PrivateKey.Public(),
			PresharedKey:        psk,
			Endpoint:            net.JoinHostPort(testHostAddr.Addr().String(), strconv.Itoa(inst.ListenPort)),
			AllowedIPs:          allowed,
			PersistentKeepalive: awgconf.Single(5),
		}},
	}
	if err := (awg.Tool{Run: sysx.InNS{Runner: app.Run, Name: testNS}}).SetConf(ctx, testIface, client.String()); err != nil {
		return nil, err
	}
	for _, s := range [][]string{
		{"ip", "addr", "add", netip.PrefixFrom(p.Addr, 32).String(), "dev", testIface},
		{"ip", "link", "set", testIface, "mtu", strconv.Itoa(inst.MTU), "up"},
	} {
		if err := p.Exec(ctx, s...); err != nil {
			return nil, err
		}
	}
	for _, a := range allowed {
		if err := p.Exec(ctx, "ip", "route", "add", a.String(), "dev", testIface); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *Peer) Exec(ctx context.Context, args ...string) error {
	_, err := p.app.Run.Run(ctx, "ip", append([]string{"netns", "exec", testNS}, args...)...)
	return err
}

func (p *Peer) Handshake(ctx context.Context) bool {
	dev, err := p.app.Tool().Show(ctx, p.inst.Name)
	if err != nil {
		return false
	}
	for _, peer := range dev.Peers {
		if peer.PublicKey == p.public && !peer.LastHandshake.IsZero() {
			return true
		}
	}
	return false
}

func (p *Peer) Close(ctx context.Context) {
	killNS(ctx, p.app)
	p.app.Run.Run(ctx, "ip", "netns", "del", testNS)
	p.app.Run.Run(ctx, "ip", "link", "del", testVethH)
	p.app.Tool().RemovePeer(ctx, p.inst.Name, p.public)
}

func Cleanup(ctx context.Context, app *core.App, inst state.Instance) {
	killNS(ctx, app)
	app.Run.Run(ctx, "ip", "netns", "del", testNS)
	app.Run.Run(ctx, "ip", "link", "del", testVethH)
	dev, err := app.Tool().Show(ctx, inst.Name)
	if err != nil {
		return
	}
	addr := netip.PrefixFrom(core.ReservedTestAddress(inst), 32).String()
	for _, p := range dev.Peers {
		if len(p.AllowedIPs) == 1 && p.AllowedIPs[0] == addr {
			app.Tool().RemovePeer(ctx, inst.Name, p.PublicKey)
		}
	}
}

func (p *Peer) Output(ctx context.Context, args ...string) ([]byte, error) {
	return p.app.Run.Run(ctx, "ip", append([]string{"netns", "exec", testNS}, args...)...)
}

func killNS(ctx context.Context, app *core.App) {
	out, err := app.Run.Run(ctx, "ip", "netns", "pids", testNS)
	if err != nil {
		return
	}
	for _, pid := range strings.Fields(string(out)) {
		app.Run.Run(ctx, "kill", pid)
	}
}
