package netx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"slices"

	"github.com/k2so-dev/awgx/internal/sysx"
	"golang.org/x/sys/unix"
)

const (
	NS            = "awgx"
	NSPath        = "/run/netns/" + NS
	DataplaneUnit = "awgx-dataplane.service"
	EchoDev       = "awgx-echo"
)

var nsSysctls = []string{
	"net.ipv4.ip_forward=1",
	"net.ipv6.conf.all.forwarding=1",
	"net.ipv4.conf.all.rp_filter=0",
	"net.ipv4.conf.default.rp_filter=0",
	"net.ipv4.conf.all.src_valid_mark=0",
}

var inheritSysctls = []string{
	"net.ipv4.tcp_congestion_control",
	"net.ipv4.tcp_rmem",
	"net.ipv4.tcp_wmem",
	"net.ipv4.tcp_mtu_probing",
	"net.ipv4.tcp_notsent_lowat",
}

func NSExists() bool {
	return sysx.Exists(NSPath)
}

func InNS(run sysx.Runner) sysx.Runner {
	return sysx.InNS{Runner: run, Name: NS}
}

func EnsureNS(ctx context.Context, run sysx.Runner) error {
	if !NSExists() {
		if _, err := run.Run(ctx, "ip", "netns", "add", NS); err != nil {
			return err
		}
	}
	ns := InNS(run)
	if _, err := ns.Run(ctx, "ip", "link", "set", "lo", "up"); err != nil {
		return err
	}
	want := append([]string(nil), nsSysctls...)
	for _, k := range inheritSysctls {
		if v := sysx.Sysctl(k); v != "" {
			want = append(want, k+"="+v)
		}
	}
	for _, kv := range want {
		ns.Run(ctx, "sysctl", "-q", "-e", "-w", kv)
	}
	if _, err := ns.Run(ctx, "ip", "link", "show", "dev", EchoDev); err != nil {
		if _, err := ns.Run(ctx, "ip", "tuntap", "add", "dev", EchoDev, "mode", "tun"); err != nil {
			return nil
		}
	}
	ns.Run(ctx, "sysctl", "-q", "-w", "net.ipv4.conf."+EchoDev+".rp_filter=0")
	ns.Run(ctx, "ip", "link", "set", EchoDev, "up")
	return nil
}

func DeleteNS(ctx context.Context, run sysx.Runner) error {
	if !NSExists() {
		return nil
	}
	_, err := run.Run(ctx, "ip", "netns", "del", NS)
	return err
}

func Do(fn func() error) error {
	runtime.LockOSThread()
	self, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer self.Close()
	target, err := os.Open(NSPath)
	if err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("network namespace %s: %w", NS, err)
	}
	defer target.Close()
	if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	ferr := fn()
	if err := unix.Setns(int(self.Fd()), unix.CLONE_NEWNET); err != nil {
		return errors.Join(ferr, err)
	}
	runtime.UnlockOSThread()
	return ferr
}

func LinkExists(name string) bool {
	if !NSExists() {
		return false
	}
	found := false
	Do(func() error {
		_, err := net.InterfaceByName(name)
		found = err == nil
		return nil
	})
	return found
}

func RootAddrs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		ip := p.Addr().Unmap()
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || slices.Contains(out, ip) {
			continue
		}
		out = append(out, ip)
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out
}

func RootLinkExists(name string) bool {
	return sysx.Exists("/sys/class/net/" + name)
}

type linkStats struct {
	IfName   string `json:"ifname"`
	MTU      int    `json:"mtu"`
	LinkType string `json:"link_type"`
	Info     struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
	Stats struct {
		Rx struct {
			Bytes   uint64 `json:"bytes"`
			Packets uint64 `json:"packets"`
		} `json:"rx"`
		Tx struct {
			Bytes   uint64 `json:"bytes"`
			Packets uint64 `json:"packets"`
		} `json:"tx"`
	} `json:"stats64"`
}

func readLink(ctx context.Context, run sysx.Runner, name string) (linkStats, error) {
	out, err := run.Run(ctx, "ip", "-j", "-s", "-d", "link", "show", "dev", name)
	if err != nil {
		return linkStats{}, err
	}
	var raw []linkStats
	if err := json.Unmarshal(out, &raw); err != nil || len(raw) == 0 {
		return linkStats{}, fmt.Errorf("ip link show %s: unexpected output", name)
	}
	return raw[0], nil
}

func LinkKind(ctx context.Context, run sysx.Runner, name string) string {
	l, err := readLink(ctx, run, name)
	if err != nil {
		return ""
	}
	return l.Info.Kind
}

func LinkMTU(ctx context.Context, run sysx.Runner, name string) int {
	l, err := readLink(ctx, run, name)
	if err != nil {
		return 0
	}
	return l.MTU
}
