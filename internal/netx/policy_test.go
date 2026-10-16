package netx

import "testing"

func TestDrift(t *testing.T) {
	want := Policy{Routes: []Route{{Family: 4, Table: 51701, Dev: "awg1"}}, Rules: []Rule{{Family: 4, Pref: 4900, IIf: "awg0", Table: 51701}}}
	have := want
	have.Routes = append(have.Routes, Route{Family: 4, Table: ProbeTable, Type: "unreachable"})
	have.Rules = append(have.Rules, Rule{Family: 4, Pref: PrefProbe, FwMark: 1, Table: ProbeTable})
	if d := Drift(have, want); len(d) != 0 {
		t.Fatalf("probe entries are drift: %v", d)
	}
	if d := Drift(Policy{}, want); len(d) != 2 {
		t.Fatalf("missing entries: %v", d)
	}
}
