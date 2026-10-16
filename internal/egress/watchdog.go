package egress

import (
	"context"
	"fmt"
	"maps"

	"github.com/k2so-dev/awgx/internal/state"
)

const (
	downAfter = 3
	upAfter   = 2
)

type Watchdog struct {
	Extra  func(ctx context.Context, p state.EgressProfile) error
	fails  map[string]int
	oks    map[string]int
	down   map[string]bool
	seeded bool
}

func (w *Watchdog) seed(health map[string]Health, profiles []state.EgressProfile) {
	w.seeded = true
	for _, p := range profiles {
		h, ok := health[p.Name]
		if !ok {
			continue
		}
		hops := max(1, len(p.Hops))
		for i := range hops {
			if h.Down || i < h.Active {
				key := fmt.Sprintf("%s/%d", p.Name, i)
				w.down[key] = true
				w.fails[key] = downAfter
			}
		}
	}
}

func NewWatchdog() *Watchdog {
	return &Watchdog{fails: map[string]int{}, oks: map[string]int{}, down: map[string]bool{}}
}

func (w *Watchdog) observe(key string, err error) bool {
	if err != nil {
		w.oks[key] = 0
		w.fails[key]++
		if w.fails[key] >= downAfter {
			w.down[key] = true
		}
	} else {
		w.fails[key] = 0
		w.oks[key]++
		if w.oks[key] >= upAfter {
			w.down[key] = false
		}
	}
	return !w.down[key]
}

func (e *Engine) checkProfile(ctx context.Context, w *Watchdog, p state.EgressProfile) []error {
	if p.Relayed() {
		err := e.probeRelay(ctx, p)
		if err == nil && w.Extra != nil {
			err = w.Extra(ctx, p)
		}
		return []error{err}
	}
	errs := make([]error, len(p.Hops))
	for i, h := range p.Hops {
		errs[i] = e.ProbeHop(ctx, h)
	}
	return errs
}

func (e *Engine) Watch(ctx context.Context, w *Watchdog) ([]string, error) {
	sn, err := e.Store.Applied()
	if err != nil {
		return nil, err
	}
	var events []string
	old := e.Health()
	if !w.seeded {
		w.seed(old, sn.Config.Profiles)
	}
	health := map[string]Health{}
	for _, p := range usedProfiles(sn.Config) {
		errs := e.checkProfile(ctx, w, p)
		h := Health{Active: -1}
		for i, err := range errs {
			key := fmt.Sprintf("%s/%d", p.Name, i)
			if w.observe(key, err) && h.Active < 0 {
				h.Active = i
			}
			if err != nil {
				events = append(events, fmt.Sprintf("probe %s hop %d: %v", p.Name, i, err))
			}
		}
		if h.Active < 0 {
			h = Health{Down: true}
		}
		if h != old[p.Name] {
			msg := fmt.Sprintf("egress %s: %s", p.Name, describe(p, h))
			events = append(events, msg)
			e.Store.Log("egress-watchdog", msg)
		}
		if h.Down || h.Active > 0 {
			health[p.Name] = h
		}
	}
	if !maps.Equal(old, health) {
		if err := e.SetHealth(health); err != nil {
			return events, err
		}
	}
	if err := e.App.ApplyRates(ctx); err != nil {
		events = append(events, "shaping: "+err.Error())
	}
	_, err = e.Reconcile(ctx)
	return events, err
}

const (
	HealthNotApplied = "not applied"
	HealthOK         = "ok"
	HealthDown       = "down"
	HealthDegraded   = "degraded"
)

func ProfileHealth(applied state.EgressConfig, health map[string]Health, name string) (string, string) {
	p, ok := applied.Profile(name)
	if !ok {
		return HealthNotApplied, ""
	}
	h := health[name]
	switch {
	case h.Down:
		return HealthDown, ""
	case h.Active > 0 && h.Active < len(p.Hops):
		return HealthDegraded, p.Hops[h.Active].Dev
	}
	return HealthOK, ""
}

func describe(p state.EgressProfile, h Health) string {
	switch {
	case h.Down && p.Fallback != "":
		return "down, traffic switched to " + p.Fallback
	case h.Down:
		return "down, traffic is dropped until it recovers"
	case p.Relayed() || h.Active == 0:
		return "healthy"
	default:
		return "failed over to " + p.Hops[h.Active].Dev
	}
}
