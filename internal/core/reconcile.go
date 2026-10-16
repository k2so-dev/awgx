package core

import (
	"context"
	"time"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/render"
)

type Drift struct {
	Missing  int  `json:"missing"`
	Extra    int  `json:"extra"`
	WrongKey bool `json:"wrong_key"`
}

func (d Drift) Any() bool {
	return d.Missing+d.Extra > 0 || d.WrongKey
}

func (a *App) Reconcile(ctx context.Context, name string, apply bool) (Drift, error) {
	var d Drift
	s, err := a.Store()
	if err != nil {
		return d, err
	}
	inst, err := s.Instance(name)
	if err != nil {
		return d, err
	}
	if !a.InterfaceUp(name) {
		return d, nil
	}
	peers, err := s.Peers(name)
	if err != nil {
		return d, err
	}
	dev, err := a.Tool().Show(ctx, name)
	if err != nil {
		return d, err
	}
	now := time.Now()
	var want []awgconf.Key
	for _, p := range peers {
		if p.Active(now) {
			want = append(want, p.PublicKey)
		}
	}
	d.Missing, d.Extra = awg.PeerDiff(dev, want)
	d.WrongKey = dev.PublicKey != inst.PrivateKey.Public()
	if !d.Any() || !apply {
		return d, nil
	}
	if err := a.WriteServerConf(inst); err != nil {
		return d, err
	}
	conf := render.Server(inst, peers, now).Strip().String()
	if err := a.Tool().SyncConf(ctx, name, conf); err != nil {
		return d, err
	}
	a.Journal("reconcile", "%s missing=%d extra=%d wrong_key=%v", name, d.Missing, d.Extra, d.WrongKey)
	return d, nil
}

func (a *App) ExpirePeers(ctx context.Context, name string) ([]string, error) {
	s, err := a.Store()
	if err != nil {
		return nil, err
	}
	inst, err := s.Instance(name)
	if err != nil || !a.InterfaceUp(name) {
		return nil, err
	}
	peers, err := s.Peers(name)
	if err != nil {
		return nil, err
	}
	dev, err := a.Tool().Show(ctx, name)
	if err != nil {
		return nil, err
	}
	live := map[awgconf.Key]bool{}
	for _, p := range dev.Peers {
		live[p.PublicKey] = true
	}
	var expired []string
	now := time.Now()
	for _, p := range peers {
		if p.Expired(now) && live[p.PublicKey] {
			if err := a.Tool().RemovePeer(ctx, name, p.PublicKey); err != nil {
				return expired, err
			}
			expired = append(expired, p.Name)
		}
	}
	if len(expired) == 0 {
		return nil, nil
	}
	a.Journal("peer.expire", "%v", expired)
	return expired, a.WriteServerConf(inst)
}
