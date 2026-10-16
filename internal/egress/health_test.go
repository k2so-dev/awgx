package egress

import (
	"testing"

	"github.com/k2so-dev/awgx/internal/state"
)

func TestSameConfigStable(t *testing.T) {
	draft := state.EgressConfig{Default: map[string]state.Assign{"awg0": {}, "awg1": {Profile: state.Direct}}, Peers: map[string]state.Assign{}}
	applied := state.EgressConfig{Default: map[string]state.Assign{}, Peers: map[string]state.Assign{}}
	for i := 0; i < 2; i++ {
		if !SameConfig(draft, applied) {
			t.Fatalf("call %d: unassigned instances must equal an empty applied config", i)
		}
	}
	if len(draft.Default) != 2 {
		t.Fatalf("SameConfig mutated the draft: %v", draft.Default)
	}
}

func TestProfileHealth(t *testing.T) {
	p := state.EgressProfile{Name: "via-b", Hops: []state.Hop{{Dev: "awg1"}, {Dev: "awg2"}}}
	applied := state.EgressConfig{Profiles: []state.EgressProfile{p}}
	cases := []struct {
		health  map[string]Health
		applied state.EgressConfig
		want    string
	}{
		{nil, state.EgressConfig{}, HealthNotApplied},
		{nil, applied, HealthOK},
		{map[string]Health{"via-b": {Down: true}}, applied, HealthDown},
		{map[string]Health{"via-b": {Active: 1}}, applied, HealthDegraded},
	}
	for _, c := range cases {
		if got, _ := ProfileHealth(c.applied, c.health, p.Name); got != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}
