package egress

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/gen"
	"github.com/k2so-dev/awgx/internal/secrets"
	"github.com/k2so-dev/awgx/internal/state"
)

const hookConf = `[Interface]
PrivateKey = AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=
Address = 10.9.0.2/32
ListenPort = 34831
PostUp = iptables -A FORWARD -j ACCEPT
DNS = 1.1.1.1

[Peer]
PublicKey = AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=
AllowedIPs = 0.0.0.0/0
Endpoint = 203.0.113.1:51820
`

func uplinkConfig(t *testing.T) string {
	t.Helper()
	src, err := awgconf.Parse(strings.NewReader(hookConf))
	if err != nil {
		t.Fatal(err)
	}
	if src.Interface.Obfuscation, err = gen.Obfuscation(gen.Presets[0]); err != nil {
		t.Fatal(err)
	}
	return src.String()
}

func TestPrepareUplinkHooks(t *testing.T) {
	c, _, err := PrepareUplink(uplinkConfig(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := c.String()
	if strings.Contains(out, "PostUp") || strings.Contains(out, "iptables") || !strings.Contains(out, "Table = off") {
		t.Fatalf("unexpected PostUp:\n%s", out)
	}
}

func TestPrepareUplinkKeepsPort(t *testing.T) {
	c, _, err := PrepareUplink(uplinkConfig(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Interface.ListenPort != 34831 || uplinkPort(c.String()) != 34831 {
		t.Fatalf("ListenPort lost:\n%s", c.String())
	}
}

func TestCheckUplinkPort(t *testing.T) {
	box, err := secrets.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(filepath.Join(t.TempDir(), "state.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveUplink(&state.Uplink{Name: "awg1", Conf: uplinkConfig(t)}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s}
	insts := []state.Instance{{Name: "awg0", ListenPort: 33406}}
	for _, tc := range []struct {
		name string
		port int
		bad  bool
	}{
		{"awg1", 34831, false},
		{"awg2", 34831, true},
		{"awg2", 33406, true},
		{"awg2", 70000, true},
		{"awg2", 0, false},
		{"awg2", 40000, false},
	} {
		if err := e.checkUplinkPort(tc.name, tc.port, insts); (err != nil) != tc.bad {
			t.Errorf("%s port %d: err %v, want error %v", tc.name, tc.port, err, tc.bad)
		}
	}
}
