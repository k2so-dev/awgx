package perf

import (
	"context"
	"testing"
	"time"
)

func TestMeasure(t *testing.T) {
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, l)
	res, err := Measure(ctx, l.Addr().String(), time.Second, "up", "down")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Direction != "up" || res[1].Direction != "down" {
		t.Fatalf("unexpected result %+v", res)
	}
	for _, r := range res {
		if r.Mbps <= 0 {
			t.Errorf("%s: no throughput", r.Direction)
		}
	}
}

func TestMTUAdvice(t *testing.T) {
	want, advice := MTUAdvice([]MTUResult{{Reachable: true, PathMTU: 1500}, {Reachable: true, PathMTU: 1420}}, 1400, 20, "")
	if want != 1320 || len(advice) != 1 {
		t.Fatalf("got %d %v", want, advice)
	}
	if _, advice := MTUAdvice([]MTUResult{{}}, 1400, 20, ""); len(advice) != 1 {
		t.Fatal("expected a no-answer note")
	}
}

func TestMTUAdviceFloor(t *testing.T) {
	want, advice := MTUAdvice([]MTUResult{{Reachable: true, PathMTU: 1320}}, 1280, 42, "203.0.113.7")
	if want != 1280 || len(advice) != 1 {
		t.Fatalf("got %d %v", want, advice)
	}
}
