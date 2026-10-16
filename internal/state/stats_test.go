package state

import (
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/secrets"
)

func TestRollupKeepsLatestEndpoint(t *testing.T) {
	box, err := secrets.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	priv, _ := awgconf.NewPrivateKey()
	hpk, _ := awgconf.NewRandomKey()
	inst := Instance{Name: "awg0", ListenPort: 51820, PrivateKey: priv, Subnet4: netip.MustParsePrefix("10.66.0.0/24"),
		Obfuscation: awgconf.Obfuscation{HeaderProtectionKey: hpk}}
	if err := s.SaveInstance(&inst); err != nil {
		t.Fatal(err)
	}
	p := Peer{Instance: "awg0", Name: "phone", PrivateKey: priv, PublicKey: priv.Public(), Address4: netip.MustParseAddr("10.66.0.2")}
	if err := s.SavePeer(&p); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start := time.Unix(now.Add(-3*time.Hour).Unix()/Bucket5m*Bucket5m, 0)
	err = s.AddSamples([]Sample{
		{PeerID: p.ID, At: start, Rx: 1, Endpoint: "95.1.2.3:4000"},
		{PeerID: p.ID, At: start.Add(time.Minute), Rx: 1, Endpoint: "203.0.113.5:51820"},
		{PeerID: p.ID, At: start.Add(2 * time.Minute), Rx: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Traffic(TrafficFilter{}, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Endpoint != "203.0.113.5:51820" || got[0].Rx != 3 {
		t.Fatalf("rollup: %+v", got)
	}
}

func TestTrafficTotalsAndSeries(t *testing.T) {
	box, err := secrets.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	priv, _ := awgconf.NewPrivateKey()
	hpk, _ := awgconf.NewRandomKey()
	inst := Instance{Name: "awg0", ListenPort: 51820, PrivateKey: priv, Subnet4: netip.MustParsePrefix("10.66.0.0/24"),
		Obfuscation: awgconf.Obfuscation{HeaderProtectionKey: hpk}}
	if err := s.SaveInstance(&inst); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i, name := range []string{"a", "b"} {
		k, _ := awgconf.NewPrivateKey()
		p := Peer{Instance: "awg0", Name: name, PrivateKey: k, PublicKey: k.Public(), Address4: netip.AddrFrom4([4]byte{10, 66, 0, byte(2 + i)})}
		if err := s.SavePeer(&p); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	since := time.Unix(time.Now().Add(-time.Hour).Unix(), 0)
	err = s.AddSamples([]Sample{
		{PeerID: ids[0], At: since.Add(time.Minute), Rx: 1, Tx: 2, Endpoint: "95.1.2.3:4000"},
		{PeerID: ids[0], At: since.Add(2 * time.Minute), Rx: 3, Handshakes: 1, LastHandshake: since.Add(2 * time.Minute)},
		{PeerID: ids[1], At: since.Add(30 * time.Minute), Rx: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.TrafficTotals(TrafficFilter{}, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Peer != "b" || got[1].Rx != 4 || got[1].Tx != 2 || got[1].Handshakes != 1 ||
		got[1].Endpoint != "95.1.2.3:4000" || !got[1].LastHandshake.Equal(since.Add(2*time.Minute)) {
		t.Fatalf("totals: %+v", got)
	}
	series, err := s.TrafficSeries(TrafficFilter{Peer: ids[0]}, since, since.Add(time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 60 || series[1].Rx != 1 || series[2].Rx != 3 || series[30].Rx != 0 {
		t.Fatalf("series: %+v", series[:3])
	}
}
