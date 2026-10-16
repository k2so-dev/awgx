package core

import (
	"time"

	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
)

func (a *App) SetInstanceI1(name string, s i1.Settings) (state.Instance, validate.Issues, error) {
	store, err := a.Store()
	if err != nil {
		return state.Instance{}, nil, err
	}
	inst, err := store.Instance(name)
	if err != nil {
		return inst, nil, err
	}
	inst.I1 = s.Over(inst.I1).WithDefaults()
	is, err := a.applyI1(&inst)
	if err != nil {
		return inst, is, err
	}
	if err := store.SaveInstance(&inst); err != nil {
		return inst, is, err
	}
	a.Journal("i1.set", "%s %s", inst.Name, inst.I1.Profile)
	return inst, is, nil
}

func (a *App) RotateI1(name string) ([]string, error) {
	store, err := a.Store()
	if err != nil {
		return nil, err
	}
	inst, err := store.Instance(name)
	if err != nil {
		return nil, err
	}
	peers, err := store.Peers(name)
	if err != nil {
		return nil, err
	}
	var affected []string
	if i1.Static(inst.I1.Profile) {
		if _, err := a.applyI1(&inst); err != nil {
			return nil, err
		}
		for _, p := range peers {
			if p.I1 == nil {
				affected = append(affected, p.Name)
			}
		}
	}
	for _, p := range peers {
		if p.I1 == nil || !i1.Static(p.I1.Profile) {
			continue
		}
		if _, err := a.setPeerI1(inst, &p, *p.I1); err != nil {
			return affected, err
		}
		if err := store.SavePeer(&p); err != nil {
			return affected, err
		}
		affected = append(affected, p.Name)
	}
	inst.I1RotatedAt = time.Now().UTC().Truncate(time.Second)
	if err := store.SaveInstance(&inst); err != nil {
		return affected, err
	}
	a.Journal("i1.rotate", "%s peers=%v", name, affected)
	return affected, nil
}
