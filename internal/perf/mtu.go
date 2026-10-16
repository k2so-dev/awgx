package perf

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	minMTU       = 576
	maxMTU       = 1500
	ipv4Overhead = 28
	ipv6Overhead = 48
)

type MTUResult struct {
	Host      string `json:"host"`
	PathMTU   int    `json:"path_mtu"`
	Reachable bool   `json:"reachable"`
}

func pingDF(ctx context.Context, run sysx.Runner, host netip.Addr, size int) bool {
	family := "-4"
	payload := size - ipv4Overhead
	if host.Is6() {
		family, payload = "-6", size-ipv6Overhead
	}
	_, err := run.Run(ctx, "ping", family, "-n", "-q", "-M", "do", "-c", "1", "-W", "1", "-s", strconv.Itoa(payload), host.String())
	return err == nil
}

func PathMTU(ctx context.Context, run sysx.Runner, host netip.Addr) MTUResult {
	r := MTUResult{Host: host.String()}
	low := minMTU
	if host.Is6() {
		low = 1280
	}
	reached := false
	for range 2 {
		if pingDF(ctx, run, host, low) {
			reached = true
			break
		}
	}
	if !reached {
		return r
	}
	r.Reachable = true
	if pingDF(ctx, run, host, maxMTU) {
		r.PathMTU = maxMTU
		return r
	}
	hi := maxMTU
	for hi-low > 1 {
		mid := (low + hi) / 2
		if pingDF(ctx, run, host, mid) {
			low = mid
		} else {
			hi = mid
		}
	}
	r.PathMTU = low
	return r
}

func MTUAdvice(results []MTUResult, current int, s4 uint16, endpoint string) (int, []string) {
	best := 0
	for _, r := range results {
		if r.Reachable && (best == 0 || r.PathMTU < best) {
			best = r.PathMTU
		}
	}
	if best == 0 {
		return 0, []string{"no probe host answered DF pings; MTU could not be measured"}
	}
	want := core.InnerMTU(best, s4, endpoint)
	if best-core.TunnelOverhead(s4, endpoint) < core.MinTunnelMTU {
		return want, []string{fmt.Sprintf("path MTU %d cannot carry the minimum tunnel MTU %d with S4 %d: packets fragment, lower S4", best, core.MinTunnelMTU, s4)}
	}
	switch {
	case current > want:
		return want, []string{fmt.Sprintf("path MTU is %d: lower the peer MTU from %d to %d to avoid fragmentation", best, current, want)}
	case current < want-40:
		return want, []string{fmt.Sprintf("path MTU is %d: the peer MTU %d could be raised to %d", best, current, want)}
	}
	return want, nil
}
