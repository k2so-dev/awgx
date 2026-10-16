package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/install"
	"github.com/k2so-dev/awgx/internal/purge"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/webconf"
	"github.com/spf13/cobra"
)

func backupCmd(app *core.App) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Archive the state and configs into a tar.zst file (mode 0600)",
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := app.Backup(out)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), name)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "directory for the archive (default /var/lib/awgx/backups)")
	return cmd
}

func restoreCmd(app *core.App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore FILE",
		Short: "Replace the state and configs with a backup and restart the instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !app.Installed() {
				return fmt.Errorf("awgx is not installed: use awgx reinstall --from %s or install first", args[0])
			}
			if !confirm(yes, "replace the current state with "+args[0]+"?") {
				return fmt.Errorf("aborted")
			}
			if err := app.Restore(context.WithoutCancel(cmd.Context()), args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "restored")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func reinstallCmd(app *core.App) *cobra.Command {
	var g genFlags
	var from string
	var regen, yes, removeOld, backupOld bool
	cmd := &cobra.Command{
		Use:   "reinstall",
		Short: "purge + install keeping peers and their keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			opts, err := g.options()
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("preset") {
				opts.Preset = ""
			}
			if from == "" && !app.Installed() {
				return fmt.Errorf("nothing to reinstall: pass --from BACKUP")
			}
			serve := app.ServeEnabled(ctx)
			if c, err := webconf.LoadConfig(app); err == nil && c.Enabled {
				serve = true
			}
			temp := ""
			if app.Installed() {
				q := "reinstall: remove everything and install again keeping peers?"
				if regen {
					q = "reinstall with new server-side parameters: every client will need a new config. Continue?"
				}
				if !confirm(yes, q) {
					return fmt.Errorf("aborted")
				}
				res, err := purge.Run(ctx, app, purge.Options{Backup: from == "", KeepBinary: true, KeepInbound: true})
				if err != nil {
					return err
				}
				if from == "" {
					from, temp = res.Backup, res.Backup
				}
			}
			o := install.Options{
				Gen:         opts,
				Restore:     from,
				RegenServer: regen,
				Serve:       serve,
				Confirm:     func(q string) bool { return removeOld || confirm(yes, q) },
			}
			if removeOld || !yes {
				o.PurgeLegacy = func(ctx context.Context, items []doctor.LegacyItem) error {
					return purge.Legacy(ctx, app, items, backupOld)
				}
			}
			res, err := install.Run(ctx, app, o)
			printIssues(cmd.ErrOrStderr(), res.Issues)
			if err != nil {
				return fmt.Errorf("%w (backup kept at %s)", err, from)
			}
			if temp != "" {
				os.Remove(temp)
				os.Remove(app.Paths.BackupDir)
			}
			rows, _ := app.PeerRows(ctx, "")
			fmt.Fprintf(cmd.OutOrStdout(), "reinstalled %s in %s with %d peers\n", strings.Join(app.InstanceNames(), ", "), res.Duration, len(rows))
			if regen {
				fmt.Fprintln(cmd.OutOrStdout(), "server-side parameters changed: re-export every peer config")
			}
			return nil
		},
	}
	g.register(cmd)
	cmd.Flags().StringVar(&from, "from", "", "install from this backup instead of the current state")
	cmd.Flags().BoolVar(&regen, "regen-server", false, "regenerate server-side parameters")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&removeOld, "remove-old", false, "remove old WireGuard/AmneziaWG setups without asking")
	cmd.Flags().BoolVar(&backupOld, "backup-old", false, "archive old setups into /var/lib/awgx/backups before removing them")
	return cmd
}

func selftestCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "selftest",
		Short: "Handshake and ping from a temporary test peer in a network namespace",
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := app.Current()
			if err != nil {
				return err
			}
			if err := selftest.Run(cmd.Context(), app, inst); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "self-test passed")
			return nil
		},
	}
}
