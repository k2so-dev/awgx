package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/perf"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/spf13/cobra"
)

type perfReport struct {
	CPU        *perf.Report      `json:"cpu,omitempty"`
	MTU        []perf.MTUResult  `json:"mtu,omitempty"`
	InnerMTU   int               `json:"recommended_mtu,omitempty"`
	CurrentMTU int               `json:"current_mtu,omitempty"`
	Throughput []perf.Throughput `json:"throughput,omitempty"`
	Advice     []string          `json:"advice"`
}

func perfCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "perf",
		Short: "Find what limits the speed: CPU and softirq, buffers, drops, MTU, throughput",
		RunE: func(cmd *cobra.Command, args []string) error {
			var r perfReport
			if err := perfCPU(cmd.Context(), app, &r, time.Second); err != nil {
				return err
			}
			perfMTU(cmd.Context(), app, &r, nil)
			return printPerf(cmd.OutOrStdout(), r, asJSON)
		},
	}
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.AddCommand(perfMTUCmd(app, &asJSON), perfCPUCmd(app, &asJSON), perfThroughputCmd(app, &asJSON), perfAgentCmd(), perfMeasureCmd())
	return cmd
}

func uplinkDevs(ctx context.Context, app *core.App) []string {
	if up, err := netx.DefaultUplink(ctx, app.Run); err == nil {
		return []string{up.Dev}
	}
	return nil
}

func perfCPU(ctx context.Context, app *core.App, r *perfReport, d time.Duration) error {
	rep, err := perf.Sample(ctx, uplinkDevs(ctx, app), d)
	if err != nil {
		return err
	}
	r.CPU = &rep
	r.Advice = append(r.Advice, rep.Advice...)
	return nil
}

func perfMTU(ctx context.Context, app *core.App, r *perfReport, hosts []string) {
	if len(hosts) == 0 {
		hosts = []string{"1.1.1.1", "8.8.8.8"}
	}
	for _, h := range hosts {
		a, err := netip.ParseAddr(h)
		if err != nil {
			r.Advice = append(r.Advice, "skipping "+h+": not an IP address")
			continue
		}
		r.MTU = append(r.MTU, perf.PathMTU(ctx, app.Run, a))
	}
	inst, err := app.Current()
	if err != nil {
		return
	}
	r.CurrentMTU = inst.MTU
	var advice []string
	r.InnerMTU, advice = perf.MTUAdvice(r.MTU, inst.MTU, inst.Obfuscation.S4, inst.Endpoint)
	r.Advice = append(r.Advice, advice...)
}

func printPerf(w io.Writer, r perfReport, asJSON bool) error {
	if asJSON {
		return printJSON(w, r)
	}
	if r.CPU != nil {
		fmt.Fprintf(w, "CPU over %s:\n", r.CPU.Interval)
		for _, c := range r.CPU.CPUs {
			fmt.Fprintf(w, "  %-6s busy %5.1f%%  softirq %5.1f%%  NET_RX %d\n", c.Name, c.Busy, c.SoftIRQ, c.NetRx)
		}
		for _, i := range r.CPU.Ifaces {
			fmt.Fprintf(w, "  %-8s rx queues %d, RPS %v, dropped rx %d tx %d\n", i.Name, i.Queues, i.RPS, i.RxDropped, i.TxDropped)
		}
		fmt.Fprintf(w, "  UDP RcvbufErrors %d, SndbufErrors %d, rmem_max %d, wmem_max %d\n",
			r.CPU.RcvbufErrors, r.CPU.SndbufErrors, r.CPU.RmemMax, r.CPU.WmemMax)
	}
	if len(r.MTU) > 0 {
		fmt.Fprintln(w, "Path MTU:")
		for _, m := range r.MTU {
			if m.Reachable {
				fmt.Fprintf(w, "  %-16s %d\n", m.Host, m.PathMTU)
			} else {
				fmt.Fprintf(w, "  %-16s no answer\n", m.Host)
			}
		}
		if r.InnerMTU > 0 {
			fmt.Fprintf(w, "  peer MTU %d, recommended %d\n", r.CurrentMTU, r.InnerMTU)
		}
	}
	if len(r.Throughput) > 0 {
		fmt.Fprintln(w, "Throughput:")
		for _, l := range perf.Describe(r.Throughput) {
			fmt.Fprintln(w, "  "+l)
		}
	}
	if len(r.Advice) == 0 && r.CPU != nil {
		fmt.Fprintln(w, "nothing limits the speed in this sample")
	}
	for _, a := range r.Advice {
		fmt.Fprintln(w, "advice: "+a)
	}
	return nil
}

func perfMTUCmd(app *core.App, asJSON *bool) *cobra.Command {
	var hosts []string
	var peer string
	cmd := &cobra.Command{
		Use:   "mtu",
		Short: "Probe the path MTU with DF pings and recommend the peer MTU",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer != "" {
				rows, err := app.PeerRows(cmd.Context(), "")
				if err != nil {
					return err
				}
				for _, r := range rows {
					if r.Name == peer && r.Endpoint != "" {
						ap, err := netip.ParseAddrPort(r.Endpoint)
						if err == nil {
							hosts = append(hosts, ap.Addr().Unmap().String())
						}
					}
				}
				if len(hosts) == 0 {
					return fmt.Errorf("peer %s has no known endpoint yet", peer)
				}
			}
			var r perfReport
			perfMTU(cmd.Context(), app, &r, hosts)
			return printPerf(cmd.OutOrStdout(), r, *asJSON)
		},
	}
	cmd.Flags().StringSliceVar(&hosts, "host", nil, "hosts to probe (default 1.1.1.1, 8.8.8.8)")
	cmd.Flags().StringVar(&peer, "peer", "", "probe the endpoint of this peer")
	return cmd
}

func perfCPUCmd(app *core.App, asJSON *bool) *cobra.Command {
	var seconds int
	cmd := &cobra.Command{
		Use:   "cpu",
		Short: "Per-core load, softirq, RPS, UDP buffer errors and drops",
		RunE: func(cmd *cobra.Command, args []string) error {
			var r perfReport
			if err := perfCPU(cmd.Context(), app, &r, time.Duration(seconds)*time.Second); err != nil {
				return err
			}
			return printPerf(cmd.OutOrStdout(), r, *asJSON)
		},
	}
	cmd.Flags().IntVar(&seconds, "seconds", 2, "sample length")
	return cmd
}

func perfThroughputCmd(app *core.App, asJSON *bool) *cobra.Command {
	var peer string
	var seconds int
	cmd := &cobra.Command{
		Use:   "throughput",
		Short: "TCP throughput through the tunnel: test peer in a netns, or a real peer running perf agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			var r perfReport
			d := time.Duration(seconds) * time.Second
			var err error
			if peer == "" {
				inst, ierr := app.Current()
				if ierr != nil {
					return ierr
				}
				err = app.Locked(func() error {
					r.Throughput, err = perf.Loopback(cmd.Context(), app, inst, d)
					return err
				})
				r.Advice = append(r.Advice, "loopback through "+inst.Name+" in a network namespace: measures the CPU cost of the datapath, not the network")
			} else {
				s, serr := app.Store()
				if serr != nil {
					return serr
				}
				p, perr := s.Peer(peer)
				if perr != nil {
					return perr
				}
				r.Throughput, err = perf.Measure(cmd.Context(), perf.AgentAddr(p.Address4.String()), d, "server to peer", "peer to server")
				if err != nil {
					err = fmt.Errorf("%w (is `awgx perf agent` running on the client?)", err)
				}
			}
			if err != nil {
				return err
			}
			return printPerf(cmd.OutOrStdout(), r, *asJSON)
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "real peer running awgx perf agent on its tunnel address")
	cmd.Flags().IntVar(&seconds, "seconds", 5, "duration of each direction")
	return cmd
}

func runAgent(cmd *cobra.Command, listen string, d time.Duration) error {
	if _, _, err := net.SplitHostPort(listen); err != nil {
		listen = perf.AgentAddr(listen)
	}
	l, err := perf.Listen(listen)
	if err != nil {
		return err
	}
	addr := l.Addr().(*net.TCPAddr)
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	if sysx.IsRoot() && sysx.Has("iptables") && (addr.IP.IsUnspecified() || addr.IP.To4() != nil) {
		rule := []string{"INPUT", "-p", "tcp", "--dport", strconv.Itoa(addr.Port), "-j", "ACCEPT"}
		if !addr.IP.IsUnspecified() {
			rule = append(rule[:1], append([]string{"-d", addr.IP.String()}, rule[1:]...)...)
		}
		run := sysx.Exec{}
		if _, err := run.Run(ctx, "iptables", append([]string{"-w", "-I"}, rule...)...); err == nil {
			defer run.Run(context.WithoutCancel(ctx), "iptables", append([]string{"-w", "-D"}, rule...)...)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "perf agent listening on %s for %s, run on the other side: awgx perf throughput --peer NAME, or awgx uplink speed NAME\n", l.Addr(), d)
	return perf.Serve(ctx, l)
}

func perfAgentCmd() *cobra.Command {
	var listen string
	var d time.Duration
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Answer throughput tests through the tunnel: on a client for perf throughput, on an upstream server for uplink speed",
		RunE:  func(cmd *cobra.Command, args []string) error { return runAgent(cmd, listen, d) },
	}
	cmd.Flags().StringVar(&listen, "listen", fmt.Sprintf(":%d", perf.AgentPort), "listen address, e.g. the tunnel IP 10.8.0.1")
	cmd.Flags().DurationVar(&d, "for", 30*time.Minute, "stop after this time, 0 to run until interrupted")
	return cmd
}

func perfMeasureCmd() *cobra.Command {
	var to, send, recv string
	var seconds int
	cmd := &cobra.Command{
		Use:    "measure",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := perf.Measure(cmd.Context(), to, time.Duration(seconds)*time.Second, send, recv)
			if err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "address")
	cmd.Flags().IntVar(&seconds, "seconds", 5, "seconds")
	cmd.Flags().StringVar(&send, "send", "send", "label")
	cmd.Flags().StringVar(&recv, "recv", "receive", "label")
	return cmd
}
