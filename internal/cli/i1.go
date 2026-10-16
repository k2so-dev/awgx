package cli

import (
	"fmt"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/cps"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/spf13/cobra"
)

func i1Cmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "i1",
		Short: "Manage the I1 packet: passes first-packet classifiers, does not fool flow DPI",
	}
	cmd.AddCommand(i1ShowCmd(app), i1GenCmd(app), i1RotateCmd(app), i1ABCmd(app))
	return cmd
}

type i1Row struct {
	Target  string `json:"target"`
	Profile string `json:"profile"`
	Size    int    `json:"size"`
}

func i1ShowCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show the instance I1 profile and peer overrides",
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := app.Current()
			if err != nil {
				return err
			}
			store, _ := app.Store()
			peers, err := store.Peers(inst.Name)
			if err != nil {
				return err
			}
			size := func(s string) int {
				p, _ := cps.Parse(s)
				return p.Size()
			}
			rows := []i1Row{{Target: "instance " + inst.Name, Profile: inst.I1.Profile, Size: size(inst.Obfuscation.I[0])}}
			for _, p := range peers {
				if p.I1 != nil {
					rows = append(rows, i1Row{Target: "peer " + p.Name, Profile: p.I1.Profile, Size: size(p.I1CPS)})
				}
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), map[string]any{"rows": rows, "rotated_at": inst.I1RotatedAt})
			}
			for _, r := range rows {
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s %-8s %5d bytes\n", r.Target, r.Profile, r.Size)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rotated %s\n", fmtx.Ago(inst.I1RotatedAt))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func i1GenCmd(app *core.App) *cobra.Command {
	var f i1Flags
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Set the instance I1 profile (client-side only, the server is not touched)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := f.settings()
			if err != nil {
				return err
			}
			if s == nil {
				return fmt.Errorf("--profile is required")
			}
			unlock, err := app.Lock()
			if err != nil {
				return err
			}
			defer unlock()
			cur, err := app.Current()
			if err != nil {
				return err
			}
			inst, is, err := app.SetInstanceI1(cur.Name, *s)
			printIssues(cmd.ErrOrStderr(), is)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "I1 profile %s set on %s; re-export configs of peers without an override\n", inst.I1.Profile, inst.Name)
			return nil
		},
	}
	f.register(cmd, "profile")
	return cmd
}

func i1RotateCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Regenerate static I1 packets (quic, stun, dns, custom); old configs keep working",
		RunE: func(cmd *cobra.Command, args []string) error {
			unlock, err := app.Lock()
			if err != nil {
				return err
			}
			defer unlock()
			cur, err := app.Current()
			if err != nil {
				return err
			}
			affected, err := app.RotateI1(cur.Name)
			if err != nil {
				return err
			}
			if len(affected) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "nothing to rotate: no static I1 profiles in use")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rotated; re-export configs for: %s\n", strings.Join(affected, ", "))
			return nil
		},
	}
}

func i1ABCmd(app *core.App) *cobra.Command {
	var peers, profiles []string
	var sni string
	cmd := &cobra.Command{
		Use:   "ab",
		Short: "Assign I1 profiles to peers round-robin for an A/B comparison",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(peers) == 0 || len(profiles) == 0 {
				return fmt.Errorf("--peers and --profiles are required")
			}
			for _, p := range profiles {
				if !i1.Valid(p) || p == i1.Custom {
					return fmt.Errorf("profile %q cannot be used in A/B", p)
				}
			}
			settings := make([]*i1.Settings, len(peers))
			for n := range peers {
				settings[n] = &i1.Settings{Profile: profiles[n%len(profiles)], SNI: strings.TrimSpace(sni)}
			}
			return app.Locked(func() error {
				done, is, err := app.SetPeersI1(peers, settings)
				printIssues(cmd.ErrOrStderr(), is)
				if err != nil {
					return err
				}
				for _, p := range done {
					fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s\n", p.Name, p.I1.Profile)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "re-export configs for these peers; comparison needs awgx serve statistics")
				return nil
			})
		},
	}
	cmd.Flags().StringSliceVar(&peers, "peers", nil, "peer names")
	cmd.Flags().StringSliceVar(&profiles, "profiles", nil, "profiles to rotate through")
	cmd.Flags().StringVar(&sni, "sni", "", "SNI for the quic profile (default: the peer or instance SNI)")
	return cmd
}
