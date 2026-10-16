package netx

import (
	"context"
	"slices"
	"strings"
	"testing"
)

type recorder struct{ calls []string }

func (r *recorder) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func (r *recorder) RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

func TestFirewallUFW(t *testing.T) {
	r := &recorder{}
	if err := OpenFirewall(context.Background(), r, UFW, "awg0", 30306); err != nil {
		t.Fatal(err)
	}
	CloseFirewall(context.Background(), r, UFW, "awg0", 30306)
	want := []string{
		"ufw allow 30306/udp comment awgx awg0",
		"ufw delete allow 30306/udp",
		"ufw route delete allow in on awg0",
	}
	if !slices.Equal(r.calls, want) {
		t.Errorf("calls %q, want %q", r.calls, want)
	}
}

func TestFirewallArtifact(t *testing.T) {
	fw, iface, port, ok := ParseFirewallArtifact(FirewallArtifact(Firewalld, "awg1", 443))
	if !ok || fw != Firewalld || iface != "awg1" || port != 443 {
		t.Errorf("got %q %q %d %v", fw, iface, port, ok)
	}
	if _, _, _, ok := ParseFirewallArtifact("unit:awg-quick@awg0"); ok {
		t.Error("unit artifact parsed as firewall")
	}
}

func TestUFWAllows(t *testing.T) {
	status := `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), deny (routed)

To                         Action      From
--                         ------      ----
62445/udp                  ALLOW IN    Anywhere                   # AmneziaWG VPN
30300:30310                ALLOW IN    Anywhere
Anywhere on eth0           ALLOW FWD   Anywhere on awg1
`
	for _, c := range []struct {
		port int
		ok   bool
	}{
		{30306, true},
		{62445, true},
		{40000, false},
	} {
		if got := UFWAllows(status, c.port); got != c.ok {
			t.Errorf("%d: got %v, want %v", c.port, got, c.ok)
		}
	}
	if !UFWAllows("Default: allow (incoming), allow (outgoing), disabled (routed)", 1) {
		t.Error("permissive defaults not detected")
	}
	if UFWAllows("30306/tcp                  ALLOW IN    Anywhere", 30306) {
		t.Error("tcp rule accepted for udp")
	}
}

func TestUFWAllowOut(t *testing.T) {
	status := "Default: deny (incoming), allow (outgoing), deny (routed)\n\n51820/udp                  ALLOW OUT   Anywhere\n"
	if UFWAllows(status, 51820) {
		t.Error("ALLOW OUT counted as an open incoming port")
	}
}
