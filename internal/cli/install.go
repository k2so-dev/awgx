package cli

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/install"
	"github.com/k2so-dev/awgx/internal/purge"
	"github.com/spf13/cobra"
)

type genFlags struct {
	preset   string
	port     int
	endpoint string
	dns      []string
	ipv6     string
	i1       i1Flags
	toggles  toggleFlags
}

func (f *genFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.preset, "preset", "balanced", "parameter preset: balanced, stealth, speed")
	cmd.Flags().IntVar(&f.port, "port", 0, "UDP listen port (random high port by default)")
	cmd.Flags().StringVar(&f.endpoint, "endpoint", "", "public address clients connect to (detected by default)")
	cmd.Flags().StringSliceVar(&f.dns, "dns", nil, "DNS servers for clients (default 1.1.1.1,1.0.0.1)")
	cmd.Flags().StringVar(&f.ipv6, "ipv6", "auto", "IPv6 inside the tunnel: auto (on if the server has an IPv6 route), on, off")
	f.i1.register(cmd, "i1")
	f.toggles.register(cmd)
}

func (f *genFlags) options() (core.GenOptions, error) {
	o := core.GenOptions{Preset: f.preset, Port: f.port, Endpoint: f.endpoint, Toggles: f.toggles.get()}
	switch f.ipv6 {
	case "", "auto":
	case "on", "off":
		on := f.ipv6 == "on"
		o.IPv6 = &on
	default:
		return o, fmt.Errorf("--ipv6 must be auto, on or off")
	}
	for _, d := range f.dns {
		a, err := netip.ParseAddr(strings.TrimSpace(d))
		if err != nil {
			return o, fmt.Errorf("invalid DNS %q", d)
		}
		o.DNS = append(o.DNS, a)
	}
	s, err := f.i1.settings()
	if err != nil {
		return o, err
	}
	if s != nil {
		o.I1 = *s
	}
	return o, nil
}

func installCmd(app *core.App) *cobra.Command {
	var g genFlags
	var force, yes, skipSelfTest, asJSON, serve, removeOld, backupOld bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the module and tools, generate the instance and start it",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := g.options()
			if err != nil {
				return err
			}
			o := install.Options{
				Gen:          opts,
				Serve:        serve,
				Force:        force,
				SkipSelfTest: skipSelfTest,
				Confirm:      func(q string) bool { return removeOld || confirm(yes, q) },
			}
			if removeOld || !yes {
				o.PurgeLegacy = func(ctx context.Context, items []doctor.LegacyItem) error {
					return purge.Legacy(ctx, app, items, backupOld)
				}
			}
			res, err := install.Run(cmd.Context(), app, o)
			printIssues(cmd.ErrOrStderr(), res.Issues)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), res)
			}
			inst := res.Instance
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s in %s: %s:%d, preset %s, I1 %s\n",
				inst.Name, res.Duration, inst.Endpoint, inst.ListenPort, inst.Preset, inst.I1.Profile)
			fmt.Fprintln(cmd.OutOrStdout(), "add a client: awgx peer add NAME")
			return nil
		},
	}
	g.register(cmd)
	cmd.Flags().BoolVar(&force, "force", false, "continue even if preflight checks fail")
	cmd.Flags().BoolVar(&removeOld, "remove-old", false, "remove old WireGuard/AmneziaWG setups without asking")
	cmd.Flags().BoolVar(&backupOld, "backup-old", false, "archive old setups into /var/lib/awgx/backups before removing them")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "answer yes to all questions")
	cmd.Flags().BoolVar(&skipSelfTest, "skip-selftest", false, "skip the netns handshake test")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().BoolVar(&serve, "serve", false, "enable awgx-serve: traffic stats, client expiry, egress failover, update check")
	return cmd
}

func purgeCmd(app *core.App) *cobra.Command {
	var withBackup, legacy, yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Remove everything awgx installed, including itself",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if legacy {
				items := doctor.Legacy(ctx, app.DoctorEnv())
				if len(items) == 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "no legacy setups found")
				}
				for _, i := range items {
					if confirm(yes, fmt.Sprintf("remove %s %s?", i.Kind, i.Name)) {
						if err := purge.Legacy(ctx, app, []doctor.LegacyItem{i}, withBackup); err != nil {
							return err
						}
					}
				}
				if !app.Installed() {
					return nil
				}
			}
			if !confirm(yes, "remove awgx, its instance, peers, packages and itself?") {
				return fmt.Errorf("aborted")
			}
			res, err := purge.Run(ctx, app, purge.Options{Backup: withBackup})
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), res)
			}
			for _, r := range res.Removed {
				fmt.Fprintln(cmd.OutOrStdout(), "removed", r)
			}
			for _, k := range res.Kept {
				fmt.Fprintln(cmd.OutOrStdout(), "kept", k)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withBackup, "backup", false, "keep backup archives of the state and removed old setups in /var/lib/awgx/backups")
	cmd.Flags().BoolVar(&legacy, "legacy", false, "also remove old WireGuard/AmneziaWG setups (asks per item)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}
