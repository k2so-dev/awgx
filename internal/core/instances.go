package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/k2so-dev/awgx/internal/validate"
)

var instanceName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,12}$`)

func (a *App) Instances() ([]state.Instance, error) {
	s, err := a.Store()
	if err != nil {
		return nil, err
	}
	return s.Instances()
}

func (a *App) ResolveInstance(name string) (state.Instance, error) {
	if name != "" {
		return a.Instance(name)
	}
	insts, err := a.Instances()
	if err != nil {
		return state.Instance{}, err
	}
	if len(insts) == 0 {
		return state.Instance{}, ErrNoInstances
	}
	for _, inst := range insts {
		if inst.Name == DefaultInstance {
			return inst, nil
		}
	}
	if len(insts) == 1 {
		return insts[0], nil
	}
	var names []string
	for _, inst := range insts {
		names = append(names, inst.Name)
	}
	return state.Instance{}, fmt.Errorf("several instances (%s), pass --instance", strings.Join(names, ", "))
}

func (a *App) Current() (state.Instance, error) {
	return a.ResolveInstance(a.Selected)
}

func ValidInstanceName(name string) error {
	if !instanceName.MatchString(name) {
		return fmt.Errorf("instance name %q: use 1-13 lowercase letters, digits, - or _, starting with a letter", name)
	}
	return nil
}

func (a *App) checkNewInstance(ctx context.Context, name string, port int) error {
	if err := ValidInstanceName(name); err != nil {
		return err
	}
	s, err := a.Store()
	if err != nil {
		return err
	}
	insts, err := s.Instances()
	if err != nil {
		return err
	}
	for _, inst := range insts {
		if inst.Name == name {
			return fmt.Errorf("instance %s already exists", name)
		}
		if port != 0 && inst.ListenPort == port {
			return fmt.Errorf("port %d/udp is used by instance %s", port, inst.Name)
		}
	}
	if _, err := s.Uplink(name); err == nil {
		return fmt.Errorf("%s is an uplink", name)
	}
	if a.InterfaceUp(name) || netx.RootLinkExists(name) {
		return fmt.Errorf("interface %s already exists", name)
	}
	if _, err := os.Stat(a.Paths.Conf(name)); err == nil {
		return fmt.Errorf("%s already exists", a.Paths.Conf(name))
	}
	if port != 0 {
		out, _ := a.Run.Run(ctx, "ss", "-Hulnp", fmt.Sprintf("sport = :%d", port))
		if strings.TrimSpace(string(out)) != "" {
			return fmt.Errorf("port %d/udp is already in use", port)
		}
	}
	return nil
}

func (a *App) AddInstance(ctx context.Context, o GenOptions) (state.Instance, validate.Issues, error) {
	if o.Name == "" {
		return state.Instance{}, nil, errors.New("instance name is required")
	}
	if err := a.checkNewInstance(ctx, o.Name, o.Port); err != nil {
		return state.Instance{}, nil, err
	}
	s, err := a.Store()
	if err != nil {
		return state.Instance{}, nil, err
	}
	if o.Endpoint == "" {
		if first, err := a.ResolveInstance(""); err == nil {
			o.Endpoint = first.Endpoint
		}
	}
	for range 20 {
		inst, is, err := a.NewInstance(ctx, o)
		if err != nil {
			return inst, is, err
		}
		if o.Port == 0 {
			if err := a.checkNewInstance(ctx, o.Name, inst.ListenPort); err != nil {
				continue
			}
		}
		if err := s.SaveInstance(&inst); err != nil {
			return inst, is, err
		}
		if err := a.startInstance(ctx, inst); err != nil {
			a.dropInstance(ctx, inst.Name)
			return inst, is, err
		}
		a.Journal("instance.add", "%s preset=%s port=%d", inst.Name, inst.Preset, inst.ListenPort)
		return inst, is, nil
	}
	return state.Instance{}, nil, errors.New("no free UDP port found, pass --port")
}

func (a *App) startInstance(ctx context.Context, inst state.Instance) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	if err := a.WriteServerConf(inst); err != nil {
		return err
	}
	s.AddArtifact(a.Paths.Conf(inst.Name), "file")
	if err := a.OpenFirewall(ctx, inst); err != nil {
		return err
	}
	a.Logf("starting %s on %d/udp", inst.Name, inst.ListenPort)
	if err := a.UpInstance(ctx, inst); err != nil {
		return err
	}
	a.LinksChanged(ctx)
	return nil
}

var AfterLinksUp func(context.Context, *App)

func (a *App) LinksChanged(ctx context.Context) {
	if AfterLinksUp != nil {
		AfterLinksUp(ctx, a)
	}
}

func (a *App) RemoveInstance(ctx context.Context, name string) error {
	insts, err := a.Instances()
	if err != nil {
		return err
	}
	found := false
	for _, inst := range insts {
		found = found || inst.Name == name
	}
	if !found {
		return fmt.Errorf("instance %s: %w", name, state.ErrNotFound)
	}
	if err := a.dropInstance(ctx, name); err != nil {
		return err
	}
	a.Journal("instance.rm", "%s", name)
	a.LinksChanged(ctx)
	return nil
}

func (a *App) dropInstance(ctx context.Context, name string) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	a.DownInstance(ctx, name)
	if err := os.Remove(a.Paths.Conf(name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.DeleteArtifact(a.Paths.Conf(name))
	s.SetSetting(settingMTU+name, "")
	s.SetSetting(settingRate+name, "")
	s.SetSetting(settingHostNet+name, "")
	a.closeFirewall(ctx, s, name)
	return s.DeleteInstance(name)
}

func (a *App) OpenFirewall(ctx context.Context, inst state.Instance) error {
	fw := netx.Firewall(ctx, a.Run)
	if fw == "" {
		return nil
	}
	s, err := a.Store()
	if err != nil {
		return err
	}
	a.Logf("opening %d/udp for %s in %s", inst.ListenPort, inst.Name, fw)
	if err := netx.OpenFirewall(ctx, a.Run, fw, inst.Name, inst.ListenPort); err != nil {
		return fmt.Errorf("%s: %w", fw, err)
	}
	return s.AddArtifact(netx.FirewallArtifact(fw, inst.Name, inst.ListenPort), "firewall")
}

func (a *App) closeFirewall(ctx context.Context, s *state.Store, name string) {
	artifacts, _ := s.Artifacts()
	for _, art := range artifacts {
		if fw, iface, port, ok := netx.ParseFirewallArtifact(art.Path); ok && iface == name {
			netx.CloseFirewall(ctx, a.Run, fw, iface, port)
			s.DeleteArtifact(art.Path)
		}
	}
}

func (a *App) usedSubnets(ctx context.Context) ([]netip.Prefix, error) {
	routes, err := netx.Routes(ctx, a.Run)
	if err != nil {
		return nil, err
	}
	if insts, err := a.Instances(); err == nil {
		for _, inst := range insts {
			routes = append(routes, inst.Subnet4)
		}
	}
	if s, err := a.Store(); err == nil {
		ups, _ := s.Uplinks()
		for _, u := range ups {
			if c, err := awgconf.Parse(strings.NewReader(u.Conf)); err == nil {
				routes = append(routes, c.Interface.Address...)
			}
		}
	}
	return routes, nil
}

func (a *App) InstanceNames() []string {
	insts, _ := a.Instances()
	var out []string
	for _, inst := range insts {
		out = append(out, inst.Name)
	}
	return out
}

func (a *App) RestartInstances(ctx context.Context, before []string) error {
	after := a.InstanceNames()
	for _, name := range before {
		if !slices.Contains(after, name) {
			a.DownInstance(ctx, name)
			os.Remove(a.Paths.Conf(name))
		}
	}
	insts, err := a.Instances()
	if err != nil {
		return err
	}
	var errs []error
	for _, inst := range insts {
		if err := a.RestartInstance(ctx, inst); err != nil {
			errs = append(errs, err)
		}
	}
	a.LinksChanged(ctx)
	return errors.Join(errs...)
}

var hostName = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

func ValidEndpoint(s string) error {
	if a, err := netip.ParseAddr(s); err == nil {
		if a.IsUnspecified() || a.IsLoopback() || a.Zone() != "" {
			return fmt.Errorf("endpoint %s is not reachable by clients", s)
		}
		return nil
	}
	if hostName.MatchString(s) {
		return nil
	}
	return fmt.Errorf("endpoint %q must be an IP address or a domain name", s)
}

func (a *App) SetEndpoint(name, endpoint string) (state.Instance, error) {
	if err := ValidEndpoint(endpoint); err != nil {
		return state.Instance{}, err
	}
	s, err := a.Store()
	if err != nil {
		return state.Instance{}, err
	}
	inst, err := s.Instance(name)
	if err != nil {
		return inst, err
	}
	old, oldMTU := inst.Endpoint, inst.MTU
	inst.Endpoint = endpoint
	inst.MTU = a.InstanceMTU(context.Background(), inst)
	if err := s.SaveInstance(&inst); err != nil {
		return inst, err
	}
	a.Journal("instance.endpoint", "%s: %s -> %s", name, old, endpoint)
	return inst, a.applyMTU(inst, oldMTU)
}

const settingMTU = "instance_mtu:"

func (a *App) FixedMTU(name string) int {
	s, err := a.Store()
	if err != nil {
		return 0
	}
	v, _ := s.Setting(settingMTU + name)
	n, _ := strconv.Atoi(v)
	return n
}

func (a *App) AutoMTU(ctx context.Context, inst state.Instance) int {
	mtu := InnerMTU(a.LinkMTU(ctx), inst.Obfuscation.S4, inst.Endpoint)
	if hop := a.egressMTU(inst); hop > 0 {
		mtu = max(min(mtu, hop), MinTunnelMTU)
	}
	return mtu
}

func (a *App) egressMTU(inst state.Instance) int {
	s, err := a.Store()
	if err != nil {
		return 0
	}
	sn, err := s.Applied()
	if err != nil {
		return 0
	}
	cfg := sn.Config
	names := []string{cfg.Default[inst.Name].Profile}
	peers, _ := s.Peers(inst.Name)
	for _, p := range peers {
		names = append(names, cfg.Peers[p.Name].Profile)
	}
	mtu := 0
	seen := map[string]bool{}
	for _, name := range names {
		for !seen[name] {
			seen[name] = true
			p, ok := cfg.Profile(name)
			if !ok {
				break
			}
			for _, h := range p.Hops {
				if n := netx.LinkMTU(context.Background(), a.NS(), h.Dev); n > 0 && (mtu == 0 || n < mtu) {
					mtu = n
				}
			}
			name = p.Fallback
		}
	}
	return mtu
}

func (a *App) InstanceMTU(ctx context.Context, inst state.Instance) int {
	if n := a.FixedMTU(inst.Name); n > 0 {
		return n
	}
	return a.AutoMTU(ctx, inst)
}

func (a *App) SetMTU(ctx context.Context, name string, mtu int) (state.Instance, error) {
	if mtu != 0 && (mtu < MinTunnelMTU || mtu > MaxLinkMTU) {
		return state.Instance{}, fmt.Errorf("MTU must be %d-%d, or 0 for automatic", MinTunnelMTU, MaxLinkMTU)
	}
	s, err := a.Store()
	if err != nil {
		return state.Instance{}, err
	}
	inst, err := s.Instance(name)
	if err != nil {
		return inst, err
	}
	value := ""
	if mtu > 0 {
		value = strconv.Itoa(mtu)
	}
	if err := s.SetSetting(settingMTU+name, value); err != nil {
		return inst, err
	}
	oldMTU := inst.MTU
	inst.MTU = a.InstanceMTU(ctx, inst)
	if err := s.SaveInstance(&inst); err != nil {
		return inst, err
	}
	a.Journal("instance.mtu", "%s: %d -> %d", name, oldMTU, inst.MTU)
	return inst, a.applyMTU(inst, oldMTU)
}

func (a *App) applyMTU(inst state.Instance, old int) error {
	if inst.MTU == old {
		return nil
	}
	if err := a.WriteServerConf(inst); err != nil {
		return err
	}
	if a.InterfaceUp(inst.Name) {
		ctx := context.Background()
		if _, err := a.NS().Run(ctx, "ip", "link", "set", "dev", inst.Name, "mtu", strconv.Itoa(inst.MTU)); err != nil {
			return err
		}
		for _, c := range []struct {
			run  sysx.Runner
			name string
		}{{a.NS(), HostPeer(inst.Name)}, {a.Run, inst.Name}} {
			c.run.Run(ctx, "ip", "link", "set", "dev", c.name, "mtu", strconv.Itoa(inst.MTU))
		}
	}
	return nil
}

const settingRate = netx.RateSetting

func (a *App) Rate(name string) int {
	s, err := a.Store()
	if err != nil {
		return 0
	}
	v, _ := s.Setting(settingRate + name)
	n, _ := strconv.Atoi(v)
	return n
}

func ValidRate(mbit int) error {
	if mbit < 0 || mbit > netx.MaxRate {
		return fmt.Errorf("rate must be 1-%d Mbit/s, or 0 for no limit", netx.MaxRate)
	}
	return nil
}

func (a *App) SetRate(ctx context.Context, name string, mbit int) error {
	if err := ValidRate(mbit); err != nil {
		return err
	}
	s, err := a.Store()
	if err != nil {
		return err
	}
	if _, err := s.Instance(name); err != nil {
		if _, uerr := s.Uplink(name); uerr != nil {
			return err
		}
	}
	value := ""
	if mbit > 0 {
		value = strconv.Itoa(mbit)
	}
	old := a.Rate(name)
	if err := s.SetSetting(settingRate+name, value); err != nil {
		return err
	}
	a.Journal("rate", "%s: %d -> %d Mbit/s", name, old, mbit)
	return a.ApplyRate(ctx, name)
}

func (a *App) ApplyRate(ctx context.Context, name string) error {
	if !a.InterfaceUp(name) {
		return nil
	}
	return netx.Shape(ctx, a.NS(), name, a.Rate(name))
}

func (a *App) ApplyRates(ctx context.Context) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	var names []string
	insts, _ := s.Instances()
	for _, i := range insts {
		names = append(names, i.Name)
	}
	ups, _ := s.Uplinks()
	for _, u := range ups {
		names = append(names, u.Name)
	}
	var errs []error
	for _, n := range names {
		if err := a.ApplyRate(ctx, n); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *App) ClearRate(name string) {
	if s, err := a.Store(); err == nil {
		s.SetSetting(settingRate+name, "")
	}
}
