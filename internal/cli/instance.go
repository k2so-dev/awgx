package cli

import (
	"fmt"
	"strconv"
	"text/tabwriter"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
	"github.com/spf13/cobra"
)

func instanceCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{Use: "instance", Short: "Run several AmneziaWG interfaces with their own ports and parameters"}
	cmd.AddCommand(instanceAddCmd(app), instanceRmCmd(app), instanceLsCmd(app), instanceSetCmd(app))
	return cmd
}

func instanceAddCmd(app *core.App) *cobra.Command {
	var g genFlags
	var skipSelfTest, asJSON bool
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Generate and start another instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.Installed() {
				return core.ErrNotInstalled
			}
			opts, err := g.options()
			if err != nil {
				return err
			}
			opts.Name = args[0]
			ctx := cmd.Context()
			var inst state.Instance
			var issues validate.Issues
			err = app.Locked(func() (err error) {
				inst, issues, err = app.AddInstance(ctx, opts)
				return err
			})
			printIssues(cmd.ErrOrStderr(), issues)
			if err != nil {
				return err
			}
			if !skipSelfTest {
				app.Logf("self-test: handshake from a test peer in a network namespace")
				if err := selftest.Run(ctx, app, inst); err != nil {
					return fmt.Errorf("self-test failed: %w (remove with: awgx instance rm %s)", err, inst.Name)
				}
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), inst)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "started %s: %s:%d, preset %s, I1 %s, subnet %s\n",
				inst.Name, inst.Endpoint, inst.ListenPort, inst.Preset, inst.I1.Profile, inst.Subnet4)
			fmt.Fprintf(cmd.OutOrStdout(), "add a client: awgx peer add NAME --instance %s\n", inst.Name)
			return nil
		},
	}
	g.register(cmd)
	cmd.Flags().BoolVar(&skipSelfTest, "skip-selftest", false, "skip the netns handshake test")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func instanceRmCmd(app *core.App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm NAME",
		Short: "Stop an instance and delete it with its peers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			s, err := app.Store()
			if err != nil {
				return err
			}
			peers, err := s.Peers(name)
			if err != nil {
				return err
			}
			if !confirm(yes, fmt.Sprintf("stop %s and delete it with %d peers?", name, len(peers))) {
				return fmt.Errorf("aborted")
			}
			ctx := cmd.Context()
			if err := app.Locked(func() error { return app.RemoveInstance(ctx, name) }); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

type instanceRow struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Preset   string `json:"preset"`
	I1       string `json:"i1"`
	Subnet4  string `json:"subnet4"`
	Subnet6  string `json:"subnet6"`
	Peers    int    `json:"peers"`
	Up       bool   `json:"up"`
	Endpoint string `json:"endpoint"`
	Rate     int    `json:"rate"`
}

func instanceLsCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.Installed() {
				return core.ErrNotInstalled
			}
			s, err := app.Store()
			if err != nil {
				return err
			}
			insts, err := s.Instances()
			if err != nil {
				return err
			}
			var rows []instanceRow
			for _, inst := range insts {
				peers, err := s.Peers(inst.Name)
				if err != nil {
					return err
				}
				rows = append(rows, instanceRow{Name: inst.Name, Port: inst.ListenPort, Preset: inst.Preset, I1: inst.I1.Profile,
					Subnet4: inst.Subnet4.String(), Subnet6: state.OrEmpty(inst.Subnet6), Peers: len(peers), Up: app.InterfaceUp(inst.Name), Endpoint: inst.Endpoint, Rate: app.Rate(inst.Name)})
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), rows)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPORT\tPRESET\tI1\tSUBNET\tPEERS\tRATE\tSTATUS")
			for _, r := range rows {
				status := "down"
				if r.Up {
					status = "up"
				}
				fmt.Fprintf(w, "%s\t%d/udp\t%s\t%s\t%s\t%d\t%s\t%s\n", r.Name, r.Port, r.Preset, r.I1, r.Subnet4, r.Peers, fmtx.Rate(r.Rate), status)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func instanceSetCmd(app *core.App) *cobra.Command {
	var endpoint, ipv6, mtu, rate string
	cmd := &cobra.Command{
		Use:   "set NAME [--endpoint ADDRESS] [--ipv6 on|off] [--mtu N|auto] [--rate MBIT|off]",
		Short: "Change the public address, IPv6 inside the tunnel, the MTU or the rate limit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if endpoint == "" && ipv6 == "" && mtu == "" && rate == "" {
				return fmt.Errorf("pass --endpoint, --ipv6, --mtu or --rate")
			}
			rateValue, err := parseRate(rate)
			if err != nil {
				return err
			}
			mtuValue := 0
			if mtu != "" && mtu != "auto" {
				n, err := strconv.Atoi(mtu)
				if err != nil || n <= 0 {
					return fmt.Errorf("--mtu takes a number or auto")
				}
				mtuValue = n
			}
			if ipv6 != "" && ipv6 != "on" && ipv6 != "off" {
				return fmt.Errorf("--ipv6 must be on or off")
			}
			if endpoint != "" {
				if err := app.Locked(func() error {
					inst, err := app.SetEndpoint(args[0], endpoint)
					if err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s endpoint is %s:%d\n", inst.Name, inst.Endpoint, inst.ListenPort)
					return nil
				}); err != nil {
					return err
				}
			}
			if mtu != "" {
				if err := app.Locked(func() error {
					inst, err := app.SetMTU(cmd.Context(), args[0], mtuValue)
					if err != nil {
						return err
					}
					mode := "fixed"
					if mtuValue == 0 {
						mode = "automatic"
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s MTU is %d (%s)\n", inst.Name, inst.MTU, mode)
					return nil
				}); err != nil {
					return err
				}
			}
			if rate != "" {
				if err := app.Locked(func() error { return app.SetRate(cmd.Context(), args[0], rateValue) }); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s rate limit: %s\n", args[0], fmtx.Rate(rateValue))
				if endpoint == "" && ipv6 == "" && mtu == "" {
					return nil
				}
			}
			if ipv6 != "" {
				var inst state.Instance
				err := app.Locked(func() (err error) {
					inst, err = app.SetIPv6(cmd.Context(), args[0], ipv6 == "on")
					return err
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s IPv6 inside the tunnel: %s\n", inst.Name, ipv6)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "re-export the peer configs")
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "IP address or domain name")
	cmd.Flags().StringVar(&ipv6, "ipv6", "", "IPv6 inside the tunnel: on or off")
	cmd.Flags().StringVar(&mtu, "mtu", "", "tunnel MTU for the server and client configs, or auto to compute it from the link")
	cmd.Flags().StringVar(&rate, "rate", "", "limit traffic sent to peers, Mbit/s (CAKE), or off")
	return cmd
}

func parseRate(s string) (int, error) {
	if s == "" || s == "off" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("--rate takes Mbit/s or off")
	}
	return n, core.ValidRate(n)
}
