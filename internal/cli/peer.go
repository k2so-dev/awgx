package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/qr"
	"github.com/k2so-dev/awgx/internal/render"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
	"github.com/spf13/cobra"
)

func peerCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{Use: "peer", Short: "Manage peers live, without restarting the interface"}
	cmd.AddCommand(peerAddCmd(app), peerRmCmd(app), peerLsCmd(app), peerShowCmd(app), peerQRCmd(app),
		peerExportCmd(app), peerSetCmd(app), peerExpireCmd(app))
	return cmd
}

func peerAddCmd(app *core.App) *cobra.Command {
	var egress, expires, note, netLabel string
	var noPSK, noQR bool
	var f i1Flags
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Create a peer and show its QR code",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			exp, err := core.ParseDuration(expires)
			if err != nil {
				return err
			}
			s, err := f.settings()
			if err != nil {
				return err
			}
			var p state.Peer
			err = app.Locked(func() error {
				var is validate.Issues
				p, is, err = app.AddPeer(cmd.Context(), core.PeerOptions{
					Name: args[0], Instance: app.Selected, Egress: egress, Expires: exp, I1: s, Note: note, NetLabel: netLabel, NoPSK: noPSK,
				})
				printIssues(cmd.ErrOrStderr(), is)
				return err
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "peer %s added: %s\n", p.Name, strings.Trim(fmt.Sprint(render.PeerAddresses(p)), "[]"))
			if egress != "" {
				fmt.Fprintln(cmd.OutOrStdout(), egressHint)
			}
			if noQR {
				return nil
			}
			return showQR(app, cmd.OutOrStdout(), p.Name)
		},
	}
	cmd.Flags().StringVar(&egress, "egress", "", "egress profile or direct (default: the instance egress)")
	cmd.Flags().StringVar(&expires, "expires", "", "expire after a duration, e.g. 30d or 12h")
	cmd.Flags().StringVar(&note, "note", "", "free text note")
	cmd.Flags().StringVar(&netLabel, "net", "", "network label for statistics (operator, country, mobile)")
	cmd.Flags().BoolVar(&noPSK, "no-psk", false, "do not use a preshared key")
	cmd.Flags().BoolVar(&noQR, "no-qr", false, "do not print the QR code")
	f.register(cmd, "i1")
	return cmd
}

func peerRmCmd(app *core.App) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm NAME",
		Short: "Remove a peer from the kernel and the state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm(yes, "remove peer "+args[0]+"?") {
				return fmt.Errorf("aborted")
			}
			return app.Locked(func() error { return app.RemovePeer(cmd.Context(), args[0]) })
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func peerLsCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List peers",
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := app.PeerRows(cmd.Context(), app.Selected)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), rows)
			}
			insts, _ := app.Instances()
			many := len(insts) > 1 && app.Selected == ""
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			head := "NAME\tADDRESS\tHANDSHAKE\tRX\tTX\tEGRESS\tI1\tSTATUS"
			if many {
				head = "INSTANCE\t" + head
			}
			fmt.Fprintln(w, head)
			for _, r := range rows {
				if many {
					fmt.Fprintf(w, "%s\t", r.Instance)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Address4, fmtx.Ago(r.LastHandshake),
					fmtx.Bytes(r.RxBytes), fmtx.Bytes(r.TxBytes), egressLabel(r.Egress), r.I1Profile, r.Label())
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func peerShowCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show NAME",
		Short: "Show a peer",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := app.PeerRows(cmd.Context(), "")
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.Name != args[0] {
					continue
				}
				if asJSON {
					return printJSON(cmd.OutOrStdout(), r)
				}
				w := cmd.OutOrStdout()
				fmt.Fprintf(w, "name:       %s\ninstance:   %s\nstatus:     %s\naddress:    %s\npublic key: %s\n", r.Name, r.Instance, r.Label(), strings.Trim(fmt.Sprint(render.PeerAddresses(r.Peer)), "[]"), r.PublicKey)
				fmt.Fprintf(w, "egress:     %s\nI1:         %s\nkeepalive:  %s\nhandshake:  %s\nendpoint:   %s\n", egressLabel(r.Egress), r.I1Profile, r.Keepalive, fmtx.Ago(r.LastHandshake), r.Endpoint)
				fmt.Fprintf(w, "transfer:   %s received, %s sent\n", fmtx.Bytes(r.RxBytes), fmtx.Bytes(r.TxBytes))
				if !r.ExpiresAt.IsZero() {
					fmt.Fprintf(w, "expires:    %s\n", r.ExpiresAt.Local().Format(time.DateTime))
				}
				if r.NetLabel != "" {
					fmt.Fprintf(w, "network:    %s\n", r.NetLabel)
				}
				if r.Note != "" {
					fmt.Fprintf(w, "note:       %s\n", r.Note)
				}
				return nil
			}
			return fmt.Errorf("peer %s: %w", args[0], state.ErrNotFound)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func exportPeer(app *core.App, name string) (state.Instance, state.Peer, error) {
	store, err := app.Store()
	if err != nil {
		return state.Instance{}, state.Peer{}, err
	}
	p, err := store.Peer(name)
	if err != nil {
		return state.Instance{}, p, err
	}
	inst, err := store.Instance(p.Instance)
	return inst, p, err
}

func showQR(app *core.App, w io.Writer, name string) error {
	inst, p, err := exportPeer(app, name)
	if err != nil {
		return err
	}
	payloads, err := render.QRPayloads(inst, p)
	if err != nil {
		return err
	}
	for n, data := range payloads {
		code, err := qr.Terminal(data)
		if err != nil {
			return err
		}
		if len(payloads) > 1 {
			fmt.Fprintf(w, "QR %d of %d\n", n+1, len(payloads))
		}
		fmt.Fprint(w, code)
	}
	fmt.Fprintln(w, "scan with AmneziaVPN; other clients: awgx peer export "+name+" --format conf")
	return nil
}

func peerQRCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "qr NAME",
		Short: "Print the AmneziaVPN QR code",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return showQR(app, cmd.OutOrStdout(), args[0])
		},
	}
}

func peerExportCmd(app *core.App) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "export NAME",
		Short: "Export a client config: conf for AmneziaWG clients, vpn for AmneziaVPN, json",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, p, err := exportPeer(app, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "note: the client must support AmneziaWG 3.1 (AmneziaVPN 5.0.1.5+, amneziawg-tools 3.1)")
			w := cmd.OutOrStdout()
			switch format {
			case "conf":
				fmt.Fprint(w, render.Client(inst, p).String())
			case "vpn":
				url, err := render.VPNURL(inst, p)
				if err != nil {
					return err
				}
				fmt.Fprintln(w, url)
			case "json":
				e, err := render.Export(inst, p)
				if err != nil {
					return err
				}
				return printJSON(w, e)
			default:
				return fmt.Errorf("unknown format %q", format)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "conf", "conf, vpn or json")
	return cmd
}

func peerSetCmd(app *core.App) *cobra.Command {
	var f i1Flags
	var enable, disable bool
	var note, netLabel, egressName string
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Change a peer: --i1 PROFILE|inherit, --enable, --disable, --note, --net",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if egressName == "inherit" {
				egressName = ""
			}
			var settings *i1.Settings
			if f.profile != "inherit" {
				var err error
				if settings, err = f.settings(); err != nil {
					return err
				}
			}
			ch := cmd.Flags().Changed
			edit := enable || disable || ch("note") || ch("net") || f.profile != ""
			return app.Locked(func() error {
				if ch("egress") {
					if err := app.CheckEgress(egressName); err != nil {
						return err
					}
				}
				if edit {
					_, err := app.UpdatePeer(cmd.Context(), args[0], func(p *state.Peer) error {
						if enable {
							p.Enabled = true
						}
						if disable {
							p.Enabled = false
						}
						if ch("note") {
							p.Note = note
						}
						if ch("net") {
							p.NetLabel = netLabel
						}
						if f.profile == "" {
							return nil
						}
						is, err := app.PeerI1(p, settings)
						printIssues(cmd.ErrOrStderr(), is)
						return err
					})
					if err != nil {
						return err
					}
					if f.profile != "" {
						fmt.Fprintln(cmd.OutOrStdout(), "I1 changed: only this peer needs a new config")
					}
				}
				if !ch("egress") {
					return nil
				}
				e, err := egress.New(app)
				if err != nil {
					return err
				}
				cur, err := e.Store.Peer(args[0])
				if err != nil {
					return err
				}
				ks := cur.Killswitch && egressName != "" && egressName != state.Direct
				if err := e.Assign("", []string{args[0]}, state.Assign{Profile: egressName, Killswitch: ks}); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), egressHint)
				return nil
			})
		},
	}
	f.register(cmd, "i1")
	cmd.Flags().BoolVar(&enable, "enable", false, "enable the peer")
	cmd.Flags().BoolVar(&disable, "disable", false, "disable the peer without removing it")
	cmd.Flags().StringVar(&note, "note", "", "free text note")
	cmd.Flags().StringVar(&netLabel, "net", "", "network label")
	cmd.Flags().StringVar(&egressName, "egress", "", "egress profile, direct, or inherit to follow the instance")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func peerExpireCmd(app *core.App) *cobra.Command {
	var in string
	var never bool
	cmd := &cobra.Command{
		Use:   "expire NAME",
		Short: "Set or clear the peer expiry (--in 30d | --never)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := core.ParseDuration(in)
			if err != nil {
				return err
			}
			if d == 0 && !never {
				return fmt.Errorf("pass --in DURATION or --never")
			}
			return app.Locked(func() error {
				_, err := app.UpdatePeer(cmd.Context(), args[0], func(p *state.Peer) error {
					p.ExpiresAt = time.Time{}
					if d > 0 {
						p.ExpiresAt = time.Now().Add(d).UTC().Truncate(time.Second)
					}
					return nil
				})
				return err
			})
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "expire after a duration, e.g. 30d")
	cmd.Flags().BoolVar(&never, "never", false, "remove the expiry")
	cmd.MarkFlagsMutuallyExclusive("in", "never")
	return cmd
}

const egressHint = "egress changes take effect after: awgx egress plan && awgx egress apply"

func egressLabel(s string) string {
	if s == "" {
		return "default"
	}
	return s
}
