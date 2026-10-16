package upgrade

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/sysx"
)

var Packages = []string{"amneziawg-dkms", "amneziawg-tools"}

func PackageDir(app *core.App) string {
	return filepath.Join(app.Paths.DataDir, "packages")
}

func Versions(ctx context.Context, run sysx.Runner) (map[string]string, error) {
	out, err := run.Run(ctx, "dpkg-query", append([]string{"-W", "-f", "${Package} ${Version}\n"}, Packages...)...)
	if err != nil {
		return nil, err
	}
	v := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, ver, ok := strings.Cut(line, " "); ok {
			v[name] = ver
		}
	}
	return v, nil
}

func debName(pkg, version string) string {
	return pkg + "_" + strings.ReplaceAll(version, ":", "%3a") + "_"
}

func CacheDebs(ctx context.Context, app *core.App) error {
	versions, err := Versions(ctx, app.Run)
	if err != nil {
		return err
	}
	dir := PackageDir(app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for pkg, ver := range versions {
		matches, _ := filepath.Glob(filepath.Join("/var/cache/apt/archives", debName(pkg, ver)+"*.deb"))
		for _, m := range matches {
			if err := copyFile(m, filepath.Join(dir, filepath.Base(m))); err != nil {
				return err
			}
		}
	}
	return nil
}

func cachedDebs(app *core.App, versions map[string]string) ([]string, error) {
	var out []string
	for pkg, ver := range versions {
		matches, _ := filepath.Glob(filepath.Join(PackageDir(app), debName(pkg, ver)+"*.deb"))
		if len(matches) == 0 {
			return nil, fmt.Errorf("no cached package for %s %s", pkg, ver)
		}
		out = append(out, matches[0])
	}
	return out, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
