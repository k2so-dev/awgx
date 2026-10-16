package web

import (
	"strings"
	"testing"

	"github.com/k2so-dev/awgx/internal/i1"
)

func TestI1Options(t *testing.T) {
	js, err := assets.ReadFile("assets/peers.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range i1.Names() {
		if !strings.Contains(string(js), "{ v: '"+n+"'") {
			t.Errorf("I1_OPTS misses %s", n)
		}
	}
}
