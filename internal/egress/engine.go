package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/relay"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

const healthKey = "egress.health"

var (
	ErrPostCheck = errors.New("post-check failed, the change stays applied; undo it with: awgx egress rollback")
	ErrInvalid   = errors.New("egress config is invalid")
	ErrNoChanges = errors.New("nothing to apply: the draft matches the applied egress")
	ErrNoNS      = errors.New("the data plane is not running, run: systemctl restart " + core.DataplaneUnit)
)

type Engine struct {
	App   *core.App
	Store *state.Store
	Log   func(string)
	links map[string]netx.LinkState
}

func init() {
	core.AfterLinksUp = func(ctx context.Context, a *core.App) {
		e, err := New(a)
		if err != nil {
			a.Logf("warning: egress and shaping not applied: %v", err)
			return
		}
		e.Up(ctx)
		if err := a.ApplyRates(ctx); err != nil {
			a.Logf("warning: shaping not applied: %v", err)
		}
	}
}

func Locked(app *core.App, fn func(e *Engine) error) error {
	return app.Locked(func() error {
		e, err := New(app)
		if err != nil {
			return err
		}
		return fn(e)
	})
}

func New(app *core.App) (*Engine, error) {
	s, err := app.Store()
	if err != nil {
		return nil, err
	}
	return &Engine{App: app, Store: s, Log: func(l string) { app.Logf("%s", l) }}, nil
}

func (e *Engine) Health() map[string]Health {
	m := map[string]Health{}
	if v, _ := e.Store.Setting(healthKey); v != "" {
		json.Unmarshal([]byte(v), &m)
	}
	return m
}

func (e *Engine) SetHealth(m map[string]Health) error {
	b, _ := json.Marshal(m)
	return e.Store.SetSetting(healthKey, string(b))
}

func (e *Engine) PinLinks(ctx context.Context) error {
	if !netx.NSExists() {
		return nil
	}
	links, err := netx.LinkStates(ctx, e.App.NS())
	if err != nil {
		return err
	}
	e.links = links
	return nil
}

func (e *Engine) Input(ctx context.Context, cfg state.EgressConfig) (Input, error) {
	in := Input{Config: cfg, Health: e.Health(), Up: map[string]bool{}, Has6: map[string]bool{}, MTU: map[string]int{},
		Root: netx.RootAddrs(), HostTable: map[string]int{}}
	var err error
	if in.Instances, err = e.Store.Instances(); err != nil {
		return in, err
	}
	for _, inst := range in.Instances {
		in.HostTable[inst.Name] = e.App.HostTable(inst.Name)
		peers, err := e.Store.Peers(inst.Name)
		if err != nil {
			return in, err
		}
		in.Peers = append(in.Peers, peers...)
	}
	if in.Uplinks, err = e.Store.Uplinks(); err != nil {
		return in, err
	}
	links := e.links
	if links == nil && netx.NSExists() {
		if links, err = netx.LinkStates(ctx, e.App.NS()); err != nil {
			return in, err
		}
	}
	for name, l := range links {
		in.Up[name] = l.Up
		in.Has6[name] = l.Has6
		in.MTU[name] = l.MTU
	}
	return in, nil
}

func (e *Engine) Converge(ctx context.Context, cfg state.EgressConfig) (Desired, error) {
	if err := netx.EnsureNS(ctx, e.App.Run); err != nil {
		return Desired{}, err
	}
	in, err := e.Input(ctx, cfg)
	if err != nil {
		return Desired{}, err
	}
	d := Render(in)
	ns := e.App.NS()
	var errs []error
	if changed, err := relay.WriteSpec(relay.SpecPath, d.Relay); err != nil {
		errs = append(errs, err)
	} else if changed {
		e.reloadRelay(ctx)
	}
	if err := netx.ApplyRuleset(ctx, ns, netx.Ruleset(d.Nft)); err != nil {
		errs = append(errs, err)
	}
	if err := netx.ApplyPolicy(ctx, ns, d.Policy, e.Log); err != nil {
		errs = append(errs, err)
	}
	return d, errors.Join(errs...)
}

var ReloadRelay func(context.Context) error

func (e *Engine) reloadRelay(ctx context.Context) {
	if ReloadRelay != nil {
		if err := ReloadRelay(ctx); err != nil {
			e.Log("relay: " + err.Error())
		}
		return
	}
	e.App.Run.Run(ctx, "systemctl", "kill", "--signal=HUP", "--kill-whom=main", core.DataplaneUnit)
}

type Plan struct {
	Applied  int64              `json:"applied"`
	Diff     []string           `json:"diff"`
	Issues   []string           `json:"issues"`
	Warnings []string           `json:"warnings"`
	Changed  bool               `json:"changed"`
	Draft    state.EgressConfig `json:"-"`
}

func (e *Engine) Plan(ctx context.Context) (Plan, error) {
	p, _, _, err := e.plan(ctx)
	return p, err
}

func (e *Engine) PlanStatus(ctx context.Context) (Plan, Status, error) {
	p, applied, from, err := e.plan(ctx)
	if err != nil {
		return p, Status{}, err
	}
	st, err := e.status(ctx, applied, from.Policy)
	return p, st, err
}

func (e *Engine) plan(ctx context.Context) (Plan, state.Snapshot, Desired, error) {
	var p Plan
	applied, err := e.Store.Applied()
	if err != nil {
		return p, applied, Desired{}, err
	}
	p.Applied = applied.ID
	if p.Draft, err = e.Store.Draft(); err != nil {
		return p, applied, Desired{}, err
	}
	in, err := e.Input(ctx, applied.Config)
	if err != nil {
		return p, applied, Desired{}, err
	}
	to := in
	to.Config = p.Draft
	from := Render(in)
	p.Diff = netx.Diff(from.Lines(), Render(to).Lines())
	p.Changed = len(p.Diff) > 0 || !SameConfig(p.Draft, applied.Config)
	p.Issues, p.Warnings = Validate(to)
	return p, applied, from, nil
}

type ApplyOptions struct {
	SSH sysx.SSHSession
}

type Result struct {
	Snapshot int64    `json:"snapshot"`
	Base     int64    `json:"base"`
	Diff     []string `json:"diff"`
	Checks   []string `json:"checks"`
	Status   string   `json:"status"`
}

func (e *Engine) Apply(ctx context.Context, o ApplyOptions) (Result, error) {
	var res Result
	plan, applied, _, err := e.plan(ctx)
	if err != nil {
		return res, err
	}
	res.Diff, res.Base = plan.Diff, applied.ID
	if !plan.Changed {
		return res, ErrNoChanges
	}
	if len(plan.Issues) > 0 {
		return res, fmt.Errorf("%w:\n  %s", ErrInvalid, strings.Join(plan.Issues, "\n  "))
	}
	checks, err := e.PreChecks(ctx, plan.Draft)
	res.Checks = checks
	if err != nil {
		e.Store.Log("egress-apply", "pre-check failed: "+err.Error())
		return res, fmt.Errorf("pre-check failed, nothing was changed: %w", err)
	}
	sn := state.Snapshot{
		Status: state.SnapshotConfirmed,
		Config: plan.Draft,
		Base:   applied.ID,
		Admin:  o.SSH.String(),
		Diff:   strings.Join(plan.Diff, "\n"),
		Checks: strings.Join(checks, "\n"),
	}
	if err := e.Store.AddSnapshot(&sn); err != nil {
		return res, err
	}
	res.Snapshot = sn.ID
	fail := func(stage string, cause error) (Result, error) {
		res.Checks = append(res.Checks, "FAIL "+stage+": "+cause.Error())
		if rerr := e.restore(ctx, sn, state.SnapshotFailed, strings.Join(res.Checks, "\n")); rerr != nil {
			return res, fmt.Errorf("%s: %w; rollback also failed: %v", stage, cause, rerr)
		}
		res.Status = state.SnapshotFailed
		return res, fmt.Errorf("%s failed, rolled back: %w", stage, cause)
	}
	if _, err := e.Converge(ctx, plan.Draft); err != nil {
		return fail("apply", err)
	}
	post, perr := e.PostChecks(ctx, plan.Draft)
	res.Checks = append(res.Checks, post...)
	e.Store.UpdateSnapshot(sn.ID, state.SnapshotConfirmed, strings.Join(res.Checks, "\n"))
	res.Status = state.SnapshotConfirmed
	msg := fmt.Sprintf("snapshot %d applied\n%s", sn.ID, sn.Diff)
	if perr != nil {
		msg = fmt.Sprintf("snapshot %d applied, post-check failed: %v\n%s", sn.ID, perr, sn.Diff)
	}
	e.Store.Log("egress-apply", msg)
	e.afterApply(ctx)
	if perr != nil {
		return res, fmt.Errorf("%w: %w", ErrPostCheck, perr)
	}
	return res, nil
}

var AfterApply func(context.Context, *Engine) error

func (e *Engine) afterApply(ctx context.Context) {
	if AfterApply != nil {
		if err := AfterApply(context.WithoutCancel(ctx), e); err != nil {
			e.Log("after apply: " + err.Error())
		}
	}
}

func (e *Engine) restore(ctx context.Context, sn state.Snapshot, status, checks string) error {
	ctx = context.WithoutCancel(ctx)
	base, err := e.Store.Confirmed(sn.ID)
	if err != nil {
		return err
	}
	e.SetHealth(map[string]Health{})
	if insts, err := e.Store.Instances(); err == nil {
		for _, inst := range insts {
			selftest.Cleanup(ctx, e.App, inst)
		}
	}
	if netx.NSExists() {
		netx.ClearTest(ctx, e.App.NS())
		netx.ClearProbe(ctx, e.App.NS())
	}
	_, cerr := e.Converge(ctx, base.Config)
	if cerr != nil {
		e.Log("restore was partial, the watchdog keeps retrying: " + cerr.Error())
	}
	e.Store.UpdateSnapshot(sn.ID, status, checks)
	e.Store.Log("egress-rollback", fmt.Sprintf("snapshot %d %s, restored snapshot %d\n%s", sn.ID, status, base.ID, checks))
	e.afterApply(ctx)
	return cerr
}

func (e *Engine) ResetDraft() error {
	sn, err := e.Store.Applied()
	if err != nil {
		return err
	}
	return e.Store.RestoreDraft(sn.Config)
}

func (e *Engine) RollbackTo(ctx context.Context, id int64, o ApplyOptions) (Result, error) {
	if id == 0 {
		applied, err := e.Store.Applied()
		if err != nil {
			return Result{}, err
		}
		if id = applied.Base; id == 0 {
			return Result{}, errors.New("nothing to roll back: the applied snapshot has no earlier one")
		}
	}
	sn, err := e.Store.Snapshot(id)
	if err != nil {
		return Result{}, err
	}
	draft, err := e.Store.Draft()
	if err != nil {
		return Result{}, err
	}
	if err := e.Store.RestoreDraft(sn.Config); err != nil {
		return Result{}, err
	}
	res, err := e.Apply(ctx, o)
	if err != nil && res.Snapshot == 0 {
		return res, errors.Join(err, e.Store.RestoreDraft(draft))
	}
	return res, err
}

func (e *Engine) Reconcile(ctx context.Context) (Desired, error) {
	sn, err := e.Store.Applied()
	if err != nil {
		return Desired{}, err
	}
	return e.Converge(ctx, sn.Config)
}

func (e *Engine) ApplyOrConverge(ctx context.Context, o ApplyOptions) (Result, bool, error) {
	res, err := e.Apply(ctx, o)
	if !errors.Is(err, ErrNoChanges) {
		return res, false, err
	}
	if !netx.NSExists() {
		return res, true, ErrNoNS
	}
	_, err = e.Reconcile(ctx)
	return res, true, err
}

func (e *Engine) Up(ctx context.Context) {
	if _, err := e.Reconcile(ctx); err != nil {
		e.Log("warning: egress was not fully applied, serve retries in 30s: " + err.Error())
		e.Store.Log("egress-up", "partial: "+err.Error())
	}
}

func (e *Engine) Down(ctx context.Context) error {
	if !netx.NSExists() {
		return nil
	}
	return errors.Join(netx.FlushPolicy(ctx, e.App.NS()), netx.RemoveTable(ctx, e.App.NS()))
}

type Status struct {
	Applied int64             `json:"applied"`
	Health  map[string]Health `json:"health"`
	Drift   []string          `json:"drift,omitempty"`
	History []state.Snapshot  `json:"history"`
}

func (e *Engine) Status(ctx context.Context) (Status, error) {
	sn, err := e.Store.Applied()
	if err != nil {
		return Status{}, err
	}
	in, err := e.Input(ctx, sn.Config)
	if err != nil {
		return Status{}, err
	}
	return e.status(ctx, sn, Render(in).Policy)
}

func (e *Engine) status(ctx context.Context, sn state.Snapshot, want netx.Policy) (Status, error) {
	var st Status
	var err error
	st.Applied = sn.ID
	st.Health = e.Health()
	if st.History, err = e.Store.Snapshots(10); err != nil {
		return st, err
	}
	if !netx.NSExists() {
		st.Drift = []string{"- network namespace " + netx.NS + " is missing: the data plane is not running"}
		return st, nil
	}
	live, err := netx.ReadPolicy(ctx, e.App.NS())
	if err != nil {
		return st, err
	}
	st.Drift = netx.Drift(live, want)
	return st, nil
}

func SameConfig(a, b state.EgressConfig) bool {
	norm := func(c state.EgressConfig) string {
		if len(c.Profiles) == 0 {
			c.Profiles = nil
		}
		def := map[string]state.Assign{}
		for k, v := range c.Default {
			if v.Profile != "" && (v.Profile != state.Direct || v.Killswitch) {
				def[k] = v
			}
		}
		c.Default = def
		if len(c.Default) == 0 {
			c.Default = nil
		}
		if len(c.Peers) == 0 {
			c.Peers = nil
		}
		b, _ := json.Marshal(c)
		return string(b)
	}
	return norm(a) == norm(b)
}
