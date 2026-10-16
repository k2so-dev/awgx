package stats

import (
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/state"
)

func TestByI1(t *testing.T) {
	now := time.Unix(10_000, 0)
	since := now.Add(-2 * time.Hour)
	peers := []PeerInfo{{1, "none", "mts"}, {2, "quic", "mts"}, {3, "quic", "home"}}
	samples := []state.Sample{
		{PeerID: 1, Handshakes: 2},
		{PeerID: 2, Handshakes: 4, Failures: 1},
		{PeerID: 3},
	}
	sessions := []state.Session{
		{PeerID: 2, I1Profile: "quic", NetLabel: "mts", Start: now.Add(-time.Hour), End: now},
		{PeerID: 3, I1Profile: "quic", NetLabel: "home", Start: now.Add(-30 * time.Minute), End: now},
		{PeerID: 2, I1Profile: "quic", NetLabel: "mts", Start: now.Add(-20 * time.Minute), End: now},
		{PeerID: 9, I1Profile: "quic", NetLabel: "mts", Start: now.Add(-50 * time.Minute), End: now},
	}
	got := ByI1(peers, samples, sessions, since, now, "")
	if len(got) != 2 || got[1].Profile != "quic" {
		t.Fatalf("unexpected %+v", got)
	}
	q := got[1]
	if q.Peers != 2 || q.AlivePeers != 1 || q.AliveShare != 0.5 || q.Failures != 1 || q.MedianSession != 30*time.Minute || q.HandshakesPerPH != 1 {
		t.Fatalf("unexpected quic summary %+v", q)
	}
	mts := ByI1(peers, samples, sessions, since, now, "mts")
	if mts[1].Peers != 1 || mts[1].Sessions != 2 {
		t.Fatalf("net filter: %+v", mts)
	}
}
