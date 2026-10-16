package core

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "30d": 30 * 24 * time.Hour, "12h": 12 * time.Hour}
	for in, want := range cases {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("%q: got %v %v", in, got, err)
		}
	}
	for _, in := range []string{"0d", "-1h", "x", "3w", "200000d", "1000000h"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
