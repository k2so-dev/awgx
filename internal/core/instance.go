package core

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/gen"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/render"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
)

const (
	MaxLinkMTU    = 1500
	MinTunnelMTU  = 1280
	tunnelHeader  = 80
	tunnelHeader4 = 60
)

type GenOptions struct {
	Name     string
	Preset   string
	Port     int
	Endpoint string
	DNS      []netip.Addr
	I1       i1.Settings
	Toggles  gen.Toggles
	Custom   *awgconf.Obfuscation
	IPv6     *bool
}

func (a *App) NewInstance(ctx context.Context, o GenOptions) (state.Instance, validate.Issues, error) {
	if o.Name == "" {
		o.Name = DefaultInstance
	}
	obf, err := o.obfuscation()
	if err != nil {
		return state.Instance{}, nil, err
	}
	if o.Custom != nil {
		o.Preset = gen.Custom
	}
	priv, err := awgconf.NewPrivateKey()
	if err != nil {
		return state.Instance{}, nil, err
	}
	if o.Port == 0 {
		o.Port = gen.ListenPort()
	}
	if len(o.DNS) == 0 {
		o.DNS = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")}
	}
	if o.Endpoint != "" {
		if err := ValidEndpoint(o.Endpoint); err != nil {
			return state.Instance{}, nil, err
		}
	}
	if o.Endpoint == "" {
		up, err := netx.DefaultUplink(ctx, a.Run)
		if err != nil {
			return state.Instance{}, nil, fmt.Errorf("detect endpoint: %w, pass --endpoint", err)
		}
		o.Endpoint = up.Source.String()
	}
	subnet4, err := a.freeSubnet4(ctx)
	if err != nil {
		return state.Instance{}, nil, err
	}
	inst := state.Instance{
		Name:        o.Name,
		ListenPort:  o.Port,
		Endpoint:    o.Endpoint,
		MTU:         InnerMTU(a.LinkMTU(ctx), obf.S4, o.Endpoint),
		Subnet4:     subnet4,
		DNS:         o.DNS,
		Preset:      o.Preset,
		Obfuscation: obf,
		I1:          o.I1.WithDefaults(),
		PrivateKey:  priv,
	}
	if o.IPv6 == nil && netx.HasIPv6Route(ctx, a.Run) || o.IPv6 != nil && *o.IPv6 {
		inst.Subnet6 = randomULA()
	}
	is, err := a.applyI1(&inst)
	if err != nil {
		return inst, is, err
	}
	return inst, is, nil
}

func (a *App) SetIPv6(ctx context.Context, name string, on bool) (state.Instance, error) {
	s, err := a.Store()
	if err != nil {
		return state.Instance{}, err
	}
	inst, err := s.Instance(name)
	if err != nil || inst.IPv6() == on {
		return inst, err
	}
	inst.Subnet6 = netip.Prefix{}
	if on {
		inst.Subnet6 = randomULA()
	}
	peers, err := s.Peers(name)
	if err != nil {
		return inst, err
	}
	if err := s.SaveInstance(&inst); err != nil {
		return inst, err
	}
	for i := range peers {
		peers[i].Address6 = netip.Addr{}
		if on {
			peers[i].Address6 = ipv6For(inst, peers[i].Address4)
		}
		if err := s.SavePeer(&peers[i]); err != nil {
			return inst, err
		}
	}
	if err := a.WriteServerConf(inst); err != nil {
		return inst, err
	}
	a.Journal("instance.ipv6", "%s: %v", name, on)
	return inst, a.RestartInterface(ctx, name)
}

func ipv6For(inst state.Instance, a4 netip.Addr) netip.Addr {
	if !inst.IPv6() {
		return netip.Addr{}
	}
	h, base := a4.As4(), inst.Subnet4.Masked().Addr().As4()
	off := binary.BigEndian.Uint32(h[:]) - binary.BigEndian.Uint32(base[:])
	b := inst.Subnet6.Masked().Addr().As16()
	binary.BigEndian.PutUint32(b[12:], binary.BigEndian.Uint32(b[12:])+off)
	return netip.AddrFrom16(b)
}

func (o *GenOptions) obfuscation() (awgconf.Obfuscation, error) {
	if o.Custom != nil {
		return withKey(*o.Custom)
	}
	if o.Preset == "" {
		o.Preset = "balanced"
	}
	preset, err := gen.PresetByName(o.Preset)
	if err != nil {
		return awgconf.Obfuscation{}, err
	}
	return gen.Obfuscation(preset.With(o.Toggles))
}

func withKey(o awgconf.Obfuscation) (awgconf.Obfuscation, error) {
	if !o.HeaderProtectionKey.IsZero() {
		return o, nil
	}
	k, err := awgconf.NewRandomKey()
	o.HeaderProtectionKey = k
	return o, err
}

func (a *App) SetObfuscation(ctx context.Context, name string, obf awgconf.Obfuscation) (state.Instance, validate.Issues, error) {
	s, err := a.Store()
	if err != nil {
		return state.Instance{}, nil, err
	}
	cur, err := s.Instance(name)
	if err != nil {
		return cur, nil, err
	}
	if obf.HeaderProtectionKey.IsZero() {
		obf.HeaderProtectionKey = cur.Obfuscation.HeaderProtectionKey
	}
	obf.I[0] = cur.Obfuscation.I[0]
	next := cur
	next.Preset = gen.Custom
	next.Obfuscation = obf
	next.MTU = a.InstanceMTU(ctx, next)
	is, err := a.CheckInstance(next)
	if err != nil {
		return cur, is, err
	}
	if err := s.SaveInstance(&next); err != nil {
		return cur, is, err
	}
	if err := a.WriteServerConf(next); err != nil {
		return next, is, err
	}
	a.Journal("instance.params", "%s custom", name)
	return next, is, a.applyParams(ctx, next, !cur.Obfuscation.SameServerSide(obf) || cur.MTU != next.MTU)
}

func (a *App) LinkMTU(ctx context.Context) int {
	if up, err := netx.DefaultUplink(ctx, a.Run); err == nil && up.MTU > 0 {
		return min(up.MTU, MaxLinkMTU)
	}
	return MaxLinkMTU
}

func TunnelOverhead(s4 uint16, endpoint string) int {
	if a, err := netip.ParseAddr(endpoint); err == nil && a.Unmap().Is4() {
		return tunnelHeader4 + int(s4)
	}
	return tunnelHeader + int(s4)
}

func InnerMTU(link int, s4 uint16, endpoint string) int {
	return max(link-TunnelOverhead(s4, endpoint), MinTunnelMTU)
}

func (a *App) Regenerate(inst state.Instance, presetName string, t gen.Toggles) (state.Instance, validate.Issues, error) {
	if presetName == "" {
		presetName = inst.Preset
	}
	if presetName == gen.Custom {
		return inst, nil, fmt.Errorf("%s uses custom parameters: pass --preset to regenerate", inst.Name)
	}
	preset, err := gen.PresetByName(presetName)
	if err != nil {
		return inst, nil, err
	}
	if t.RandomTrailers == nil && presetName == inst.Preset {
		t.RandomTrailers = &inst.Obfuscation.RandomTrailers
	}
	if t.DisableCookies == nil && presetName == inst.Preset {
		t.DisableCookies = &inst.Obfuscation.DisableCookies
	}
	obf, err := gen.Obfuscation(preset.With(t))
	if err != nil {
		return inst, nil, err
	}
	next := inst
	next.Preset = presetName
	next.Obfuscation = obf
	next.MTU = a.InstanceMTU(context.Background(), next)
	is, err := a.applyI1(&next)
	return next, is, err
}

func (a *App) SaveRegenerated(ctx context.Context, next state.Instance) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	cur, err := s.Instance(next.Name)
	if err != nil {
		return err
	}
	serverSide := !cur.Obfuscation.SameServerSide(next.Obfuscation)
	if err := s.SaveInstance(&next); err != nil {
		return err
	}
	if err := a.WriteServerConf(next); err != nil {
		return err
	}
	a.Journal("gen", "preset=%s server_side=%v", next.Preset, serverSide)
	return a.applyParams(ctx, next, serverSide || cur.MTU != next.MTU)
}

func (a *App) applyParams(ctx context.Context, inst state.Instance, restart bool) error {
	if restart || !a.InterfaceUp(inst.Name) {
		return a.RestartInterface(ctx, inst.Name)
	}
	return a.UpInstance(ctx, inst)
}

func (a *App) applyI1(inst *state.Instance) (validate.Issues, error) {
	cps, err := i1.Generate(inst.I1)
	if err != nil {
		return nil, err
	}
	inst.Obfuscation.I[0] = cps
	inst.I1RotatedAt = time.Now().UTC().Truncate(time.Second)
	return a.CheckInstance(*inst)
}

func CheckCustom(o awgconf.Obfuscation) validate.Issues {
	o.I[0] = ""
	return validate.Obfuscation(o, gen.PathMTU)
}

func (a *App) CheckInstance(inst state.Instance) (validate.Issues, error) {
	is := validate.Obfuscation(inst.Obfuscation, gen.PathMTU)
	is = append(is, validate.ProfilePort(inst.I1.Profile, inst.ListenPort)...)
	return is, is.Err()
}

func (a *App) freeSubnet4(ctx context.Context) (netip.Prefix, error) {
	routes, err := a.usedSubnets(ctx)
	if err != nil {
		return netip.Prefix{}, err
	}
	for range 200 {
		b := make([]byte, 2)
		rand.Read(b)
		p := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, b[0], b[1], 0}), 24)
		if !slices.ContainsFunc(routes, p.Overlaps) {
			return p, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("no free 10.x.y.0/24 subnet")
}

func randomULA() netip.Prefix {
	var a [16]byte
	a[0] = 0xfd
	rand.Read(a[1:6])
	return netip.PrefixFrom(netip.AddrFrom16(a), 64)
}

func (a *App) WriteServerConf(inst state.Instance) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	peers, err := s.Peers(inst.Name)
	if err != nil {
		return err
	}
	conf := render.Server(inst, peers, time.Now())
	if err := os.MkdirAll(a.Paths.ConfDir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(a.Paths.Conf(inst.Name), []byte(conf.String()), 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (a *App) RestartInterface(ctx context.Context, name string) error {
	inst, err := a.Instance(name)
	if err != nil {
		return err
	}
	if err := a.RestartInstance(ctx, inst); err != nil {
		return err
	}
	a.LinksChanged(ctx)
	return nil
}
