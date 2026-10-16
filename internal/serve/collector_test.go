package serve

import (
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/state"
)

func TestObserve(t *testing.T) {
	c := NewCollector()
	p := state.Peer{ID: 1, PublicKey: awgconf.Key{1}}
	t0 := time.Unix(1_000_000, 0)
	obs := func(rx, tx uint64, hs time.Time, at time.Time) Result {
		return c.Observe(Observation{Peer: p, Rx: rx, Tx: tx, LastHandshake: hs}, at)
	}

	if r := obs(100, 100, t0, t0); r.Record {
		t.Fatal("first observation is a baseline")
	}
	r := obs(300, 150, t0.Add(20*time.Second), t0.Add(30*time.Second))
	if r.Sample.Rx != 200 || r.Sample.Tx != 50 || r.Sample.Handshakes != 1 || !r.OpenSession || !r.Record {
		t.Fatalf("unexpected %+v", r)
	}
	r = obs(310, 160, t0.Add(20*time.Second), t0.Add(40*time.Second))
	if r.Sample.Handshakes != 0 || r.OpenSession || !r.TouchSession {
		t.Fatalf("unexpected %+v", r)
	}
	r = obs(310, 500, t0.Add(20*time.Second), t0.Add(400*time.Second))
	if r.Sample.Failures != 1 || !r.CloseSession {
		t.Fatalf("want failure and closed session, got %+v", r)
	}
	r = obs(310, 900, t0.Add(20*time.Second), t0.Add(410*time.Second))
	if r.Sample.Failures != 0 {
		t.Fatal("failure counted twice for one stale period")
	}
	obs(310, 900, t0.Add(20*time.Second), t0.Add(420*time.Second))
	r = obs(310, 950, t0.Add(20*time.Second), t0.Add(430*time.Second))
	if r.Sample.Failures != 0 {
		t.Fatal("idle tick restarted the stale period")
	}
	r = obs(5, 5, t0.Add(500*time.Second), t0.Add(505*time.Second))
	if r.Sample.Rx != 5 || r.Sample.Tx != 5 {
		t.Fatalf("counter reset not handled: %+v", r.Sample)
	}
}
