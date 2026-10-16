package core

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/sysx"
)

func (a *App) KernelPostinst(ctx context.Context, kernel string) (bool, error) {
	s, err := a.Store()
	if err != nil {
		return false, err
	}
	out, _ := a.Run.Run(ctx, "dkms", "status", "amneziawg", "-k", kernel)
	built := strings.Contains(string(out), "installed")
	if !built {
		a.Journal("kernel.hook", "module not built for %s: the tunnel will not start after a reboot into it", kernel)
	} else {
		a.Journal("kernel.hook", "module built for %s", kernel)
	}
	return built, s.SetSetting("kernel_hook_last", kernel)
}

func (a *App) FailedKernels() []string {
	images, _ := filepath.Glob("/boot/vmlinuz-*")
	if len(images) == 0 || !sysx.Has("dkms") {
		return nil
	}
	out, _ := a.Run.Run(context.Background(), "dkms", "status", "amneziawg")
	running := sysx.KernelRelease()
	var failed []string
	for _, img := range images {
		k := strings.TrimPrefix(filepath.Base(img), "vmlinuz-")
		if kernelOlder(k, running) {
			continue
		}
		if !builtFor(string(out), k) {
			failed = append(failed, k)
		}
	}
	return failed
}

func builtFor(status, kernel string) bool {
	for _, line := range strings.Split(status, "\n") {
		if strings.Contains(line, ", "+kernel+",") && strings.Contains(line, ": installed") {
			return true
		}
	}
	return false
}

func kernelOlder(a, b string) bool {
	x, y := kernelNumbers(a), kernelNumbers(b)
	for i := range min(len(x), len(y)) {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

func kernelNumbers(k string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(k, func(r rune) bool { return r < '0' || r > '9' }) {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}
