package egress

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/state"
)

func (e *Engine) freeTable(ctx context.Context, draft state.EgressConfig) (int, error) {
	used := map[int]bool{}
	for _, p := range draft.Profiles {
		used[p.Table] = true
	}
	sns, err := e.Store.Snapshots(50)
	if err != nil {
		return 0, err
	}
	for _, sn := range sns {
		for _, p := range sn.Config.Profiles {
			used[p.Table] = true
		}
	}
	live, err := netx.ReadRoutes(ctx, e.App.NS(), netx.TableMin, netx.TableMax)
	if err != nil {
		return 0, err
	}
	for _, r := range live {
		used[r.Table] = true
	}
	for t := netx.TableMin + 1; t <= netx.TableMax; t++ {
		if !used[t] {
			return t, nil
		}
	}
	return 0, fmt.Errorf("no free routing table in %d-%d", netx.TableMin+1, netx.TableMax)
}

func (e *Engine) AddProfile(ctx context.Context, p state.EgressProfile) (state.EgressProfile, error) {
	if err := ValidName(p.Name); err != nil {
		return p, err
	}
	draft, err := e.Store.Draft()
	if err != nil {
		return p, err
	}
	if _, ok := draft.Profile(p.Name); ok {
		return p, fmt.Errorf("profile %s already exists", p.Name)
	}
	if p.Table, err = e.freeTable(ctx, draft); err != nil {
		return p, err
	}
	if p.Relayed() {
		if p.Socks == nil {
			return p, fmt.Errorf("profile %s: SOCKS5 server is required", p.Name)
		}
		if p.Socks.Address, err = ParseSocksAddress(p.Socks.Address); err != nil {
			return p, err
		}
	}
	next := draft
	next.Profiles = append(slices.Clone(draft.Profiles), p)
	if err := e.checkEdit(ctx, draft, next); err != nil {
		return p, err
	}
	return p, e.Store.SaveProfile(p)
}

func (e *Engine) checkEdit(ctx context.Context, before, after state.EgressConfig) error {
	in, err := e.Input(ctx, after)
	if err != nil {
		return err
	}
	issues, _ := Validate(in)
	if len(issues) == 0 {
		return nil
	}
	if in, err = e.Input(ctx, before); err != nil {
		return err
	}
	old, _ := Validate(in)
	for _, i := range issues {
		if !slices.Contains(old, i) {
			return errors.New(i)
		}
	}
	return nil
}

func Retype(q *state.EgressProfile, kind string) error {
	if kind == q.Kind {
		return nil
	}
	if q.Kind == "xui" {
		return fmt.Errorf("profile %s is managed by awgx xui", q.Name)
	}
	switch kind {
	case "iface":
		q.Socks = nil
	case "socks5":
		q.Hops = nil
	default:
		return fmt.Errorf("type must be iface or socks5")
	}
	q.Kind, q.DNSHijack = kind, kind == "socks5"
	return nil
}

func Retarget(q *state.EgressProfile, kind, fallback string, hops []string, socks state.Socks) error {
	old, oldHops := q.Socks, q.Hops
	if err := Retype(q, kind); err != nil {
		return err
	}
	q.Fallback, q.Hops, q.Socks = fallback, nil, nil
	if kind == "socks5" {
		if old != nil && socks.Password == "" && socks.User != "" {
			socks.Password = old.Password
		}
		socks.Address = strings.TrimSpace(socks.Address)
		q.Socks = &socks
		return nil
	}
	q.Hops = Hops(hops, oldHops)
	return nil
}

func Hops(devs []string, old []state.Hop) []state.Hop {
	via := map[string]netip.Addr{}
	for _, h := range old {
		via[h.Dev] = h.Via
	}
	var out []state.Hop
	for _, d := range devs {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, state.Hop{Dev: d, Via: via[d]})
		}
	}
	return out
}

func (e *Engine) UpdateProfile(ctx context.Context, name string, edit func(p *state.EgressProfile) error) (state.EgressProfile, error) {
	draft, err := e.Store.Draft()
	if err != nil {
		return state.EgressProfile{}, err
	}
	p, ok := draft.Profile(name)
	if !ok {
		return p, fmt.Errorf("profile %s: %w", name, state.ErrNotFound)
	}
	if p.Socks != nil {
		socks := *p.Socks
		p.Socks = &socks
	}
	p.Hops = slices.Clone(p.Hops)
	if err := edit(&p); err != nil {
		return p, err
	}
	next := draft
	next.Profiles = slices.Clone(draft.Profiles)
	for i := range next.Profiles {
		if next.Profiles[i].Name == name {
			next.Profiles[i] = p
		}
	}
	if err := e.checkEdit(ctx, draft, next); err != nil {
		return p, err
	}
	return p, e.Store.SaveProfile(p)
}

func (e *Engine) RemoveProfile(name string) error {
	draft, err := e.Store.Draft()
	if err != nil {
		return err
	}
	if _, ok := draft.Profile(name); !ok {
		return fmt.Errorf("profile %s: %w", name, state.ErrNotFound)
	}
	var users []string
	for inst, a := range draft.Default {
		if a.Profile == name {
			users = append(users, inst)
		}
	}
	for peer, a := range draft.Peers {
		if a.Profile == name {
			users = append(users, peer)
		}
	}
	for _, p := range draft.Profiles {
		if p.Fallback == name {
			users = append(users, "fallback of "+p.Name)
		}
	}
	if len(users) > 0 {
		slices.Sort(users)
		return fmt.Errorf("profile %s is used by %v, reassign them first", name, users)
	}
	return e.Store.DeleteProfile(name)
}

func (e *Engine) Assign(instance string, peers []string, a state.Assign) error {
	if a.Killswitch && (a.Profile == "" || a.Profile == state.Direct) {
		return fmt.Errorf("a killswitch needs an egress profile")
	}
	draft, err := e.Store.Draft()
	if err != nil {
		return err
	}
	if a.Profile != "" && a.Profile != state.Direct {
		if _, ok := draft.Profile(a.Profile); !ok {
			return fmt.Errorf("profile %s: %w", a.Profile, state.ErrNotFound)
		}
	}
	if len(peers) == 0 {
		if _, err := e.Store.Instance(instance); err != nil {
			return err
		}
		if a.Profile == "" {
			a.Profile = state.Direct
		}
		return e.Store.SetInstanceEgress(instance, a)
	}
	for _, name := range peers {
		if _, err := e.Store.Peer(name); err != nil {
			return err
		}
	}
	for _, name := range peers {
		if err := e.Store.SetPeerEgress(name, a); err != nil {
			return err
		}
	}
	return nil
}
