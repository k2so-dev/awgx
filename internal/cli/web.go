package cli

import (
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/qr"
	"github.com/k2so-dev/awgx/internal/web"
	"github.com/k2so-dev/awgx/internal/webconf"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func webCmd(app *core.App) *cobra.Command {
	cmd := &cobra.Command{Use: "web", Short: "Built-in web UI served by awgx-serve (127.0.0.1 by default)"}
	cmd.AddCommand(webStatusCmd(app), webEnableCmd(app), webDisableCmd(app), webPasswdCmd(app), webMetricsCmd(app))
	return cmd
}

func webStatusCmd(app *core.App) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show web UI settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := webconf.LoadConfig(app)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), c)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "enabled:  %v\nlisten:   %s\nhost:     %s\npassword: %v\ntotp:     %v\nmetrics:  %v\n", c.Enabled, c.Listen, orDash(c.Host), c.HasPass, c.TOTP, c.Metrics)
			if c.Enabled {
				_, port, _ := net.SplitHostPort(c.Listen)
				fmt.Fprintf(out, "\nfrom your machine: %s, then open http://127.0.0.1:%s\n", webconf.Tunnel(c.Listen, webconf.Endpoint(app)), port)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func webEnableCmd(app *core.App) *cobra.Command {
	var listen, host string
	var yes bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Serve the web UI from awgx-serve",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := webconf.LoadConfig(app)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("listen") {
				listen = c.Listen
			}
			if !cmd.Flags().Changed("host") {
				host = c.Host
			}
			ap, err := netip.ParseAddrPort(listen)
			if err != nil {
				return fmt.Errorf("--listen must be IP:PORT: %w", err)
			}
			if !ap.Addr().IsLoopback() && !confirm(yes, listen+" is reachable from the network; put it behind a TLS reverse proxy. Continue?") {
				return fmt.Errorf("aborted")
			}
			if !c.HasPass {
				pass, err := readPassword(cmd, false)
				if err != nil {
					return err
				}
				if err := webconf.SetPassword(app, pass); err != nil {
					return err
				}
			}
			if err := webconf.Enable(app, listen, host); err != nil {
				return err
			}
			if err := app.StartServe(cmd.Context()); err != nil {
				return err
			}
			_, port, _ := net.SplitHostPort(listen)
			fmt.Fprintf(cmd.OutOrStdout(), "web UI on %s; from your machine: %s, then open http://127.0.0.1:%s\n", listen, webconf.Tunnel(listen, webconf.Endpoint(app)), port)
			return nil
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "listen address (default: the stored one, else "+webconf.DefaultListen+")")
	cmd.Flags().StringVar(&host, "host", "", "domain name of a reverse proxy in front of the UI (other Host headers are refused)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask about non-loopback addresses")
	return cmd
}

func webDisableCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Stop serving the web UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := webconf.Disable(app); err != nil {
				return err
			}
			return app.RestartServe(cmd.Context())
		},
	}
}

func readSecret(cmd *cobra.Command, prompt string) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return string(b), err
	}
	line, err := input.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readPassword(cmd *cobra.Command, stdin bool) (string, error) {
	if stdin {
		return readSecret(cmd, "")
	}
	pass, err := readSecret(cmd, "new web UI password: ")
	if err != nil || !term.IsTerminal(int(os.Stdin.Fd())) {
		return pass, err
	}
	again, err := readSecret(cmd, "repeat: ")
	if err != nil {
		return "", err
	}
	if again != pass {
		return "", fmt.Errorf("passwords differ")
	}
	return pass, nil
}

func webPasswdCmd(app *core.App) *cobra.Command {
	var stdin, totp, noTOTP bool
	cmd := &cobra.Command{
		Use:   "passwd",
		Short: "Set the web UI password (argon2id), optionally with TOTP",
		RunE: func(cmd *cobra.Command, args []string) error {
			pass, err := readPassword(cmd, stdin)
			if err != nil {
				return err
			}
			if err := webconf.CheckPassword(pass); err != nil {
				return err
			}
			var secret string
			if totp {
				secret = web.NewTOTPSecret()
				host, _ := os.Hostname()
				url := web.TOTPURL(secret, host)
				code, _ := qr.Terminal(url)
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\nsecret: %s\n", code, url, secret)
				answer, err := readSecret(cmd, "code from the authenticator: ")
				if err != nil {
					return err
				}
				if !web.VerifyTOTP(secret, strings.TrimSpace(answer), time.Now()) {
					return fmt.Errorf("wrong code, nothing changed")
				}
			}
			if err := webconf.SetPassword(app, pass); err != nil {
				return err
			}
			s, err := app.Store()
			if err != nil {
				return err
			}
			if noTOTP {
				if err := s.DeleteSecret(webconf.SecretTOTP); err != nil {
					return err
				}
			}
			if totp {
				if err := s.SetSecretSetting(webconf.SecretTOTP, secret); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "TOTP enabled")
			}
			if c, _ := webconf.LoadConfig(app); c.Enabled {
				return app.RestartServe(cmd.Context())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&stdin, "password-stdin", false, "read the password from stdin")
	cmd.Flags().BoolVar(&totp, "totp", false, "require a TOTP code")
	cmd.Flags().BoolVar(&noTOTP, "no-totp", false, "remove TOTP")
	cmd.MarkFlagsMutuallyExclusive("totp", "no-totp")
	return cmd
}

func webMetricsCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:       "metrics on|off",
		Short:     "Prometheus /metrics on the web UI address, without authentication",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "on" && args[0] != "off" {
				return fmt.Errorf("use on or off")
			}
			return webconf.SetMetrics(app, args[0] == "on")
		},
	}
}
