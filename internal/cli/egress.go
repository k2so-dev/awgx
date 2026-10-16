package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/spf13/cobra"
)

func egressCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{Use: "egress", Short: "Route peer traffic through other interfaces or SOCKS5 with plan/apply"}
	cmd.AddCommand(egressLsCmd(app), egressAddCmd(app), egressSetCmd(app), egressRmCmd(app), egressUseCmd(app),
		egressPlanCmd(app), egressApplyCmd(app), egressRollbackCmd(app), egressResetCmd(app),
		egressStatusCmd(app), egressProbeCmd())
	return cmd
}

func egressLsCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List egress profiles (draft)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := egress.New(app)
			if err != nil {
				return err
			}
			draft, err := e.Store.Draft()
			if err != nil {
				return err
			}
			if asJSON {
				for i, p := range draft.Profiles {
					if p.Socks != nil && p.Socks.Password != "" {
						masked := *p.Socks
						masked.Password = "***"
						draft.Profiles[i].Socks = &masked
					}
				}
				return printJSON(cmd.OutOrStdout(), draft)
			}
			health := e.Health()
			applied, err := e.Store.Applied()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tTARGET\tFALLBACK\tTABLE\tHEALTH")
			for _, p := range draft.Profiles {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", p.Name, p.Kind, p.Target(), orDash(p.Fallback), p.Table, healthText(applied.Config, health, p))
			}
			w.Flush()
			for inst, a := range draft.Default {
				fmt.Fprintf(cmd.OutOrStdout(), "\n%s default: %s%s\n", inst, a.Profile, ks(a.Killswitch))
			}
			for peer, a := range draft.Peers {
				fmt.Fprintf(cmd.OutOrStdout(), "peer %s: %s%s\n", peer, orDash(a.Profile), ks(a.Killswitch))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func ks(on bool) string {
	if on {
		return " (killswitch)"
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func healthText(applied state.EgressConfig, health map[string]egress.Health, p state.EgressProfile) string {
	h, dev := egress.ProfileHealth(applied, health, p.Name)
	if dev != "" {
		return h + ": " + dev
	}
	return h
}

type profileFlags struct {
	kind, server, user, password, via, fallback string
	devs                                        []string
	noUDP, dnsHijack, passwordStdin             bool
}

func (f *profileFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.kind, "type", "", "iface or socks5")
	cmd.Flags().StringSliceVar(&f.devs, "dev", nil, "interfaces in failover order (iface)")
	cmd.Flags().StringVar(&f.via, "via", "", "gateway for the first interface (iface); empty removes it")
	cmd.Flags().StringVar(&f.server, "server", "", "SOCKS5 server IP:PORT (socks5)")
	cmd.Flags().StringVar(&f.user, "user", "", "SOCKS5 user")
	cmd.Flags().StringVar(&f.password, "password", "", "SOCKS5 password; visible in the process list, prefer --password-stdin")
	cmd.Flags().BoolVar(&f.passwordStdin, "password-stdin", false, "read the SOCKS5 password from stdin or a prompt")
	cmd.Flags().BoolVar(&f.noUDP, "no-udp", false, "SOCKS5 server has no UDP ASSOCIATE; UDP of peers will not work")
	cmd.Flags().StringVar(&f.fallback, "fallback", "", "profile, direct, or drop (default) when this one is down")
	cmd.Flags().BoolVar(&f.dnsHijack, "dns-hijack", false, "redirect DNS of peers to the configured resolver through this egress")
}

func (f *profileFlags) apply(cmd *cobra.Command, p *state.EgressProfile) error {
	ch := cmd.Flags().Changed
	if p.Kind == "xui" {
		for _, name := range []string{"type", "dev", "via", "server", "user", "password", "password-stdin", "no-udp"} {
			if ch(name) {
				return fmt.Errorf("profile %s is managed by awgx xui: only --fallback and --dns-hijack can change", p.Name)
			}
		}
	}
	if f.passwordStdin {
		pass, err := readSecret(cmd, "SOCKS5 password: ")
		if err != nil {
			return err
		}
		f.password = pass
		cmd.Flags().Set("password", pass)
	}
	if ch("type") {
		if err := egress.Retype(p, f.kind); err != nil {
			return err
		}
	}
	other := map[string][]string{"iface": {"server", "user", "password", "no-udp"}, "socks5": {"dev", "via"}}
	for _, name := range other[p.Kind] {
		if ch(name) {
			return fmt.Errorf("--%s does not apply to a %s profile", name, p.Kind)
		}
	}
	if ch("dev") || ch("via") {
		devs := f.devs
		if !ch("dev") {
			for _, h := range p.Hops {
				devs = append(devs, h.Dev)
			}
		}
		p.Hops = egress.Hops(devs, p.Hops)
		if ch("via") {
			var via netip.Addr
			if f.via != "" {
				var err error
				if via, err = netip.ParseAddr(f.via); err != nil {
					return fmt.Errorf("invalid --via: %w", err)
				}
			}
			if len(p.Hops) == 0 {
				return fmt.Errorf("--via needs --dev")
			}
			p.Hops[0].Via = via
		}
	}
	if ch("server") || ch("user") || ch("password") || ch("no-udp") {
		if p.Socks == nil {
			p.Socks = &state.Socks{}
		}
		if ch("server") {
			p.Socks.Address = f.server
		}
		if ch("user") {
			p.Socks.User = f.user
			if f.user == "" {
				p.Socks.Password = ""
			}
		}
		if ch("password") {
			p.Socks.Password = f.password
		}
		if ch("no-udp") {
			p.Socks.NoUDP = f.noUDP
		}
	}
	if ch("fallback") {
		p.Fallback = f.fallback
		if p.Fallback == "drop" {
			p.Fallback = ""
		}
	}
	if ch("dns-hijack") {
		p.DNSHijack = f.dnsHijack
	}
	return nil
}

func egressAddCmd(app *core.App) *cobra.Command {
	var f profileFlags
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add an egress profile to the draft",
		Example: "  awgx egress add up --type iface --dev awg1,awg2 --fallback direct\n" +
			"  awgx egress add proxy --type socks5 --server 127.0.0.1:1080 --user u --password p --dns-hijack",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := state.EgressProfile{Name: args[0]}
			if err := f.apply(cmd, &p); err != nil {
				return err
			}
			return egress.Locked(app, func(e *egress.Engine) error {
				p, err := e.AddProfile(cmd.Context(), p)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "profile %s added (table %d); assign it with: awgx egress use %s\n", p.Name, p.Table, p.Name)
				return nil
			})
		},
	}
	f.register(cmd)
	cmd.MarkFlagRequired("type")
	return cmd
}

func egressSetCmd(app *core.App) *cobra.Command {
	var f profileFlags
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Change an egress profile in the draft",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return egress.Locked(app, func(e *egress.Engine) error {
				_, err := e.UpdateProfile(cmd.Context(), args[0], func(p *state.EgressProfile) error { return f.apply(cmd, p) })
				if err == nil {
					fmt.Fprintln(cmd.OutOrStdout(), "draft changed, review with: awgx egress plan")
				}
				return err
			})
		},
	}
	f.register(cmd)
	return cmd
}

func egressRmCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Remove an egress profile from the draft",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return egress.Locked(app, func(e *egress.Engine) error { return e.RemoveProfile(args[0]) })
		},
	}
}

func egressUseCmd(app *core.App) *cobra.Command {
	var peers []string
	var killswitch, inherit bool
	cmd := &cobra.Command{
		Use:   "use PROFILE|direct",
		Short: "Assign an egress to the whole instance or to --peer (draft)",
		Example: "  awgx egress use up --killswitch\n  awgx egress use proxy --peer alice,bob\n" +
			"  awgx egress use direct --peer carol\n  awgx egress use --inherit --peer carol",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a := state.Assign{Killswitch: killswitch}
			if len(args) == 1 && args[0] == "inherit" {
				inherit, args = true, nil
			}
			switch {
			case inherit && len(peers) > 0 && len(args) == 0:
			case !inherit && len(args) == 1:
				a.Profile = args[0]
			default:
				return fmt.Errorf("pass a profile, or --inherit with --peer")
			}
			var instance string
			if len(peers) == 0 {
				inst, err := app.Current()
				if err != nil {
					return err
				}
				instance = inst.Name
			}
			return egress.Locked(app, func(e *egress.Engine) error {
				if err := e.Assign(instance, peers, a); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "draft changed, review with: awgx egress plan, then: awgx egress apply")
				return nil
			})
		},
	}
	cmd.Flags().StringSliceVar(&peers, "peer", nil, "peers to assign (default: whole instance)")
	cmd.Flags().BoolVar(&killswitch, "killswitch", false, "drop traffic instead of going direct when the egress is down")
	cmd.Flags().BoolVar(&inherit, "inherit", false, "peers follow the instance default again")
	return cmd
}

func printPlan(w io.Writer, p egress.Plan) {
	switch {
	case !p.Changed:
		fmt.Fprintln(w, "no changes")
	case len(p.Diff) == 0:
		fmt.Fprintln(w, "profile settings changed; the rules stay the same")
	}
	for _, l := range p.Diff {
		fmt.Fprintln(w, "  "+l)
	}
	for _, s := range p.Warnings {
		fmt.Fprintln(w, "warning: "+s)
	}
	for _, s := range p.Issues {
		fmt.Fprintln(w, "error: "+s)
	}
}

func egressPlanCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what apply would change; changes nothing",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := egress.New(app)
			if err != nil {
				return err
			}
			p, err := e.Plan(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), p)
			}
			printPlan(cmd.OutOrStdout(), p)
			if len(p.Issues) > 0 {
				return exitError{2}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func egressApplyCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply the draft with pre/post-checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(cmd, app, asJSON, true, func(ctx context.Context, e *egress.Engine, o egress.ApplyOptions) (egress.Result, error) {
				res, _, err := e.ApplyOrConverge(ctx, o)
				return res, err
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func runApply(cmd *cobra.Command, app *core.App, asJSON, plan bool,
	fn func(ctx context.Context, e *egress.Engine, o egress.ApplyOptions) (egress.Result, error)) error {
	out := cmd.OutOrStdout()
	o := egress.ApplyOptions{SSH: sysx.CurrentSSH()}
	var res egress.Result
	err := egress.Locked(app, func(e *egress.Engine) error {
		if plan && !asJSON {
			p, err := e.Plan(cmd.Context())
			if err != nil {
				return err
			}
			printPlan(out, p)
		}
		var err error
		res, err = fn(cmd.Context(), e, o)
		return err
	})
	if asJSON {
		if err == nil && res.Snapshot == 0 {
			return printJSON(out, map[string]bool{"changed": false})
		}
		printJSON(out, res)
		if err != nil {
			return exitError{1}
		}
		return nil
	}
	for _, c := range res.Checks {
		fmt.Fprintln(out, c)
	}
	if errors.Is(err, egress.ErrPostCheck) {
		fmt.Fprintf(out, "applied snapshot %d\n", res.Snapshot)
	}
	if err != nil {
		return err
	}
	if res.Snapshot == 0 {
		fmt.Fprintln(out, "no changes; the applied egress was converged again")
		return nil
	}
	fmt.Fprintf(out, "applied snapshot %d\n", res.Snapshot)
	return nil
}

func egressResetCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Discard the draft: copy the applied egress back into it",
		RunE: func(cmd *cobra.Command, args []string) error {
			return egress.Locked(app, func(e *egress.Engine) error {
				if err := e.ResetDraft(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "draft reset to the applied egress")
				return nil
			})
		},
	}
}

func egressRollbackCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback [SNAPSHOT]",
		Short: "Re-apply an earlier snapshot, by default the one before the applied snapshot",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var id int64
			if len(args) > 0 {
				var err error
				if id, err = strconv.ParseInt(args[0], 10, 64); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			return runApply(cmd, app, false, false, func(ctx context.Context, e *egress.Engine, o egress.ApplyOptions) (egress.Result, error) {
				if p, err := e.Plan(ctx); err == nil && p.Changed {
					fmt.Fprintln(out, "warning: the unapplied draft is replaced by the snapshot")
				}
				res, err := e.RollbackTo(ctx, id, o)
				for _, l := range res.Diff {
					fmt.Fprintln(out, "  "+l)
				}
				return res, err
			})
		},
	}
}

func egressStatusCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show applied snapshot, health and history",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := egress.New(app)
			if err != nil {
				return err
			}
			st, err := e.Status(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "applied snapshot: %d\n", st.Applied)
			for name, h := range st.Health {
				fmt.Fprintf(out, "health %s: %+v\n", name, h)
			}
			if len(st.Drift) > 0 {
				fmt.Fprintln(out, "live state differs from the applied snapshot (serve reconciles it):")
				for _, l := range st.Drift {
					fmt.Fprintln(out, "  "+l)
				}
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "\nID\tCREATED\tSTATUS\tCHANGES")
			for _, sn := range st.History {
				fmt.Fprintf(w, "%d\t%s\t%s\t%d\n", sn.ID, sn.CreatedAt.Local().Format("2006-01-02 15:04:05"), sn.Status, countLines(sn.Diff))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func egressProbeCmd() *cobra.Command {
	var tcp []string
	var dns string
	cmd := &cobra.Command{
		Use:    "probe",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return egress.ProbeCommand(cmd.Context(), tcp, dns)
		},
	}
	cmd.Flags().StringSliceVar(&tcp, "tcp", nil, "TCP targets")
	cmd.Flags().StringVar(&dns, "dns", "", "DNS server")
	return cmd
}
