package core

import "testing"

func TestHostPeer(t *testing.T) {
	if got := HostPeer("awg0"); got != "awg0.h" {
		t.Fatalf("short name: %s", got)
	}
	a, b := HostPeer("office-east-01"), HostPeer("office-east-02")
	if a == b || len(a) > 15 || len(b) > 15 {
		t.Fatalf("long names: %s %s", a, b)
	}
}
