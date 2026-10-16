package state

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/secrets"
)

func TestRoundTrip(t *testing.T) {
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
	inst := Instance{
		Name:        "awg0",
		ListenPort:  51820,
		Endpoint:    "203.0.113.1",
		MTU:         1380,
		Subnet4:     netip.MustParsePrefix("10.66.0.0/24"),
		Subnet6:     netip.MustParsePrefix("fd00:1::/64"),
		DNS:         []netip.Addr{netip.MustParseAddr("1.1.1.1")},
		Preset:      "balanced",
		Obfuscation: awgconf.Obfuscation{S1: 20, HeaderProtectionKey: hpk, I: [5]string{"<r 10>"}},
		I1:          i1.Settings{Profile: i1.Noise},
		PrivateKey:  priv,
	}
	if err := s.SaveInstance(&inst); err != nil {
		t.Fatal(err)
	}
	got, err := s.Instance("awg0")
	if err != nil {
		t.Fatal(err)
	}
	if got.PrivateKey != priv || got.Obfuscation != inst.Obfuscation || got.Subnet6 != inst.Subnet6 || got.I1 != inst.I1 {
		t.Fatalf("instance mismatch: %+v", got)
	}

	ppriv, _ := awgconf.NewPrivateKey()
	psk, _ := awgconf.NewRandomKey()
	p := Peer{
		Instance:     "awg0",
		Name:         "alice",
		PublicKey:    ppriv.Public(),
		PrivateKey:   ppriv,
		PresharedKey: psk,
		Address4:     netip.MustParseAddr("10.66.0.2"),
		Address6:     netip.MustParseAddr("fd00:1::2"),
		AllowedIPs:   []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		Egress:       "direct",
		Keepalive:    awgconf.Range{Lo: 20, Hi: 30},
		I1:           &i1.Settings{Profile: i1.STUN},
		Enabled:      true,
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
	if err := s.SavePeer(&p); err != nil {
		t.Fatal(err)
	}
	gp, err := s.Peer("alice")
	if err != nil {
		t.Fatal(err)
	}
	if gp.PrivateKey != ppriv || gp.PresharedKey != psk || gp.Keepalive != p.Keepalive || gp.I1.Profile != i1.STUN ||
		!gp.ExpiresAt.Equal(p.ExpiresAt) || len(gp.AllowedIPs) != 2 {
		t.Fatalf("peer mismatch: %+v", gp)
	}
	p.Enabled = false
	if err := s.SavePeer(&p); err != nil {
		t.Fatal(err)
	}
	if gp, _ = s.Peer("alice"); gp.Enabled {
		t.Fatal("update not saved")
	}
	if err := s.DeletePeer("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Peer("alice"); err == nil {
		t.Fatal("peer not deleted")
	}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalSource(t *testing.T) {
	box, err := secrets.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "state.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Log("a", "one")
	s.Source = "web"
	s.Log("b", "two")
	j, err := s.Journal(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(j) != 2 || j[0].Source != "web" || j[1].Source != "" {
		t.Fatalf("got %+v", j)
	}
}

func TestLockContext(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := LockContext(ctx, dir); err == nil {
		t.Fatal("got the lock while it was held")
	}
	unlock()
	release, err := LockContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
