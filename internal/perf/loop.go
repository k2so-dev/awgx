package perf

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

func Loopback(ctx context.Context, app *core.App, inst state.Instance, d time.Duration) ([]Throughput, error) {
	l, err := Listen(net.JoinHostPort(inst.Address4().String(), "0"))
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go Serve(sctx, l)
	if sysx.Has("iptables") {
		rule := []string{"INPUT", "-i", inst.Name, "-p", "tcp", "--dport", strconv.Itoa(l.Addr().(*net.TCPAddr).Port), "-j", "ACCEPT"}
		if _, err := app.Run.Run(ctx, "iptables", append([]string{"-w", "-I"}, rule...)...); err == nil {
			defer app.Run.Run(context.WithoutCancel(ctx), "iptables", append([]string{"-w", "-D"}, rule...)...)
		}
	}
	tp, err := selftest.Start(ctx, app, inst, []netip.Prefix{inst.Subnet4})
	if err != nil {
		return nil, err
	}
	defer tp.Close(ctx)
	out, err := tp.Output(ctx, app.Paths.Binary, "perf", "measure", "--to", l.Addr().String(),
		"--seconds", strconv.Itoa(int(d.Seconds())), "--send", "peer to server", "--recv", "server to peer")
	if err != nil {
		return nil, err
	}
	var res []Throughput
	return res, json.Unmarshal(out, &res)
}
