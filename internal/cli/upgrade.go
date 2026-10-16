package cli

import (
	"context"
	"fmt"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/upgrade"
	"github.com/spf13/cobra"
)

func upgradeCmd(app *core.App) *cobra.Command {
	var self, check, asJSON bool
	var channel, autoCheck string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the module and tools with automatic rollback, or awgx itself with --self",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			if autoCheck != "" {
				if autoCheck != "on" && autoCheck != "off" {
					return fmt.Errorf("--auto-check takes on or off")
				}
				return upgrade.SetAutoCheck(app, autoCheck == "on")
			}
			if check {
				tag, err := upgrade.CheckUpdate(ctx, app)
				if err != nil {
					return err
				}
				if tag == "" {
					fmt.Fprintln(out, "awgx is up to date")
				} else {
					fmt.Fprintf(out, "awgx %s is available: awgx upgrade --self\n", tag)
				}
				return nil
			}
			if self {
				tag, err := upgrade.SelfUpdate(ctx, app, channel)
				if err != nil {
					return err
				}
				if tag == "" {
					fmt.Fprintln(out, "awgx is up to date")
					return nil
				}
				if _, err := app.Run.Run(ctx, "systemctl", "try-restart", core.DataplaneUnit); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: restart %s: %v\n", core.DataplaneUnit, err)
				}
				app.RestartServe(ctx)
				fmt.Fprintf(out, "awgx upgraded to %s\n", tag)
				return nil
			}
			unlock, err := app.Lock()
			if err != nil {
				return err
			}
			defer unlock()
			res, err := upgrade.Modules(ctx, app)
			if asJSON {
				printJSON(out, res)
			}
			if err != nil {
				return err
			}
			if !asJSON {
				for pkg, v := range res.After {
					if res.Before[pkg] != v {
						fmt.Fprintf(out, "%s %s -> %s\n", pkg, res.Before[pkg], v)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&self, "self", false, "upgrade the awgx binary from GitHub Releases (signature verified)")
	cmd.Flags().StringVar(&channel, "channel", "", "release channel: stable or beta (default beta on a pre-release build, stable otherwise)")
	cmd.Flags().BoolVar(&check, "check", false, "only check whether a newer awgx exists")
	cmd.Flags().StringVar(&autoCheck, "auto-check", "", "daily update check in awgx serve: on or off")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func updateChecker(app *core.App) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if store, err := app.Store(); err == nil {
			if v, _ := store.Setting(core.SettingUpdateCheck); v == "off" {
				return "", store.SetSetting(state.SettingUpdate, "")
			}
		}
		return upgrade.CheckUpdate(ctx, app)
	}
}
