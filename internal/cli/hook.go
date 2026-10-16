package cli

import (
	"fmt"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/spf13/cobra"
)

func hookCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{Use: "hook", Short: "System hooks installed by awgx", Hidden: true}
	cmd.AddCommand(&cobra.Command{
		Use:   "kernel-postinst VERSION",
		Short: "Record whether the module was built for a newly installed kernel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			built, err := app.KernelPostinst(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !built {
				fmt.Fprintf(cmd.ErrOrStderr(), "awgx: amneziawg module was not built for %s, the tunnel will not start after a reboot into it; see awgx doctor\n", args[0])
			}
			return nil
		},
	})
	return cmd
}
