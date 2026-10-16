package cli

import (
	"io"
	"testing"

	"github.com/k2so-dev/awgx/internal/core"
)

func TestStrictArgs(t *testing.T) {
	for _, args := range [][]string{{"backup", "create"}, {"version", "x"}, {"egress", "apply", "now"}} {
		root := New(core.New())
		root.SetArgs(args)
		root.SetOut(io.Discard)
		if err := root.Execute(); err == nil {
			t.Fatalf("%v: extra args accepted", args)
		}
	}
}
