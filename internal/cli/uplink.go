package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/spf13/cobra"
)

func uplinkCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{Use: "uplink", Short: "Client awg interfaces to other servers, for cascades (egress type iface)"}
	cmd.AddCommand(uplinkAddCmd(app), uplinkSetCmd(app), uplinkRmCmd(app), uplinkLsCmd(app), uplinkTestCmd(app), uplinkSpeedCmd(app))
	return cmd
}

func uplinkAddCmd(app *core.App) *cobra.Command {
	var conf, port string
	var routed bool
	cmd := &cobra.Command{
		Use:     "add NAME --conf FILE [--port N]",
		Short:   "Bring up a client awg interface from a 3.1 .conf (Table = off, DNS ignored)",
		Example: "  awgx uplink add awg1 --conf upstream.conf\n  awgx egress add cascade --type iface --dev awg1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(conf)
			if err != nil {
				return err
			}
			n := 0
			if port != "" {
				if n, err = parsePort(port); err != nil {
					return err
				}
			}
			return egress.Locked(app, func(e *egress.Engine) error {
				warnings, err := e.AddUplink(cmd.Context(), args[0], string(data), n, routed)
				for _, w := range warnings {
					fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w)
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "uplink %s is up; check it with: awgx uplink test %s\n", args[0], args[0])
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&conf, "conf", "", "client .conf from the upstream server")
	cmd.Flags().StringVar(&port, "port", "", "fixed local UDP port, kept across restarts (default: ListenPort of the .conf, or random)")
	cmd.Flags().BoolVar(&routed, "routed", false, "no NAT on this uplink: upstream must route the peer subnet back")
	cmd.MarkFlagRequired("conf")
	return cmd
}

func uplinkSetCmd(app *core.App) *cobra.Command {
	var conf, routed, rate, port string
	var mtu int
	cmd := &cobra.Command{
		Use:     "set NAME [--conf FILE] [--mtu N] [--port N|auto] [--routed on|off] [--rate MBIT|off]",
		Short:   "Replace the config, change the MTU, port, NAT or rate limit of an uplink; profiles using it are kept",
		Example: "  awgx uplink set awg1 --conf new.conf\n  awgx uplink set awg1 --mtu 1316\n  awgx uplink set awg1 --port 34831\n  awgx uplink set awg1 --rate 240",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var ed egress.UplinkEdit
			if conf == "" && mtu == 0 && port == "" && routed == "" && rate == "" {
				return fmt.Errorf("pass --conf, --mtu, --port, --routed or --rate")
			}
			if port != "" {
				n := 0
				if port != "auto" {
					var err error
					if n, err = parsePort(port); err != nil {
						return err
					}
				}
				ed.Port = &n
			}
			mbit := 0
			if rate != "" {
				n, err := parseRate(rate)
				if err != nil {
					return err
				}
				mbit = n
			}
			if conf != "" {
				data, err := os.ReadFile(conf)
				if err != nil {
					return err
				}
				ed.Conf = string(data)
			}
			ed.MTU = mtu
			if routed != "" {
				if routed != "on" && routed != "off" {
					return fmt.Errorf("--routed must be on or off")
				}
				on := routed == "on"
				ed.Routed = &on
			}
			return egress.Locked(app, func(e *egress.Engine) error {
				if _, err := e.Store.Uplink(args[0]); err != nil {
					return err
				}
				if conf != "" || mtu != 0 || port != "" || routed != "" {
					warnings, err := e.UpdateUplink(cmd.Context(), args[0], ed)
					for _, w := range warnings {
						fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w)
					}
					if err != nil {
						return err
					}
					if conf == "" && mtu == 0 && routed == "" {
						fmt.Fprintf(cmd.OutOrStdout(), "uplink %s port: %s\n", args[0], fmtx.Port(*ed.Port, 0))
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), "uplink %s updated and restarted; check it with: awgx uplink test %s\n", args[0], args[0])
					}
				}
				if rate == "" {
					return nil
				}
				if err := app.SetRate(cmd.Context(), args[0], mbit); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "uplink %s rate limit: %s\n", args[0], fmtx.Rate(mbit))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&conf, "conf", "", "new client .conf from the upstream server")
	cmd.Flags().IntVar(&mtu, "mtu", 0, "uplink MTU (default: keep, or fit a new --conf to this server's link)")
	cmd.Flags().StringVar(&port, "port", "", "fixed local UDP port, kept across restarts, or auto for a random one (default: keep)")
	cmd.Flags().StringVar(&routed, "routed", "", "on: no NAT, upstream routes the peer subnets back; off: NAT")
	cmd.Flags().StringVar(&rate, "rate", "", "limit traffic sent to the upstream server, Mbit/s (CAKE), or off")
	return cmd
}

func uplinkRmCmd(app *core.App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm NAME",
		Short: "Stop and remove an uplink",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm(yes, "remove uplink "+args[0]+"?") {
				return fmt.Errorf("aborted")
			}
			return egress.Locked(app, func(e *egress.Engine) error { return e.RemoveUplink(cmd.Context(), args[0]) })
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func uplinkLsCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List uplinks",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := egress.New(app)
			if err != nil {
				return err
			}
			ups, err := e.Uplinks(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), ups)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tENDPOINT\tADDRESS\tMTU\tPORT\tRATE\tUP\tHANDSHAKE\tRX\tTX\tUSED BY")
			for _, u := range ups {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%v\t%s\t%s\t%s\t%s\n", u.Name, u.Endpoint, u.Address, u.MTU, fmtx.Port(u.Port, u.ListenPort), fmtx.Rate(u.Rate), u.Up, fmtx.Ago(u.LastHandshake),
					fmtx.Bytes(u.RxBytes), fmtx.Bytes(u.TxBytes), orDash(strings.Join(u.UsedBy, ",")))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func uplinkTestCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "test NAME",
		Short: "Check handshake and traffic through an uplink",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return egress.Locked(app, func(e *egress.Engine) error {
				lines, err := e.TestUplink(cmd.Context(), args[0])
				for _, l := range lines {
					fmt.Fprintln(cmd.OutOrStdout(), l)
				}
				return err
			})
		},
	}
}

func uplinkSpeedCmd(app *core.App) *cobra.Command {
	var o egress.SpeedOpts
	cmd := &cobra.Command{
		Use:   "speed NAME [--agent IP[:PORT]] [--seconds 10] [--streams 1]",
		Short: "Measure throughput, ping, loss, retransmits and CPU through an uplink",
		Long:  "Needs `awgx perf agent --listen TUNNEL_IP` on the upstream server. The agent defaults to the .1 address of the uplink subnet; --agent is remembered.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app.Out = cmd.OutOrStdout()
			return egress.Locked(app, func(e *egress.Engine) error { return e.SpeedUplink(cmd.Context(), args[0], o) })
		},
	}
	cmd.Flags().StringVar(&o.Agent, "agent", "", "address of awgx perf agent on the upstream server")
	cmd.Flags().IntVar(&o.Seconds, "seconds", 10, "duration of each direction, 3-60")
	cmd.Flags().IntVar(&o.Streams, "streams", 1, "parallel TCP streams, 1-16")
	return cmd
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("--port takes 1-65535 or auto")
	}
	return n, nil
}
