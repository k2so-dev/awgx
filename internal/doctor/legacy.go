package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/netx"
)

type LegacyItem struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

func Legacy(ctx context.Context, e *Env) []LegacyItem {
	owned := map[string]bool{}
	if e.Store != nil {
		if insts, err := e.Store.Instances(); err == nil {
			for _, i := range insts {
				owned[i.Name] = true
			}
		}
		if ups, err := e.Store.Uplinks(); err == nil {
			for _, u := range ups {
				owned[u.Name] = true
			}
		}
	}
	var items []LegacyItem
	if links, err := netx.Links(ctx, e.Run); err == nil {
		for _, l := range links {
			if (l.Kind == "wireguard" || l.Kind == "amneziawg") && !owned[l.Name] {
				items = append(items, LegacyItem{Kind: "interface", Name: l.Name})
			}
		}
	}
	for _, dir := range []string{awg.ConfDir, "/etc/wireguard"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
		for _, f := range files {
			name := strings.TrimSuffix(filepath.Base(f), ".conf")
			if dir == awg.ConfDir && owned[name] {
				continue
			}
			items = append(items, LegacyItem{Kind: "config", Name: f, Path: f})
		}
	}
	if out, err := e.Run.Run(ctx, "dkms", "status", "amneziawg"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			ver, _, _ := strings.Cut(line, ",")
			if ver != "" && awg.KernelModuleVersion() != "" && !awg.Supports31(awg.KernelModuleVersion()) {
				items = append(items, LegacyItem{Kind: "dkms", Name: strings.TrimSpace(ver)})
			}
		}
	}
	if out, err := e.Run.Run(ctx, "docker", "ps", "-a", "--format", "{{.Names}}"); err == nil {
		for _, n := range strings.Fields(string(out)) {
			if strings.HasPrefix(n, "amnezia-awg") || strings.HasPrefix(n, "amnezia-wireguard") {
				items = append(items, LegacyItem{Kind: "container", Name: n})
			}
		}
	}
	if _, err := os.Stat("/usr/bin/amneziawg-go"); err == nil {
		items = append(items, LegacyItem{Kind: "binary", Name: "/usr/bin/amneziawg-go", Path: "/usr/bin/amneziawg-go"})
	}
	return items
}
