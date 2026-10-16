package cli

import (
	"fmt"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/serve"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/stats"
	"github.com/k2so-dev/awgx/internal/webconf"
	"github.com/spf13/cobra"
)

func statsCmd(app *core.App) *cobra.Command {
	var peer, since, netLabel string
	var byI1, asJSON bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Traffic and handshake statistics collected by awgx serve",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := core.ParseDuration(since)
			if err != nil {
				return err
			}
			store, err := app.Store()
			if err != nil {
				return err
			}
			now := time.Now()
			from := now.Add(-d)
			var peerID int64
			var peerInst string
			if peer != "" {
				p, err := store.Peer(peer)
				if err != nil {
					return err
				}
				peerID, peerInst = p.ID, p.Instance
				if app.Selected != "" && app.Selected != peerInst {
					return fmt.Errorf("peer %s is on %s, not %s", peer, peerInst, app.Selected)
				}
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			if byI1 {
				inst, err := app.Current()
				if peerInst != "" {
					inst, err = store.Instance(peerInst)
				}
				if err != nil {
					return err
				}
				f := state.TrafficFilter{Peer: peerID, Instance: inst.Name}
				samples, err := store.Traffic(f, from)
				if err != nil {
					return err
				}
				peers, err := store.Peers(inst.Name)
				if err != nil {
					return err
				}
				sessions, err := store.Sessions(f, from)
				if err != nil {
					return err
				}
				var infos []stats.PeerInfo
				for _, p := range peers {
					if peerID != 0 && p.ID != peerID {
						continue
					}
					infos = append(infos, stats.PeerInfo{ID: p.ID, Profile: p.I1Profile(inst), NetLabel: p.NetLabel})
				}
				summary := stats.ByI1(infos, samples, sessions, from, now, netLabel)
				if asJSON {
					return printJSON(cmd.OutOrStdout(), summary)
				}
				fmt.Fprintln(w, "I1\tPEERS\tALIVE\tSESSIONS\tMEDIAN SESSION\tHANDSHAKES/PEER-H\tFAILURES")
				for _, s := range summary {
					fmt.Fprintf(w, "%s\t%d\t%.0f%%\t%d\t%s\t%.1f\t%d\n", s.Profile, s.Peers, s.AliveShare*100, s.Sessions,
						s.MedianSession.Round(time.Second), s.HandshakesPerPH, s.Failures)
				}
				return w.Flush()
			}
			f := state.TrafficFilter{Peer: peerID, Net: netLabel}
			if app.Selected != "" {
				inst, err := app.Current()
				if err != nil {
					return err
				}
				f.Instance = inst.Name
			}
			totals, err := store.TrafficTotals(f, from)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), totals)
			}
			fmt.Fprintln(w, "PEER\tRX\tTX\tHANDSHAKES\tFAILURES\tLAST HANDSHAKE")
			for _, t := range totals {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", t.Peer, fmtx.Bytes(t.Rx), fmtx.Bytes(t.Tx), t.Handshakes, t.Failures, fmtx.Ago(t.LastHandshake))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "only this peer")
	cmd.Flags().StringVar(&since, "since", "24h", "period, e.g. 24h or 7d")
	cmd.Flags().BoolVar(&byI1, "by-i1", false, "compare I1 profiles: alive share, median session, handshake rate")
	cmd.Flags().StringVar(&netLabel, "net", "", "only peers with this network label")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func serveCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon: statistics, reconcile, expiry, I1 rotation (usually via awgx-serve.service)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return serve.New(app, serve.Default, updateChecker(app)).Run(ctx)
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "enable",
			Short: "Turn on awgx-serve: traffic stats, client expiry, egress failover, update check",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return app.StartServe(cmd.Context())
			},
		},
		&cobra.Command{
			Use:   "disable",
			Short: "Turn off awgx-serve",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := webconf.LoadConfig(app)
				if err != nil {
					return err
				}
				if c.Enabled {
					return fmt.Errorf("the web UI runs in awgx-serve: run awgx web disable first")
				}
				return app.SetServe(cmd.Context(), false)
			},
		},
	)
	return cmd
}
