package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/spf13/cobra"
)

func doctorCmd(app *core.App) *cobra.Command {
	var fix, yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the system; exit code 0 ok, 1 warnings, 2 failures",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			env := app.DoctorEnv()
			results := doctor.Run(ctx, env)
			if fix {
				if applyFixes(ctx, cmd.ErrOrStderr(), results, yes) {
					results = doctor.Run(ctx, env)
				}
			}
			if asJSON {
				if err := printJSON(cmd.OutOrStdout(), results); err != nil {
					return err
				}
			} else {
				printResults(cmd.OutOrStdout(), results)
			}
			if code := doctor.ExitCode(results); code != 0 {
				return exitError{code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "offer automatic fixes")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "apply fixes without asking")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func printResults(w io.Writer, results []doctor.Result) {
	for _, r := range results {
		fmt.Fprintf(w, "%-4s  %-22s %s\n", r.Status, r.ID, r.Message)
		if r.Fix != nil && r.Status != doctor.OK {
			fmt.Fprintf(w, "      %-18s fix: %s (awgx doctor --fix)\n", "", r.Fix.Description)
		}
	}
}

func applyFixes(ctx context.Context, w io.Writer, results []doctor.Result, yes bool) bool {
	var ids []string
	for _, r := range results {
		if r.Fix != nil && r.Status != doctor.OK && confirm(yes, fmt.Sprintf("%s: %s. Fix: %s?", r.ID, r.Message, r.Fix.Description)) {
			ids = append(ids, r.ID)
		}
	}
	for _, f := range doctor.Apply(ctx, results, ids, func(l string) { fmt.Fprintln(w, l) }) {
		fmt.Fprintln(w, f)
	}
	return len(ids) > 0
}
