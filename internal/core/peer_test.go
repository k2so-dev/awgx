package core

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/state"
)

func TestEnableExpiredPeer(t *testing.T) {
	app := New()
	app.Paths.StateDir, app.Paths.ConfDir = t.TempDir(), t.TempDir()
	s, err := app.Init()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	testInstance(t, s, "awgtest9", 40000)
	priv, _ := awgconf.NewPrivateKey()
	p := state.Peer{Instance: "awgtest9", Name: "old", PrivateKey: priv, PublicKey: priv.Public(),
		Address4: netip.MustParseAddr("10.66.0.2"), ExpiresAt: time.Now().Add(-time.Hour)}
	if err := s.SavePeer(&p); err != nil {
		t.Fatal(err)
	}
	enable := func(p *state.Peer) error { p.Enabled = true; return nil }
	if _, err := app.UpdatePeer(context.Background(), "old", enable); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired peer enabled: %v", err)
	}
	if got, _ := s.Peer("old"); got.Enabled {
		t.Fatal("expired peer saved as enabled")
	}
	renew := func(p *state.Peer) error { p.Enabled, p.ExpiresAt = true, time.Now().Add(time.Hour); return nil }
	if _, err := app.UpdatePeer(context.Background(), "old", renew); err != nil {
		t.Fatal(err)
	}
}
