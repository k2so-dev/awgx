package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/gen"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/render"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
)

var peerName = regexp.MustCompile(`^[a-zA-Z0-9-]{1,32}$`)

var DefaultAllowedIPs = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}

var DefaultKeepalive = awgconf.Range{Lo: 20, Hi: 30}

type PeerOptions struct {
	Instance   string
	Name       string
	Egress     string
	Expires    time.Duration
	I1         *i1.Settings
	Note       string
	NetLabel   string
	NoPSK      bool
	AllowedIPs []netip.Prefix
	Keepalive  awgconf.Range
}

var (
	ErrPeerName   = errors.New("use latin letters, digits and -")
	ErrPeerExists = errors.New("already exists")
)

func (a *App) AddPeer(ctx context.Context, o PeerOptions) (state.Peer, validate.Issues, error) {
	if !peerName.MatchString(o.Name) {
		return state.Peer{}, nil, fmt.Errorf("invalid peer name %q: %w", o.Name, ErrPeerName)
	}
	s, err := a.Store()
	if err != nil {
		return state.Peer{}, nil, err
	}
	if _, err := s.Peer(o.Name); err == nil {
		return state.Peer{}, nil, fmt.Errorf("peer %s %w", o.Name, ErrPeerExists)
	}
	if sn, err := s.Applied(); err == nil {
		if _, ok := sn.Config.Peers[o.Name]; ok {
			return state.Peer{}, nil, fmt.Errorf("a removed peer %s still has a route in the applied egress: run awgx egress apply first, or pick another name", o.Name)
		}
	}
	if o.Egress == "inherit" {
		o.Egress = ""
	}
	if err := a.CheckEgress(o.Egress); err != nil {
		return state.Peer{}, nil, err
	}
	inst, err := a.ResolveInstance(o.Instance)
	if err != nil {
		return state.Peer{}, nil, err
	}
	peers, err := s.Peers(inst.Name)
	if err != nil {
		return state.Peer{}, nil, err
	}
	a4, a6, err := nextAddress(inst, peers)
	if err != nil {
		return state.Peer{}, nil, err
	}
	priv, err := awgconf.NewPrivateKey()
	if err != nil {
		return state.Peer{}, nil, err
	}
	p := state.Peer{
		Instance:   inst.Name,
		Name:       o.Name,
		PublicKey:  priv.Public(),
		PrivateKey: priv,
		Address4:   a4,
		Address6:   a6,
		AllowedIPs: o.AllowedIPs,
		Egress:     o.Egress,
		Keepalive:  o.Keepalive,
		NetLabel:   o.NetLabel,
		Note:       o.Note,
		Enabled:    true,
	}
	if len(p.AllowedIPs) == 0 {
		p.AllowedIPs = DefaultAllowedIPs
	}
	if p.Keepalive.IsZero() {
		p.Keepalive = DefaultKeepalive
	}
	if !o.NoPSK {
		if p.PresharedKey, err = awgconf.NewRandomKey(); err != nil {
			return p, nil, err
		}
	}
	if o.Expires > 0 {
		p.ExpiresAt = time.Now().Add(o.Expires).UTC().Truncate(time.Second)
	}
	var is validate.Issues
	if o.I1 != nil {
		if is, err = a.setPeerI1(inst, &p, *o.I1); err != nil {
			return p, is, err
		}
	}
	keys := map[string]awgconf.Key{"server": inst.PrivateKey, "new peer": priv, "header protection": inst.Obfuscation.HeaderProtectionKey}
	for _, other := range peers {
		keys["peer "+other.Name] = other.PrivateKey
	}
	if err := validate.UniqueKeys(keys).Err(); err != nil {
		return p, is, err
	}
	if err := s.SavePeer(&p); err != nil {
		return p, is, err
	}
	if err := a.syncPeer(ctx, inst, p); err != nil {
		if a.InterfaceUp(inst.Name) {
			a.Tool().RemovePeer(ctx, inst.Name, p.PublicKey)
		}
		return p, is, errors.Join(err, s.DeletePeer(p.Name))
	}
	a.Journal("peer.add", "%s %s", p.Name, p.Address4)
	return p, is, nil
}

func (a *App) setPeerI1(inst state.Instance, p *state.Peer, settings i1.Settings) (validate.Issues, error) {
	base := inst.I1
	if p.I1 != nil {
		base = *p.I1
	}
	settings = settings.Over(base).WithDefaults()
	cps, err := i1.Generate(settings)
	if err != nil {
		return nil, err
	}
	obf := inst.Obfuscation
	obf.I[0] = cps
	is := validate.Obfuscation(obf, gen.PathMTU)
	is = append(is, validate.ProfilePort(settings.Profile, inst.ListenPort)...)
	if err := is.Err(); err != nil {
		return is, err
	}
	p.I1 = &settings
	p.I1CPS = cps
	return is, nil
}

func (a *App) SetPeersI1(names []string, settings []*i1.Settings) ([]state.Peer, validate.Issues, error) {
	s, err := a.Store()
	if err != nil {
		return nil, nil, err
	}
	peers := make([]state.Peer, len(names))
	var all validate.Issues
	for i, name := range names {
		p, err := s.Peer(name)
		if err != nil {
			return nil, all, err
		}
		is, err := a.PeerI1(&p, settings[i])
		all = append(all, is...)
		if err != nil {
			return nil, all, fmt.Errorf("%s: %w", name, err)
		}
		peers[i] = p
	}
	for i := range peers {
		if err := s.SavePeer(&peers[i]); err != nil {
			return peers[:i], all, err
		}
		a.journalI1(peers[i])
	}
	return peers, all, nil
}

func (a *App) journalI1(p state.Peer) {
	prof := "inherit"
	if p.I1 != nil {
		prof = p.I1.Profile
	}
	a.Journal("peer.i1", "%s %s", p.Name, prof)
}

func (a *App) PeerI1(p *state.Peer, settings *i1.Settings) (validate.Issues, error) {
	if settings == nil {
		p.I1, p.I1CPS = nil, ""
		return nil, nil
	}
	s, err := a.Store()
	if err != nil {
		return nil, err
	}
	inst, err := s.Instance(p.Instance)
	if err != nil {
		return nil, err
	}
	return a.setPeerI1(inst, p, *settings)
}

func (a *App) RemovePeer(ctx context.Context, name string) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	p, err := s.Peer(name)
	if err != nil {
		return err
	}
	inst, err := s.Instance(p.Instance)
	if err != nil {
		return err
	}
	if err := s.DeletePeer(name); err != nil {
		return err
	}
	var rmErr error
	if a.InterfaceUp(inst.Name) {
		rmErr = a.Tool().RemovePeer(ctx, inst.Name, p.PublicKey)
	}
	a.Journal("peer.rm", "%s %s", p.Name, p.PublicKey)
	return errors.Join(a.WriteServerConf(inst), rmErr)
}

func (a *App) UpdatePeer(ctx context.Context, name string, edit func(p *state.Peer) error) (state.Peer, error) {
	s, err := a.Store()
	if err != nil {
		return state.Peer{}, err
	}
	p, err := s.Peer(name)
	if err != nil {
		return p, err
	}
	inst, err := s.Instance(p.Instance)
	if err != nil {
		return p, err
	}
	was, old := p.Enabled, p.I1
	if err := edit(&p); err != nil {
		return p, err
	}
	if p.Enabled && !was && p.Expired(time.Now()) {
		return p, fmt.Errorf("peer %s expired on %s: set a new expiry to enable it", p.Name, p.ExpiresAt.Format(time.DateOnly))
	}
	if err := s.SavePeer(&p); err != nil {
		return p, err
	}
	a.Journal("peer.update", "%s enabled=%v expires=%s", p.Name, p.Enabled, p.ExpiresAt.Format(time.RFC3339))
	if old != p.I1 && (old == nil || p.I1 == nil || *old != *p.I1) {
		a.journalI1(p)
	}
	return p, a.syncPeer(ctx, inst, p)
}

func (a *App) syncPeer(ctx context.Context, inst state.Instance, p state.Peer) error {
	if a.InterfaceUp(inst.Name) {
		var err error
		if p.Active(time.Now()) {
			err = a.Tool().SetPeer(ctx, inst.Name, p.PublicKey, p.PresharedKey, render.PeerAddresses(p))
		} else {
			err = a.Tool().RemovePeer(ctx, inst.Name, p.PublicKey)
		}
		if err != nil {
			return err
		}
	}
	return a.WriteServerConf(inst)
}

func nextAddress(inst state.Instance, peers []state.Peer) (netip.Addr, netip.Addr, error) {
	used := map[netip.Addr]bool{inst.Address4(): true, ReservedTestAddress(inst): true}
	for _, p := range peers {
		used[p.Address4] = true
	}
	a4 := inst.Address4()
	for {
		a4 = a4.Next()
		if !inst.Subnet4.Contains(a4) || isBroadcast(inst.Subnet4, a4) {
			return a4, netip.Addr{}, errors.New("no free addresses left in " + inst.Subnet4.String())
		}
		if !used[a4] {
			return a4, ipv6For(inst, a4), nil
		}
	}
}

func isBroadcast(p netip.Prefix, a netip.Addr) bool {
	return !p.Contains(a.Next())
}

func ReservedTestAddress(inst state.Instance) netip.Addr {
	a := inst.Subnet4.Masked().Addr().As4()
	host := uint32(1)<<(32-inst.Subnet4.Bits()) - 2
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3]) + host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func ValidPeerName(name string) bool {
	return peerName.MatchString(name)
}

func (a *App) CheckEgress(name string) error {
	if name == "" || name == state.Direct {
		return nil
	}
	s, err := a.Store()
	if err != nil {
		return err
	}
	draft, err := s.Draft()
	if err != nil {
		return err
	}
	if _, ok := draft.Profile(name); !ok {
		return fmt.Errorf("egress profile %s does not exist, see: awgx egress ls", name)
	}
	return nil
}
