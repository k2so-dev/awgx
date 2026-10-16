package serve

import (
	"context"
	"errors"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/state"
)

const (
	freshHandshake = 180 * time.Second
	sessionTimeout = 300 * time.Second
	staleAfter     = 190 * time.Second
)

type counter struct {
	rx, tx uint64
	hs     time.Time
	stale  bool
}

type Collector struct {
	prev map[awgconf.Key]counter
}

func NewCollector() *Collector {
	return &Collector{prev: map[awgconf.Key]counter{}}
}

type Observation struct {
	Peer          state.Peer
	Rx, Tx        uint64
	LastHandshake time.Time
	Endpoint      string
}

type Result struct {
	Sample       state.Sample
	Record       bool
	OpenSession  bool
	TouchSession bool
	CloseSession bool
}

func (c *Collector) Observe(o Observation, now time.Time) Result {
	key := o.Peer.PublicKey
	prev, seen := c.prev[key]
	cur := counter{rx: o.Rx, tx: o.Tx, hs: o.LastHandshake}
	c.prev[key] = cur
	r := Result{Sample: state.Sample{PeerID: o.Peer.ID, At: now, LastHandshake: o.LastHandshake, Endpoint: o.Endpoint}}
	if !seen {
		return r
	}
	r.Sample.Rx = delta(o.Rx, prev.rx)
	r.Sample.Tx = delta(o.Tx, prev.tx)
	newHandshake := !o.LastHandshake.IsZero() && o.LastHandshake.After(prev.hs)
	if newHandshake {
		r.Sample.Handshakes = 1
	}
	age := now.Sub(o.LastHandshake)
	old := o.LastHandshake.IsZero() || age > staleAfter
	cur.stale = old && (prev.stale || r.Sample.Tx > 0)
	if cur.stale && !prev.stale {
		r.Sample.Failures = 1
	}
	c.prev[key] = cur
	r.Record = r.Sample.Rx > 0 || r.Sample.Tx > 0 || r.Sample.Handshakes > 0 || r.Sample.Failures > 0
	switch {
	case !o.LastHandshake.IsZero() && age < freshHandshake:
		r.TouchSession = true
		r.OpenSession = newHandshake
	case o.LastHandshake.IsZero() || age > sessionTimeout:
		r.CloseSession = true
	}
	return r
}

func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

func (c *Collector) Collect(ctx context.Context, app *core.App, now time.Time) error {
	insts, err := app.Instances()
	if err != nil {
		return err
	}
	var errs []error
	seen := map[awgconf.Key]bool{}
	for _, inst := range insts {
		if app.InterfaceUp(inst.Name) {
			errs = append(errs, c.collect(ctx, app, inst, now, seen))
		}
	}
	err = errors.Join(errs...)
	if err == nil {
		for k := range c.prev {
			if !seen[k] {
				delete(c.prev, k)
			}
		}
	}
	return err
}

func (c *Collector) collect(ctx context.Context, app *core.App, inst state.Instance, now time.Time, seen map[awgconf.Key]bool) error {
	store, _ := app.Store()
	peers, err := store.Peers(inst.Name)
	if err != nil {
		return err
	}
	dev, err := app.Tool().Show(ctx, inst.Name)
	if err != nil {
		return err
	}
	byKey := map[awgconf.Key]state.Peer{}
	for _, p := range peers {
		byKey[p.PublicKey] = p
	}
	var samples []state.Sample
	var errs []error
	for _, live := range dev.Peers {
		p, ok := byKey[live.PublicKey]
		if !ok {
			continue
		}
		seen[live.PublicKey] = true
		r := c.Observe(Observation{Peer: p, Rx: live.RxBytes, Tx: live.TxBytes, LastHandshake: live.LastHandshake, Endpoint: live.Endpoint}, now)
		if r.Record {
			samples = append(samples, r.Sample)
		}
		errs = append(errs, applySession(store, p, inst, r, now))
	}
	return errors.Join(append(errs, store.AddSamples(samples))...)
}

func applySession(store *state.Store, p state.Peer, inst state.Instance, r Result, now time.Time) error {
	switch {
	case r.CloseSession:
		return store.CloseSession(p.ID)
	case r.TouchSession:
		touched, err := store.TouchSession(p.ID, now)
		if err != nil || touched {
			return err
		}
		return store.OpenSession(p.ID, p.I1Profile(inst), p.NetLabel, r.Sample.LastHandshake)
	}
	return nil
}
