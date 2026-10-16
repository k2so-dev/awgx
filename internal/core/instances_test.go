package core

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/state"
)

func testInstance(t *testing.T, s *state.Store, name string, port int) {
	t.Helper()
	priv, _ := awgconf.NewPrivateKey()
	hpk, _ := awgconf.NewRandomKey()
	inst := state.Instance{
		Name: name, ListenPort: port, Endpoint: "203.0.113.1", MTU: 1380,
		Subnet4: netip.MustParsePrefix("10.66.0.0/24"), Subnet6: netip.MustParsePrefix("fd00:1::/64"),
		DNS: []netip.Addr{netip.MustParseAddr("1.1.1.1")}, Preset: "balanced",
		Obfuscation: awgconf.Obfuscation{HeaderProtectionKey: hpk}, I1: i1.Settings{Profile: i1.None}, PrivateKey: priv,
	}
	if err := s.SaveInstance(&inst); err != nil {
		t.Fatal(err)
	}
}

func TestResolveInstance(t *testing.T) {
	app := New()
	app.Paths.StateDir = t.TempDir()
	s, err := app.Init()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if _, err := app.ResolveInstance(""); err != ErrNoInstances {
		t.Fatalf("no instances: %v", err)
	}
	testInstance(t, s, "fast", 40000)
	if inst, err := app.ResolveInstance(""); err != nil || inst.Name != "fast" {
		t.Fatalf("single instance: %v %v", inst.Name, err)
	}
	testInstance(t, s, "quiet", 40001)
	if _, err := app.ResolveInstance(""); err == nil || !strings.Contains(err.Error(), "--instance") {
		t.Fatalf("ambiguous selection accepted: %v", err)
	}
	testInstance(t, s, DefaultInstance, 40002)
	if inst, _ := app.ResolveInstance(""); inst.Name != DefaultInstance {
		t.Fatalf("default not preferred: %s", inst.Name)
	}
	if inst, _ := app.ResolveInstance("quiet"); inst.Name != "quiet" {
		t.Fatalf("explicit: %s", inst.Name)
	}
	if _, err := app.ResolveInstance("nope"); err == nil {
		t.Fatal("unknown instance accepted")
	}
}

func TestInstanceName(t *testing.T) {
	for name, want := range map[string]bool{"awg1": true, "fast-443": true, "a": true, "": false, "1awg": false,
		"Awg": false, "awg 1": false, "abcdefghijklmn": false, "abcdefghijklm": true, "../x": false} {
		if got := instanceName.MatchString(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}

func TestInnerMTU(t *testing.T) {
	for _, c := range []struct {
		link     int
		s4       uint16
		endpoint string
		want     int
	}{
		{1500, 24, "vpn.example.com", 1396},
		{1400, 42, "203.0.113.10", 1298},
		{1400, 42, "2001:db8::1", 1280},
		{1400, 24, "", 1296},
	} {
		if got := InnerMTU(c.link, c.s4, c.endpoint); got != c.want {
			t.Errorf("InnerMTU(%d, %d, %q) = %d, want %d", c.link, c.s4, c.endpoint, got, c.want)
		}
	}
}

func TestIPv6For(t *testing.T) {
	inst := state.Instance{Subnet4: netip.MustParsePrefix("10.212.113.0/24"), Subnet6: netip.MustParsePrefix("fd21:e71c:e66a::/64")}
	if got := ipv6For(inst, netip.MustParseAddr("10.212.113.7")); got != netip.MustParseAddr("fd21:e71c:e66a::7") {
		t.Errorf("got %s", got)
	}
	inst.Subnet6 = netip.Prefix{}
	if got := ipv6For(inst, netip.MustParseAddr("10.212.113.7")); got.IsValid() {
		t.Errorf("IPv6 off returned %s", got)
	}
}
