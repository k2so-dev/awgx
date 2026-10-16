package cli

import (
	"fmt"
	"os"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/setup"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func setupCmd(app *core.App) *cobra.Command {
	var yes, noFix, noPublic, asJSON, serve, removeOld, backupOld bool
	var scenario, endpoint, ipv6 string
	var clients []string
	cmd := &cobra.Command{
		Use:     "setup",
		Short:   "Setup: checks, fixes, install, first clients; on an installed server: maintenance",
		Example: "  awgx setup\n  awgx setup --yes --scenario stealth --client phone --client laptop",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !sysx.IsRoot() {
				return fmt.Errorf("setup needs root: run sudo awgx setup")
			}
			if !yes && !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("no terminal: run awgx setup --yes for the defaults")
			}
			ctx := cmd.Context()
			probe := setup.Inspect(ctx, app, !noPublic)
			plan := setup.Defaults(probe)
			plan.Serve = serve
			if noFix {
				plan.Fixes = nil
			}
			if plan.Install && probe.Container() {
				return setup.ErrContainer
			}
			if !plan.Install && probe.Instance == nil {
				if _, err := app.Current(); err != nil {
					return err
				}
			}
			switch scenario {
			case setup.Standard, setup.Stealth, setup.Speed:
			default:
				return fmt.Errorf("--scenario takes standard, stealth or speed")
			}
			if !plan.Install && cmd.Flags().Changed("scenario") {
				return fmt.Errorf("awgx is installed: --scenario applies only to the first install, use awgx gen --preset")
			}
			if plan.Install {
				plan.Gen = setup.ScenarioOptions(scenario, probe)
			}
			if cmd.Flags().Changed("client") {
				plan.Clients = clients
			}
			if endpoint != "" {
				plan.Endpoint = endpoint
				if probe.Instance != nil && endpoint == probe.Instance.Endpoint {
					plan.Endpoint = ""
				}
			}
			switch ipv6 {
			case "auto":
			case "on", "off":
				on := ipv6 == "on"
				if plan.Install {
					plan.Gen.IPv6 = &on
				} else if probe.Instance != nil && on != probe.Instance.IPv6() {
					plan.IPv6 = &on
				}
			default:
				return fmt.Errorf("--ipv6 must be auto, on or off")
			}
			out, info := cmd.OutOrStdout(), cmd.OutOrStdout()
			if asJSON {
				info = cmd.ErrOrStderr()
			}
			if probe.PublicErr != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: public address lookup failed: "+probe.PublicErr)
			}
			if removeOld {
				plan.Legacy = probe.Legacy
				plan.BackupLegacy = backupOld
			} else if len(probe.Legacy) > 0 {
				for _, l := range probe.Legacy {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: old setup %s %s found; --remove-old removes it\n", l.Kind, l.Name)
				}
			}
			if err := plan.Validate(); err != nil {
				return err
			}
			lines := plan.Summary(probe)
			if len(lines) == 0 {
				fmt.Fprintln(info, "nothing to change")
				if asJSON {
					return printJSON(out, map[string]bool{"changed": false})
				}
				return nil
			}
			for _, l := range lines {
				fmt.Fprintln(info, "+ "+l)
			}
			if !confirm(yes, "apply?") {
				return fmt.Errorf("aborted")
			}
			rep, err := setup.Run(ctx, app, plan)
			printIssues(cmd.ErrOrStderr(), rep.Issues)
			if err != nil {
				for _, f := range rep.ClientFiles {
					fmt.Fprintln(cmd.ErrOrStderr(), "client config: "+f)
				}
				for _, n := range rep.Notes {
					fmt.Fprintln(cmd.ErrOrStderr(), n)
				}
				return err
			}
			if asJSON {
				return printJSON(out, rep)
			}
			inst := rep.Instance
			fmt.Fprintf(out, "%s: %s:%d/udp, preset %s, I1 %s\n", inst.Name, inst.Endpoint, inst.ListenPort, inst.Preset, inst.I1.Profile)
			for _, f := range rep.ClientFiles {
				fmt.Fprintln(out, "client config: "+f)
			}
			for _, n := range rep.Notes {
				fmt.Fprintln(out, n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before applying the plan")
	cmd.Flags().StringVar(&scenario, "scenario", setup.Standard, "standard, stealth or speed (first install)")
	cmd.Flags().StringSliceVar(&clients, "client", nil, "clients to create (default phone on the first install)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "address clients connect to (default: the public IPv4)")
	cmd.Flags().StringVar(&ipv6, "ipv6", "auto", "IPv6 inside the tunnel: auto (on if the server has an IPv6 route), on, off")
	cmd.Flags().BoolVar(&noFix, "no-fix", false, "do not apply doctor fixes")
	cmd.Flags().BoolVar(&noPublic, "no-public-ip", false, "do not ask api.ipify.org for the public address")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().BoolVar(&serve, "serve", false, "enable awgx-serve: traffic stats, client expiry, egress failover, update check")
	cmd.Flags().BoolVar(&removeOld, "remove-old", false, "remove old WireGuard/AmneziaWG setups")
	cmd.Flags().BoolVar(&backupOld, "backup-old", false, "archive old setups into /var/lib/awgx/backups before removing them")
	return cmd
}
