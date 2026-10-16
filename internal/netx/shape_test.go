package netx

import "testing"

func TestParseCakeRate(t *testing.T) {
	cases := map[string]int{
		"qdisc cake 8001: root refcnt 2 bandwidth 240Mbit besteffort triple-isolate nonat nowash no-ack-filter split-gso rtt 100ms raw overhead 0 \n": 240,
		"qdisc cake 8002: root refcnt 2 bandwidth 1Gbit besteffort\n":                                                                                 1000,
		"qdisc cake 8003: root refcnt 2 bandwidth unlimited diffserv3\n":                                                                              0,
		"qdisc fq 0: root refcnt 2 limit 10000p\n":                                                                                                    0,
		"qdisc noqueue 0: root refcnt 2\n":                                                                                                            0,
		"":                                                                                                                                            0,
	}
	for in, want := range cases {
		if got := parseCakeRate(in); got != want {
			t.Errorf("%q: got %d, want %d", in, got, want)
		}
	}
}
