package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/spf13/cobra"
)

var Version = "dev"

func Execute() int {
	app := core.New()
	err := New(app).Execute()
	app.Close()
	if err == nil {
		return 0
	}
	var e exitError
	if errors.As(err, &e) {
		return e.code
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

func New(app *core.App) *cobra.Command {
	app.Version = Version
	app.Source = source()
	root := &cobra.Command{
		Use:           "awgx",
		Short:         "Install, configure and maintain an AmneziaWG 3.1 server on the kernel module",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&app.Selected, "instance", "i", "", "instance to work with (default: "+core.DefaultInstance+" or the only one)")
	root.AddCommand(
		versionCmd(),
		setupCmd(app),
		instanceCmd(app),
		doctorCmd(app),
		installCmd(app),
		purgeCmd(app),
		genCmd(app),
		i1Cmd(app),
		peerCmd(app),
		dataplaneCmd(app),
		statsCmd(app),
		serveCmd(app),
		backupCmd(app),
		restoreCmd(app),
		reinstallCmd(app),
		selftestCmd(app),
		hookCmd(app),
		egressCmd(app),
		uplinkCmd(app),
		xuiCmd(app),
		perfCmd(app),
		webCmd(app),
		upgradeCmd(app),
	)
	strictArgs(root)
	return root
}

func strictArgs(c *cobra.Command) {
	if c.Runnable() && c.Args == nil {
		c.Args = cobra.NoArgs
	}
	for _, sub := range c.Commands() {
		strictArgs(sub)
	}
}

func source() string {
	if s := os.Getenv("AWGX_SOURCE"); s != "" {
		return s
	}
	if os.Getenv("INVOCATION_ID") != "" {
		return "system"
	}
	return "cli"
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the awgx version",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println("awgx " + Version)
		},
	}
}
