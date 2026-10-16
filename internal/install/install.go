package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/backup"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/k2so-dev/awgx/internal/upgrade"
	"github.com/k2so-dev/awgx/internal/validate"
	"github.com/k2so-dev/awgx/internal/webconf"
)

type Options struct {
	Gen          core.GenOptions
	Force        bool
	SkipSelfTest bool
	Restore      string
	RegenServer  bool
	Serve        bool
	Confirm      func(question string) bool
	PurgeLegacy  func(ctx context.Context, items []doctor.LegacyItem) error
	SkipLegacy   bool
}

type Result struct {
	Instance state.Instance  `json:"instance"`
	Issues   validate.Issues `json:"issues,omitempty"`
	Duration time.Duration   `json:"duration"`
}

func Run(ctx context.Context, app *core.App, o Options) (Result, error) {
	start := time.Now()
	var res Result
	if !sysx.IsRoot() {
		return res, errors.New("install must run as root")
	}
	if app.Installed() {
		return res, errors.New("awgx is already installed, use awgx reinstall or awgx purge")
	}
	osInfo := sysx.ReadOS()
	if _, err := PPADist(osInfo); err != nil {
		return res, err
	}
	kernel := sysx.KernelRelease()
	env := &doctor.Env{Run: app.Run, OS: osInfo, Kernel: kernel, SysctlDir: app.Paths.SysctlDir}

	app.Logf("preflight doctor")
	results := doctor.Run(ctx, env)
	var failed []doctor.Result
	for _, r := range doctor.Failed(results) {
		failed = append(failed, r)
		app.Logf("  fail %s: %s", r.ID, r.Message)
	}
	if len(failed) > 0 && !o.Force {
		return res, errors.New("preflight failed, fix the problems above or pass --force")
	}

	var items []doctor.LegacyItem
	if !o.SkipLegacy {
		items = doctor.Legacy(ctx, env)
	}
	if len(items) > 0 {
		var names []string
		for _, i := range items {
			names = append(names, i.Name)
		}
		found := "found old WireGuard/AmneziaWG setups: " + strings.Join(names, ", ")
		if o.PurgeLegacy == nil {
			app.Logf("%s; kept, --remove-old backs them up and removes them", found)
		} else if o.Confirm != nil && o.Confirm(found+"; back up and remove them?") {
			if err := o.PurgeLegacy(ctx, items); err != nil {
				return res, err
			}
		}
	}

	var newDirs []string
	for _, d := range []string{"/var/lib/dkms", "/etc/amnezia", "/etc/apt/keyrings"} {
		if !sysx.Exists(d) {
			newDirs = append(newDirs, d)
		}
	}
	added, err := installPackages(ctx, app, osInfo, kernel)
	if err != nil {
		if errors.Is(err, ErrModuleBuild) {
			return res, fmt.Errorf("%w\nthe kernel %s is not supported by the upstream module; awgx needs a kernel it builds for", err, kernel)
		}
		return res, err
	}
	if err := upgrade.CacheDebs(ctx, app); err != nil {
		app.Logf("cannot cache packages for rollback: %v", err)
	}
	if o.Restore != "" {
		app.Logf("restoring state from %s", o.Restore)
		if _, err := backup.Restore(o.Restore, "/", app.BackupPaths()); err != nil {
			return res, fmt.Errorf("restore: %w", err)
		}
	}
	store, err := app.Init()
	if err != nil {
		return res, err
	}
	for _, d := range newDirs {
		store.AddArtifact(d, "dir")
	}
	for _, p := range added {
		store.AddArtifact("package:"+p, "package")
	}
	store.AddArtifact(KeyringPath, "file")
	store.AddArtifact(SourcesPath, "file")

	if _, err := app.Run.Run(ctx, "modprobe", "amneziawg"); err != nil {
		return res, fmt.Errorf("load module: %w", err)
	}
	if v := awg.KernelModuleVersion(); !awg.Supports31(v) {
		return res, fmt.Errorf("installed module %q does not support AWG 3.1", v)
	}
	tv, err := awg.Tool{Run: app.Run}.ToolsVersion(ctx)
	if err != nil {
		return res, err
	}
	if !awg.Supports31(tv) {
		return res, fmt.Errorf("installed amneziawg-tools %s do not support AWG 3.1", tv)
	}

	if err := installBinary(app); err != nil {
		return res, err
	}
	store.AddArtifact(app.Paths.Binary, "file")
	if err := installHook(app); err != nil {
		return res, err
	}

	insts, issues, err := instances(ctx, app, o)
	res.Issues = issues
	if err != nil {
		return res, err
	}
	for i := range insts {
		if err := store.SaveInstance(&insts[i]); err != nil {
			return res, err
		}
		if err := app.WriteServerConf(insts[i]); err != nil {
			return res, err
		}
		store.AddArtifact(app.Paths.Conf(insts[i].Name), "file")
	}
	res.Instance = insts[0]

	for _, inst := range insts {
		if err := app.OpenFirewall(ctx, inst); err != nil {
			return res, err
		}
		app.Logf("starting %s on %d/udp in network namespace %s", inst.Name, inst.ListenPort, netx.NS)
		if err := app.UpInstance(ctx, inst); err != nil {
			return res, err
		}
		store.Log("install", fmt.Sprintf("%s preset=%s port=%d", inst.Name, inst.Preset, inst.ListenPort))
	}
	if err := app.InstallDataplane(ctx); err != nil {
		return res, err
	}

	if !o.SkipSelfTest {
		for _, inst := range insts {
			app.Logf("self-test of %s: handshake from a test peer in a network namespace", inst.Name)
			if err := selftest.Run(ctx, app, inst); err != nil {
				return res, fmt.Errorf("self-test of %s failed: %w", inst.Name, err)
			}
		}
		app.Logf("self-test passed")
	}
	if err := installServe(ctx, app, o.Serve); err != nil {
		return res, err
	}
	res.Duration = time.Since(start).Round(time.Second)
	return res, nil
}

func instances(ctx context.Context, app *core.App, o Options) ([]state.Instance, validate.Issues, error) {
	if o.Restore == "" {
		inst, is, err := app.NewInstance(ctx, o.Gen)
		return []state.Instance{inst}, is, err
	}
	insts, err := app.Instances()
	if err != nil {
		return nil, nil, err
	}
	if len(insts) == 0 {
		return nil, nil, errors.New("the backup has no instances")
	}
	var all validate.Issues
	for i, inst := range insts {
		var is validate.Issues
		if o.RegenServer {
			app.Logf("regenerating server-side parameters of %s: every client needs a new config", inst.Name)
			insts[i], is, err = app.Regenerate(inst, o.Gen.Preset, o.Gen.Toggles)
		} else {
			is, err = app.CheckInstance(inst)
		}
		all = append(all, is...)
		if err != nil {
			return insts, all, err
		}
	}
	return insts, all, nil
}

func installBinary(app *core.App) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if self == app.Paths.Binary {
		return nil
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := app.Paths.Binary + ".new"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, app.Paths.Binary)
}

func serveUnit(binary string) string {
	return `[Unit]
Description=awgx daemon: statistics, reconcile, expiry, I1 rotation
After=network-online.target awgx-dataplane.service
Wants=network-online.target

[Service]
ExecStart=` + binary + ` serve
Restart=always
RestartSec=5
RestartSteps=5
RestartMaxDelaySec=120

[Install]
WantedBy=multi-user.target
`
}

func installServe(ctx context.Context, app *core.App, on bool) error {
	store, err := app.Store()
	if err != nil {
		return err
	}
	path := filepath.Join("/etc/systemd/system", core.ServeUnit)
	if err := os.WriteFile(path, []byte(serveUnit(app.Paths.Binary)), 0o644); err != nil {
		return err
	}
	store.AddArtifact(path, "file")
	if _, err := app.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := store.AddArtifact("unit:"+core.ServeUnit, "unit"); err != nil {
		return err
	}
	if c, err := webconf.LoadConfig(app); err == nil && c.Enabled {
		on = true
	}
	if !on {
		app.Logf("background service %s is off: no traffic stats, client expiry or egress failover; turn it on in System", core.ServeUnit)
		return nil
	}
	return app.SetServe(ctx, true)
}

const (
	KernelHook = "/etc/kernel/postinst.d/zz-awgx"
)

func installHook(app *core.App) error {
	store, err := app.Store()
	if err != nil {
		return err
	}
	hook := "#!/bin/sh\n" + app.Paths.Binary + " hook kernel-postinst \"$1\" || true\n"
	if err := os.MkdirAll(filepath.Dir(KernelHook), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(KernelHook, []byte(hook), 0o755); err != nil {
		return err
	}
	return store.AddArtifact(KernelHook, "file")
}
