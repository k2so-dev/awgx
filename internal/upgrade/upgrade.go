package upgrade

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

type Result struct {
	Before     map[string]string `json:"before"`
	After      map[string]string `json:"after"`
	RolledBack bool              `json:"rolled_back"`
}

func Modules(ctx context.Context, app *core.App) (Result, error) {
	var res Result
	if !sysx.IsRoot() {
		return res, errors.New("upgrade must run as root")
	}
	insts, err := app.Instances()
	if err != nil {
		return res, err
	}
	if res.Before, err = Versions(ctx, app.Run); err != nil {
		return res, err
	}
	if err := CacheDebs(ctx, app); err != nil {
		return res, err
	}
	app.Run.Run(ctx, "apt-mark", append([]string{"unhold"}, Packages...)...)
	app.Logf("checking %v", Packages)
	if _, err := app.Run.Run(ctx, "apt-get", "update"); err != nil {
		return res, err
	}
	args := append([]string{"install", "-y", "--only-upgrade"}, Packages...)
	_, upErr := app.Run.Run(ctx, "apt-get", args...)
	res.After, _ = Versions(ctx, app.Run)
	if upErr == nil && maps.Equal(res.Before, res.After) {
		app.Logf("already up to date")
		return res, nil
	}
	if upErr == nil {
		upErr = reload(ctx, app)
	}
	if upErr == nil {
		for _, inst := range insts {
			if upErr = selftest.Check(ctx, app, inst); upErr != nil {
				break
			}
		}
		if upErr == nil {
			app.Journal("upgrade", "%v -> %v", res.Before, res.After)
			return res, nil
		}
	}
	app.Logf("upgrade failed: %v; rolling back", upErr)
	if err := rollback(ctx, app, res.Before); err != nil {
		return res, fmt.Errorf("upgrade failed: %v; rollback failed: %w", upErr, err)
	}
	res.RolledBack = true
	app.Run.Run(ctx, "apt-mark", append([]string{"hold"}, Packages...)...)
	app.Logf("packages are on hold so apt upgrade does not bring the broken version back; awgx upgrade retries")
	res.After, _ = Versions(ctx, app.Run)
	app.Journal("upgrade.rollback", "%v", upErr)
	return res, fmt.Errorf("upgrade failed and was rolled back: %w", upErr)
}

func reload(ctx context.Context, app *core.App) error {
	app.Run.Run(ctx, "systemctl", "stop", core.DataplaneUnit)
	app.DownLinks(ctx)
	if _, err := app.Run.Run(ctx, "rmmod", "amneziawg"); err != nil && sysx.Exists("/sys/module/amneziawg") {
		app.Run.Run(ctx, "systemctl", "start", core.DataplaneUnit)
		return fmt.Errorf("unload the old module: %w", err)
	}
	if _, err := app.Run.Run(ctx, "modprobe", "amneziawg"); err != nil {
		return err
	}
	if v := awg.KernelModuleVersion(); !awg.Supports31(v) {
		return fmt.Errorf("module %q does not support AWG 3.1", v)
	}
	if _, err := app.Run.Run(ctx, "systemctl", "start", core.DataplaneUnit); err != nil {
		return err
	}
	if err := app.UpAll(ctx); err != nil {
		return err
	}
	app.LinksChanged(ctx)
	return nil
}

func rollback(ctx context.Context, app *core.App, versions map[string]string) error {
	debs, err := cachedDebs(app, versions)
	if err != nil {
		return err
	}
	if _, err := app.Run.Run(ctx, "dpkg", append([]string{"-i"}, debs...)...); err != nil {
		return err
	}
	return reload(ctx, app)
}

func SelfUpdate(ctx context.Context, app *core.App, channel string) (string, error) {
	tag, err := Self(ctx, app.Paths.Binary, channel, app.Version)
	if err != nil || tag == "" {
		return tag, err
	}
	if s, err := app.Store(); err == nil {
		s.SetSetting(state.SettingUpdate, "")
	}
	app.Journal("upgrade", "awgx %s -> %s", app.Version, tag)
	return tag, nil
}

func SetAutoCheck(app *core.App, on bool) error {
	s, err := app.Store()
	if err != nil {
		return err
	}
	if err := s.SetSetting(core.SettingUpdateCheck, fmtx.OnOff(on)); err != nil || on {
		return err
	}
	return s.SetSetting(state.SettingUpdate, "")
}

func CheckUpdate(ctx context.Context, app *core.App) (string, error) {
	tag, err := CheckSelf(ctx, app.Version)
	if err != nil {
		return "", err
	}
	if s, err := app.Store(); err == nil {
		s.SetSetting(state.SettingUpdate, tag)
	}
	return tag, nil
}
