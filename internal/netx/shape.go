package netx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	MaxRate     = 100000
	RateSetting = "rate:"
)

func ShapedRate(ctx context.Context, run sysx.Runner, dev string) int {
	out, err := run.Run(ctx, "tc", "qdisc", "show", "dev", dev, "root")
	if err != nil {
		return 0
	}
	return parseCakeRate(string(out))
}

func parseCakeRate(out string) int {
	f := strings.Fields(out)
	if len(f) < 2 || f[0] != "qdisc" || f[1] != "cake" {
		return 0
	}
	for i := 2; i+1 < len(f); i++ {
		if f[i] == "bandwidth" {
			return parseBits(f[i+1])
		}
	}
	return 0
}

func parseBits(s string) int {
	units := []struct {
		suffix string
		mul    float64
	}{{"Gbit", 1000}, {"Mbit", 1}, {"Kbit", 0.001}, {"bit", 0.000001}}
	for _, u := range units {
		if v, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return 0
			}
			return int(n*u.mul + 0.5)
		}
	}
	return 0
}

func Shape(ctx context.Context, run sysx.Runner, dev string, mbit int) error {
	cur := ShapedRate(ctx, run, dev)
	if cur == mbit {
		return nil
	}
	if mbit == 0 {
		_, err := run.Run(ctx, "tc", "qdisc", "del", "dev", dev, "root")
		return err
	}
	if _, err := run.Run(ctx, "tc", "qdisc", "replace", "dev", dev, "root", "cake", "bandwidth", strconv.Itoa(mbit)+"mbit", "besteffort"); err != nil {
		return fmt.Errorf("%w (is the sch_cake kernel module available?)", err)
	}
	return nil
}
