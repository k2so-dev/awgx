package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/relay"
	"github.com/spf13/cobra"
)

func linksUp(app *core.App) bool {
	insts, err := app.Instances()
	if err != nil || len(insts) == 0 {
		return false
	}
	for _, inst := range insts {
		if !app.InterfaceUp(inst.Name) {
			return false
		}
	}
	return true
}

func netUp(ctx context.Context, app *core.App) {
	if err := app.UpAll(ctx); err != nil {
		app.Logf("warning: %v", err)
	}
	app.LinksChanged(ctx)
}

var errBusy = errors.New("state is locked")

func hostRoutes(ctx context.Context, app *core.App) error {
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	unlock, err := app.LockContext(lctx)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return errBusy
	}
	if err != nil {
		return err
	}
	defer unlock()
	defer app.Close()
	e, err := egress.New(app)
	if err != nil {
		return err
	}
	_, err = e.Reconcile(ctx)
	return err
}

func dataplaneCmd(app *core.App) *cobra.Command {
	return &cobra.Command{
		Use:    "dataplane",
		Short:  "Run the awgx data plane: network namespace, interfaces and the egress relay",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			hup := make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP)
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			if err := netx.EnsureNS(ctx, app.Run); err != nil {
				return err
			}
			srv, err := relay.NewServer(netx.NSPath, func(s string) { app.Logf("%s", s) })
			if err != nil {
				return err
			}
			var relayRetry time.Time
			reload := func() {
				relayRetry = time.Time{}
				spec, err := relay.ReadSpec(relay.SpecPath)
				if err == nil {
					err = srv.Apply(spec)
				}
				if err != nil {
					app.Logf("relay: %v, retrying in 30s", err)
					relayRetry = time.Now().Add(30 * time.Second)
				}
			}
			if err := srv.StartEcho(netx.EchoDev); err != nil {
				app.Logf("warning: ping emulation is off: %v", err)
			}
			egress.ReloadRelay = func(context.Context) error {
				reload()
				return nil
			}
			reload()
			lctx, lcancel := context.WithTimeout(ctx, 30*time.Second)
			unlock, lockErr := app.LockContext(lctx)
			lcancel()
			switch {
			case lockErr == nil:
				netUp(ctx, app)
			case linksUp(app):
				app.Logf("state is locked and the links are up, the lock holder manages them")
			default:
				app.Logf("state is locked, bringing links up without the lock")
				netUp(ctx, app)
			}
			app.Close()
			if lockErr == nil {
				unlock()
			}
			srv.WriteStats()
			app.Logf("data plane is running in network namespace %s", netx.NS)
			go srv.Run(ctx)
			addrs := netx.RootAddrs()
			var retry time.Time
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-hup:
					reload()
				case <-tick.C:
					if !relayRetry.IsZero() && time.Now().After(relayRetry) {
						reload()
					}
					now := netx.RootAddrs()
					if slices.Equal(now, addrs) || time.Now().Before(retry) {
						continue
					}
					switch err := hostRoutes(ctx, app); {
					case err == nil:
						app.Logf("host addresses changed, routes to them updated")
						addrs = now
					case !errors.Is(err, errBusy):
						app.Logf("warning: routes to the new host addresses not updated, retrying in 30s: %v", err)
						retry = time.Now().Add(30 * time.Second)
					}
				}
			}
		},
	}
}
