package perf

import "testing"

func TestParsePing(t *testing.T) {
	out := `PING 10.98.130.1 (10.98.130.1) 56(84) bytes of data.

--- 10.98.130.1 ping statistics ---
50 packets transmitted, 49 received, 2% packet loss, time 9810ms
rtt min/avg/max/mdev = 25.112/26.403/31.950/1.204 ms
`
	p, err := ParsePing(out)
	if err != nil {
		t.Fatal(err)
	}
	if p.Sent != 50 || p.Received != 49 || p.Loss != 2 || p.Avg != 26.403 || p.Max != 31.95 {
		t.Fatalf("got %+v", p)
	}
	p, err = ParsePing("--- x ping statistics ---\n5 packets transmitted, 0 received, +5 errors, 100% packet loss, time 4000ms\n")
	if err != nil || p.Received != 0 || p.Loss != 100 {
		t.Fatalf("got %+v %v", p, err)
	}
	if _, err := ParsePing("ping: unknown host"); err == nil {
		t.Fatal("want error")
	}
}
