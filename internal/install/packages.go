package install

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	ppaFingerprint = "75C9DD72C799870E310542E24166F2C257290828"
	ppaURL         = "https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu"
	KeyringPath    = "/etc/apt/keyrings/awgx-amnezia.asc"
	SourcesPath    = "/etc/apt/sources.list.d/awgx-amnezia.list"
)

var debianDists = map[string]string{
	"12": "jammy",
	"13": "noble",
}

func PPADist(o sysx.OS) (string, error) {
	switch {
	case o.Ubuntu() && o.Codename != "":
		return o.Codename, nil
	case o.Debian():
		if d, ok := debianDists[o.VersionID]; ok {
			return d, nil
		}
	}
	return "", fmt.Errorf("unsupported OS %q: Debian 12/13 and Ubuntu 24.04/26.04 are supported", o.Pretty)
}

func installedPackages(ctx context.Context, run sysx.Runner) ([]string, error) {
	out, err := run.Run(ctx, "dpkg-query", "-W", "-f", "${Package} ${Status}\n")
	if err != nil {
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[3] == "installed" {
			pkgs = append(pkgs, f[0])
		}
	}
	return pkgs, nil
}

func fetchKey(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := "https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x" + ppaFingerprint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keyserver: %s", resp.Status)
	}
	key, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if err := checkKey(key, ppaFingerprint); err != nil {
		return nil, err
	}
	return key, nil
}

func checkKey(armored []byte, fingerprint string) error {
	if n := bytes.Count(armored, []byte("-----BEGIN PGP")); n != 1 {
		return fmt.Errorf("keyserver returned %d armored blocks, expected one key", n)
	}
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armored))
	if err != nil {
		return fmt.Errorf("keyserver returned no usable key: %w", err)
	}
	if len(ring) != 1 {
		return fmt.Errorf("keyserver returned %d keys, expected only %s", len(ring), fingerprint)
	}
	if got := strings.ToUpper(hex.EncodeToString(ring[0].PrimaryKey.Fingerprint[:])); got != fingerprint {
		return fmt.Errorf("keyserver returned key %s, expected %s", got, fingerprint)
	}
	return nil
}

var ErrModuleBuild = errors.New("the amneziawg kernel module does not build")

func installPackages(ctx context.Context, app *core.App, o sysx.OS, kernel string) ([]string, error) {
	dist, err := PPADist(o)
	if err != nil {
		return nil, err
	}
	before, err := installedPackages(ctx, app.Run)
	if err != nil {
		return nil, err
	}
	app.Logf("adding Amnezia PPA (%s)", dist)
	key, err := fetchKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch PPA key: %w", err)
	}
	if err := os.MkdirAll("/etc/apt/keyrings", 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(KeyringPath, key, 0o644); err != nil {
		return nil, err
	}
	line := fmt.Sprintf("deb [signed-by=%s] %s %s main\n", KeyringPath, ppaURL, dist)
	if err := os.WriteFile(SourcesPath, []byte(line), 0o644); err != nil {
		return nil, err
	}
	app.Logf("apt-get update")
	if _, err := app.Run.Run(ctx, "apt-get", "update"); err != nil {
		return nil, err
	}
	pkgs := []string{"dkms", "linux-headers-" + kernel, "amneziawg-dkms", "amneziawg-tools", "nftables"}
	app.Logf("installing %s (builds the kernel module, takes a minute)", strings.Join(pkgs, " "))
	args := append([]string{"install", "-y", "--no-install-recommends"}, pkgs...)
	_, installErr := app.Run.Run(ctx, "apt-get", args...)
	after, err := installedPackages(ctx, app.Run)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, p := range after {
		if !slices.Contains(before, p) {
			added = append(added, p)
		}
	}
	if installErr != nil {
		rollbackPackages(ctx, app, added)
		if log := dkmsBuildLog(); log != "" {
			return nil, fmt.Errorf("%w for kernel %s (see %s); changes were rolled back", ErrModuleBuild, kernel, log)
		}
		return nil, fmt.Errorf("%w; changes were rolled back", installErr)
	}
	return added, nil
}

const dkmsLogCopy = "/var/log/awgx-dkms-make.log"

func rollbackPackages(ctx context.Context, app *core.App, added []string) {
	app.Logf("rolling back packages")
	log := dkmsBuildLog()
	if log != "" {
		if data, err := os.ReadFile(log); err == nil {
			os.WriteFile(dkmsLogCopy, data, 0o600)
		}
	}
	app.Run.Run(ctx, "dpkg", "--purge", "--force-remove-reinstreq", "amneziawg-dkms")
	if len(added) > 0 {
		app.Run.Run(ctx, "apt-get", append([]string{"purge", "-y"}, added...)...)
	}
	os.Remove(SourcesPath)
	os.Remove(KeyringPath)
	app.Run.Run(ctx, "apt-get", "update")
}

func dkmsBuildLog() string {
	if sysx.Exists(dkmsLogCopy) {
		return dkmsLogCopy
	}
	logs, _ := filepath.Glob("/var/lib/dkms/amneziawg/*/build/make.log")
	if len(logs) == 0 {
		return ""
	}
	return logs[0]
}
