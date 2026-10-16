package purge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/k2so-dev/awgx/internal/backup"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/k2so-dev/awgx/internal/xui"
)

type Options struct {
	Backup      bool
	KeepBinary  bool
	KeepInbound bool
}

type Result struct {
	Backup  string   `json:"backup,omitempty"`
	Removed []string `json:"removed"`
	Kept    []string `json:"kept,omitempty"`
}

var dropInGlobs = []string{
	"/etc/sysctl.d/90-awgx-*.conf",
	"/etc/modules-load.d/90-awgx-*.conf",
	"/etc/systemd/system/awgx-*",
}

func Run(ctx context.Context, app *core.App, o Options) (Result, error) {
	var res Result
	if !sysx.IsRoot() {
		return res, errors.New("purge must run as root")
	}
	var files, dirs, sysctls, packages, units, instances, firewall []string
	if store, err := app.Store(); err == nil {
		insts, _ := store.Instances()
		for _, i := range insts {
			instances = append(instances, i.Name)
		}
		artifacts, err := store.Artifacts()
		if err != nil {
			return res, err
		}
		for _, a := range artifacts {
			switch a.Kind {
			case "file":
				files = append(files, a.Path)
			case "dir":
				dirs = append(dirs, a.Path)
			case "sysctl":
				sysctls = append(sysctls, strings.TrimPrefix(a.Path, "sysctl:"))
			case "package":
				packages = append(packages, strings.TrimPrefix(a.Path, "package:"))
			case "unit":
				units = append(units, strings.TrimPrefix(a.Path, "unit:"))
			case "firewall":
				firewall = append(firewall, a.Path)
			}
		}
	} else if !errors.Is(err, core.ErrNotInstalled) {
		return res, err
	}

	if o.Backup && app.Installed() {
		name, err := app.Backup("")
		if err != nil {
			return res, err
		}
		res.Backup = name
		app.Logf("backup: %s", name)
	}
	app.Run.Run(ctx, "systemctl", "stop", core.ServeUnit)
	for _, u := range units {
		if _, err := app.Run.Run(ctx, "systemctl", "disable", "--now", u); err == nil {
			res.Removed = append(res.Removed, "systemctl disable --now "+u)
		}
	}
	if e, err := egress.New(app); err == nil {
		if insts, err := e.Store.Instances(); err == nil {
			for _, inst := range insts {
				selftest.Cleanup(ctx, app, inst)
			}
		}
		e.Down(ctx)
		if !o.KeepInbound {
			if err := xui.Disown(ctx, e); err != nil {
				app.Logf("3x-ui inbound was not removed: %v", err)
			}
		}
	}
	app.Close()

	run := func(name string, args ...string) {
		if _, err := app.Run.Run(ctx, name, args...); err == nil {
			res.Removed = append(res.Removed, name+" "+strings.Join(args, " "))
		}
	}
	for _, i := range instances {
		if app.InterfaceUp(i) {
			run("ip", "netns", "exec", netx.NS, "ip", "link", "del", i)
		}
		if netx.RootLinkExists(i) {
			run("ip", "link", "del", i)
		}
	}
	if netx.NSExists() && netx.DeleteNS(ctx, app.Run) == nil {
		res.Removed = append(res.Removed, "ip netns del "+netx.NS)
	}
	os.RemoveAll("/run/awgx")
	for _, f := range firewall {
		if fw, iface, port, ok := netx.ParseFirewallArtifact(f); ok && netx.CloseFirewall(ctx, app.Run, fw, iface, port) == nil {
			res.Removed = append(res.Removed, fmt.Sprintf("%s rules for %s %d/udp", fw, iface, port))
		}
	}
	if netx.TableExists(ctx, app.Run) {
		run("nft", "delete", "table", "inet", netx.Table)
	}

	if len(packages) > 0 {
		safe, kept := removable(ctx, app, packages)
		res.Kept = append(res.Kept, kept...)
		if len(safe) > 0 {
			app.Logf("removing packages: %s", strings.Join(safe, " "))
			args := append([]string{"purge", "-y"}, safe...)
			if _, err := app.Run.Run(ctx, "apt-get", args...); err != nil {
				return res, err
			}
			res.Removed = append(res.Removed, safe...)
		}
	}
	removeDKMS(ctx, app, &res)
	app.Run.Run(ctx, "rmmod", "amneziawg")

	for _, f := range files {
		if f == app.Paths.Binary {
			continue
		}
		if os.Remove(f) == nil {
			res.Removed = append(res.Removed, f)
		}
	}
	for _, g := range dropInGlobs {
		matches, _ := filepath.Glob(g)
		for _, m := range matches {
			if os.Remove(m) == nil {
				res.Removed = append(res.Removed, m)
			}
		}
	}
	for _, kv := range sysctls {
		run("sysctl", "-w", kv)
	}
	if slices.Contains(files, "/etc/apt/sources.list.d/awgx-amnezia.list") {
		app.Run.Run(ctx, "apt-get", "update")
	}
	app.Run.Run(ctx, "systemctl", "daemon-reload")

	if err := os.RemoveAll(app.Paths.StateDir); err == nil {
		res.Removed = append(res.Removed, app.Paths.StateDir)
	}
	removeIfEmpty(app.Paths.ConfDir)
	removeIfEmpty(filepath.Dir(app.Paths.ConfDir))
	for _, d := range dirs {
		switch {
		case d == "/var/lib/dkms" && installed(ctx, app, "dkms"):
			continue
		case d == "/var/lib/dkms":
			os.RemoveAll(d)
		default:
			removeIfEmpty(d)
		}
		if !sysx.Exists(d) {
			res.Removed = append(res.Removed, d)
		}
	}
	os.RemoveAll(filepath.Join(app.Paths.DataDir, "packages"))
	os.Remove(app.Paths.BackupDir)
	removeIfEmpty(app.Paths.DataDir)
	if res.Backup != "" {
		res.Kept = append(res.Kept, res.Backup)
	} else if sysx.Exists(app.Paths.BackupDir) {
		res.Kept = append(res.Kept, app.Paths.BackupDir)
	}
	if !o.KeepBinary && os.Remove(app.Paths.Binary) == nil {
		res.Removed = append(res.Removed, app.Paths.Binary)
	}
	return res, nil
}

func removable(ctx context.Context, app *core.App, packages []string) ([]string, []string) {
	args := append([]string{"-s", "purge"}, packages...)
	out, err := app.Run.Run(ctx, "apt-get", args...)
	if err != nil {
		return nil, packages
	}
	var extra []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && (f[0] == "Purg" || f[0] == "Remv") && !slices.Contains(packages, f[1]) {
			extra = append(extra, f[1])
		}
	}
	if len(extra) == 0 {
		return packages, nil
	}
	app.Logf("keeping packages installed by awgx because other packages depend on them: %s", strings.Join(extra, " "))
	var safe, kept []string
	for _, p := range packages {
		if strings.HasPrefix(p, "amneziawg") {
			safe = append(safe, p)
		} else {
			kept = append(kept, p)
		}
	}
	return safe, kept
}

func removeDKMS(ctx context.Context, app *core.App, res *Result) {
	out, err := app.Run.Run(ctx, "dkms", "status", "amneziawg")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		mod, _, _ := strings.Cut(line, ",")
		mod = strings.TrimSpace(mod)
		if strings.HasPrefix(mod, "amneziawg/") {
			if _, err := app.Run.Run(ctx, "dkms", "remove", mod, "--all"); err == nil {
				res.Removed = append(res.Removed, "dkms "+mod)
			}
			os.RemoveAll("/usr/src/" + strings.ReplaceAll(mod, "/", "-"))
		}
	}
}

func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		os.Remove(dir)
	}
}

func Legacy(ctx context.Context, app *core.App, items []doctor.LegacyItem, keep bool) error {
	var paths []string
	for _, i := range items {
		if i.Path != "" {
			paths = append(paths, i.Path)
		}
	}
	if keep && len(paths) > 0 {
		name, err := backup.Create(app.Paths.BackupDir, paths, nil)
		if err != nil {
			return err
		}
		app.Logf("legacy backup: %s", name)
	}
	for _, i := range items {
		var err error
		switch i.Kind {
		case "interface":
			app.Run.Run(ctx, "systemctl", "disable", "--now", "awg-quick@"+i.Name)
			app.Run.Run(ctx, "systemctl", "disable", "--now", "wg-quick@"+i.Name)
			if sysx.Exists("/sys/class/net/" + i.Name) {
				_, err = app.Run.Run(ctx, "ip", "link", "del", i.Name)
			}
		case "config", "binary":
			err = os.Remove(i.Path)
		case "dkms":
			_, err = app.Run.Run(ctx, "dkms", "remove", i.Name, "--all")
		case "container":
			_, err = app.Run.Run(ctx, "docker", "rm", "-f", i.Name)
		}
		if err != nil {
			return err
		}
		app.Logf("removed %s %s", i.Kind, i.Name)
	}
	return nil
}

func installed(ctx context.Context, app *core.App, pkg string) bool {
	out, err := app.Run.Run(ctx, "dpkg-query", "-W", "-f", "${db:Status-Status}", pkg)
	return err == nil && strings.TrimSpace(string(out)) == "installed"
}
