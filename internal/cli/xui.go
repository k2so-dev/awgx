package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/xui"
	"github.com/spf13/cobra"
)

func xuiCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{Use: "xui", Short: "Send peer traffic into Xray of the local 3x-ui through a local SOCKS5 inbound"}
	cmd.AddCommand(xuiConnectCmd(app), xuiStatusCmd(app), xuiDisconnectCmd(app))
	return cmd
}

func xuiConnectCmd(app *core.App) *cobra.Command {
	var o xui.ConnectOptions
	var peers []string
	var yes, noKill, tokenStdin bool
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Create the awgx-egress inbound in 3x-ui and route peers into it (plan/apply)",
		Example: "  awgx xui connect --token TOKEN\n  awgx xui connect --peers alice,bob --token TOKEN\n" +
			"  awgx xui connect --user admin --password PASS   (3x-ui without API tokens)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(peers) == 1 && peers[0] == "all" {
				peers = nil
			}
			o.Peers, o.Killswitch = peers, !noKill
			if tokenStdin {
				t, err := readSecret(cmd, "3x-ui API token: ")
				if err != nil {
					return err
				}
				o.Auth.Token = strings.TrimSpace(t)
			}
			if o.Auth.User != "" && o.Auth.Password == "" {
				p, err := readSecret(cmd, "3x-ui password: ")
				if err != nil {
					return err
				}
				o.Auth.Password = p
			}
			s, err := app.Store()
			if err != nil {
				return err
			}
			if _, saved, _ := xui.Load(s); !saved && o.Auth.Token == "" && o.Auth.User == "" {
				if !confirm(yes, "no --token given: run x-ui setting -getApiToken? It regenerates the cli-fallback token (the old one stops working) and the new one has admin rights") {
					return fmt.Errorf("aborted; create a token in 3x-ui (Settings, Security, API Token) and pass --token")
				}
				t, err := xui.FallbackToken(cmd.Context(), app.Run)
				if err != nil {
					return err
				}
				o.Auth.Token = t
			}
			inst, err := app.Current()
			if err != nil {
				return err
			}
			o.Instance = inst.Name
			err = egress.Locked(app, func(e *egress.Engine) error {
				_, notes, err := xui.Connect(cmd.Context(), e, o)
				for _, n := range notes {
					fmt.Fprintln(cmd.OutOrStdout(), n)
				}
				return err
			})
			if err != nil {
				return err
			}
			if err := runApply(cmd, app, false, true, func(ctx context.Context, e *egress.Engine, ao egress.ApplyOptions) (egress.Result, error) {
				res, _, err := e.ApplyOrConverge(ctx, ao)
				return res, err
			}); err != nil {
				return err
			}
			e, err := egress.New(app)
			if err != nil {
				return err
			}
			st, err := xui.Check(cmd.Context(), e)
			switch {
			case err != nil:
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: connected, but the status check failed: %v\n", err)
			case st.Problem == "":
				fmt.Fprintf(cmd.OutOrStdout(), "inbound %d: up %s, down %s\n", st.InboundID, fmtx.Bytes(uint64(st.Up)), fmtx.Bytes(uint64(st.Down)))
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&peers, "peers", []string{"all"}, "all, or a comma separated list of peers")
	cmd.Flags().StringVar(&o.Panel, "panel", "", "panel URL with basePath (detected from x-ui by default)")
	cmd.Flags().StringVar(&o.Auth.Token, "token", "", "3x-ui API token (Settings, Security, API Token); visible in the process list, prefer --token-stdin")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the API token from stdin or a prompt")
	cmd.Flags().StringVar(&o.Auth.User, "user", "", "panel login, only for 3x-ui without API tokens")
	cmd.Flags().StringVar(&o.Auth.Password, "password", "", "panel password (prompted when omitted with --user)")
	cmd.Flags().StringVar(&o.Fallback, "fallback", "", "profile or direct to use when 3x-ui is down (default: drop, direct with --no-killswitch)")
	cmd.Flags().BoolVar(&noKill, "no-killswitch", false, "let traffic go direct when 3x-ui is down")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before using the fallback API token")
	return cmd
}

func xuiStatusCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check the inbound, its settings and traffic",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := egress.New(app)
			if err != nil {
				return err
			}
			st, err := xui.Check(cmd.Context(), e)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), st)
			}
			out := cmd.OutOrStdout()
			if !st.Connected {
				fmt.Fprintln(out, "not connected; run: awgx xui connect")
				return nil
			}
			fmt.Fprintf(out, "panel:    %s (3x-ui %s, running %v)\n", st.Panel, st.Version, st.Running)
			fmt.Fprintf(out, "inbound:  %d present %v, enabled %v, settings match %v\n", st.InboundID, st.Present, st.Enabled, st.Matches)
			fmt.Fprintf(out, "traffic:  up %s, down %s\n", fmtx.Bytes(uint64(st.Up)), fmtx.Bytes(uint64(st.Down)))
			applied, err := e.Store.Applied()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "egress:   applied %v, health %s\n", st.Applied, healthText(applied.Config, e.Health(), state.EgressProfile{Name: xui.ProfileName}))
			if st.Removing {
				fmt.Fprintln(out, "pending:  disconnect, finished after the detach is applied")
			}
			if st.Problem != "" {
				fmt.Fprintln(out, "problem:  "+st.Problem)
				return exitError{1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func xuiDisconnectCmd(app *core.App) *cobra.Command {
	var yes, keepInbound bool
	cmd := &cobra.Command{
		Use:   "disconnect",
		Short: "Move peers back to direct (plan/apply), then delete the inbound from 3x-ui",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := egress.New(app)
			if err != nil {
				return err
			}
			rec, ok, err := xui.Load(e.Store)
			if err != nil {
				return err
			}
			draft, err := e.Store.Draft()
			if err != nil {
				return err
			}
			applied, err := e.Store.Applied()
			if err != nil {
				return err
			}
			_, inDraft := draft.Profile(xui.ProfileName)
			_, inApplied := applied.Config.Profile(xui.ProfileName)
			if !ok && !inDraft && !inApplied {
				fmt.Fprintln(cmd.OutOrStdout(), "not connected")
				return nil
			}
			what := fmt.Sprintf("delete inbound %d (%s) from 3x-ui", rec.InboundID, xui.Tag)
			if !ok {
				what = "remove the xui profile"
			} else if keepInbound {
				what = fmt.Sprintf("keep inbound %d in 3x-ui and forget it", rec.InboundID)
			}
			if !confirm(yes, "move every route that uses xui to direct and "+what+"?") {
				return fmt.Errorf("aborted, nothing changed")
			}
			var detached bool
			if err := egress.Locked(app, func(e *egress.Engine) error {
				detached, err = xui.Disconnect(e, keepInbound)
				return err
			}); err != nil {
				return err
			}
			applied, err = e.Store.Applied()
			if err != nil {
				return err
			}
			if _, used := applied.Config.Profile(xui.ProfileName); used || detached {
				err := runApply(cmd, app, false, true, func(ctx context.Context, e *egress.Engine, o egress.ApplyOptions) (egress.Result, error) {
					return e.Apply(ctx, o)
				})
				reportXUI(cmd, e, rec.InboundID, keepInbound)
				return err
			}
			err = egress.Locked(app, func(e *egress.Engine) error { return xui.AfterApply(cmd.Context(), e) })
			reportXUI(cmd, e, rec.InboundID, keepInbound)
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask")
	cmd.Flags().BoolVar(&keepInbound, "keep-inbound", false, "leave the inbound in 3x-ui, only stop using it")
	return cmd
}

func reportXUI(cmd *cobra.Command, e *egress.Engine, inbound int, keep bool) {
	rec, ok, _ := xui.Load(e.Store)
	switch {
	case !ok && inbound != 0:
		fmt.Fprintf(cmd.OutOrStdout(), "3x-ui disconnected, inbound %d %s\n", inbound, map[bool]string{true: "kept in the panel", false: "deleted"}[keep])
	case ok && rec.Remove:
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: 3x-ui inbound %d is not removed yet; serve retries it\n", rec.InboundID)
	}
}
