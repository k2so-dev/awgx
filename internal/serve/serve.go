package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/web"
	"github.com/k2so-dev/awgx/internal/webconf"
	"github.com/k2so-dev/awgx/internal/xui"
)

type Intervals struct {
	Stats     time.Duration
	Reconcile time.Duration
	Expire    time.Duration
	Rollup    time.Duration
	Rotate    time.Duration
	Egress    time.Duration
	Updates   time.Duration
	RotateAge time.Duration
}

var Default = Intervals{
	Stats:     10 * time.Second,
	Reconcile: time.Minute,
	Expire:    5 * time.Minute,
	Rollup:    5 * time.Minute,
	Rotate:    time.Hour,
	Egress:    30 * time.Second,
	Updates:   24 * time.Hour,
	RotateAge: 7 * 24 * time.Hour,
}

type Daemon struct {
	app       *core.App
	log       *slog.Logger
	intervals Intervals
	collector *Collector
	watchdog  *egress.Watchdog
	updates   func(ctx context.Context) (string, error)
}

func New(app *core.App, iv Intervals, updates func(ctx context.Context) (string, error)) *Daemon {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	d := &Daemon{app: app, log: log, intervals: iv, collector: NewCollector(), watchdog: egress.NewWatchdog(), updates: updates}
	d.watchdog.Extra = func(ctx context.Context, p state.EgressProfile) error {
		e, err := egress.New(app)
		if err != nil {
			return err
		}
		return xui.Watch(ctx, e, p)
	}
	return d
}

type job struct {
	name  string
	every time.Duration
	run   func(ctx context.Context) error
}

func (d *Daemon) Run(ctx context.Context) error {
	if _, err := d.app.Store(); err != nil {
		return err
	}
	jobs := []job{
		{"stats", d.intervals.Stats, d.stats},
		{"reconcile", d.intervals.Reconcile, d.reconcile},
		{"expire", d.intervals.Expire, d.expire},
		{"rollup", d.intervals.Rollup, d.rollup},
		{"rotate", d.intervals.Rotate, d.rotate},
		{"egress", d.intervals.Egress, d.egress},
	}
	if d.updates != nil && d.intervals.Updates > 0 {
		jobs = append(jobs, job{"updates", d.intervals.Updates, d.checkUpdates})
	}
	d.log.Info("serve started", "jobs", len(jobs))
	if cfg, err := webconf.LoadConfig(d.app); err == nil && cfg.Enabled {
		if !cfg.HasPass {
			d.log.Error("web UI is enabled but has no password, not starting it; run: awgx web passwd")
		} else {
			srv := web.New(d.app.Fork("web"), d.log)
			go func() {
				if err := srv.Run(ctx, cfg.Listen); err != nil {
					d.log.Error("web UI stopped", "err", err)
				}
			}()
		}
	}
	done := make(chan struct{})
	for _, j := range jobs {
		go func(j job) {
			d.runJob(ctx, j)
			t := time.NewTicker(j.every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					done <- struct{}{}
					return
				case <-t.C:
					d.runJob(ctx, j)
				}
			}
		}(j)
	}
	for range jobs {
		<-done
	}
	d.log.Info("serve stopped")
	return nil
}

func (d *Daemon) runJob(ctx context.Context, j job) {
	if err := j.run(ctx); err != nil && ctx.Err() == nil {
		d.log.Error("job failed", "job", j.name, "err", err)
	}
}

func (d *Daemon) locked(ctx context.Context, fn func() error) error {
	unlock, err := d.app.LockContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (d *Daemon) stats(ctx context.Context) error {
	return d.collector.Collect(ctx, d.app, time.Now())
}

func (d *Daemon) each(fn func(inst state.Instance) error) error {
	insts, err := d.app.Instances()
	if err != nil {
		return err
	}
	var errs []error
	for _, inst := range insts {
		if err := fn(inst); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", inst.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (d *Daemon) reconcile(ctx context.Context) error {
	return d.locked(ctx, func() error {
		return d.each(func(inst state.Instance) error {
			drift, err := d.app.Reconcile(ctx, inst.Name, true)
			if drift.Any() {
				d.log.Warn("state and kernel differed, reconciled", "instance", inst.Name, "missing", drift.Missing, "extra", drift.Extra, "wrong_key", drift.WrongKey)
			}
			return err
		})
	})
}

func (d *Daemon) expire(ctx context.Context) error {
	return d.locked(ctx, func() error {
		return d.each(func(inst state.Instance) error {
			names, err := d.app.ExpirePeers(ctx, inst.Name)
			if len(names) > 0 {
				d.log.Info("expired peers disabled", "instance", inst.Name, "peers", names)
			}
			return err
		})
	})
}

func (d *Daemon) rollup(ctx context.Context) error {
	s, err := d.app.Store()
	if err != nil {
		return err
	}
	return s.Rollup(time.Now())
}

func (d *Daemon) rotate(ctx context.Context) error {
	return d.each(func(inst state.Instance) error {
		if time.Since(inst.I1RotatedAt) < d.intervals.RotateAge {
			return nil
		}
		return d.locked(ctx, func() error {
			affected, err := d.app.RotateI1(inst.Name)
			if len(affected) > 0 || i1.Static(inst.I1.Profile) {
				d.log.Info("I1 rotated, re-export configs", "instance", inst.Name, "peers", affected)
			}
			return err
		})
	})
}

func (d *Daemon) egress(ctx context.Context) error {
	return d.locked(ctx, func() error {
		e, err := egress.New(d.app)
		if err != nil {
			return err
		}
		e.Log = func(l string) { d.log.Info("egress", "change", l) }
		if msg, err := xui.Maintain(ctx, e); err != nil {
			d.log.Warn("3x-ui maintenance", "err", err)
		} else if msg != "" {
			d.log.Info("3x-ui", "change", msg)
		}
		events, err := e.Watch(ctx, d.watchdog)
		for _, ev := range events {
			d.log.Warn("egress watchdog", "event", ev)
		}
		return err
	})
}

func (d *Daemon) checkUpdates(ctx context.Context) error {
	v, err := d.updates(ctx)
	if err == nil && v != "" {
		d.log.Info("update available", "version", v)
	}
	return err
}
