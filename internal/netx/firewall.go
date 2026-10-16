package netx

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	UFW       = "ufw"
	Firewalld = "firewalld"
)

func Firewall(ctx context.Context, run sysx.Runner) string {
	if out, err := run.Run(ctx, "ufw", "status"); err == nil && strings.Contains(string(out), "Status: active") {
		return UFW
	}
	if out, err := run.Run(ctx, "systemctl", "is-active", "firewalld"); err == nil && strings.TrimSpace(string(out)) == "active" {
		return Firewalld
	}
	return ""
}

func OpenFirewall(ctx context.Context, run sysx.Runner, fw, iface string, port int) error {
	return firewall(ctx, run, fw, iface, port, true)
}

func CloseFirewall(ctx context.Context, run sysx.Runner, fw, iface string, port int) error {
	return firewall(ctx, run, fw, iface, port, false)
}

func firewall(ctx context.Context, run sysx.Runner, fw, iface string, port int, open bool) error {
	p := strconv.Itoa(port) + "/udp"
	var cmds [][]string
	switch fw {
	case UFW:
		comment := "awgx " + iface
		if open {
			cmds = [][]string{
				{"ufw", "allow", p, "comment", comment},
			}
		} else {
			cmds = [][]string{
				{"ufw", "delete", "allow", p},
				{"ufw", "route", "delete", "allow", "in", "on", iface},
			}
		}
	case Firewalld:
		op := "--add-"
		if !open {
			op = "--remove-"
		}
		cmds = [][]string{
			{"firewall-cmd", "--permanent", op + "port=" + p},
			{"firewall-cmd", "--permanent", "--zone=trusted", op + "interface=" + iface},
			{"firewall-cmd", "--reload"},
		}
	default:
		return fmt.Errorf("unknown firewall %q", fw)
	}
	for _, c := range cmds {
		if _, err := run.Run(ctx, c[0], c[1:]...); err != nil && open {
			return err
		}
	}
	return nil
}

func FirewallArtifact(fw, iface string, port int) string {
	return fmt.Sprintf("firewall:%s:%s:%d", fw, iface, port)
}

func ParseFirewallArtifact(path string) (fw, iface string, port int, ok bool) {
	f := strings.Split(path, ":")
	if len(f) != 4 || f[0] != "firewall" {
		return "", "", 0, false
	}
	port, err := strconv.Atoi(f[3])
	if err != nil {
		return "", "", 0, false
	}
	return f[1], f[2], port, true
}

var ufwColumns = regexp.MustCompile(`\s{2,}`)

func UFWAllows(status string, port int) bool {
	ok := strings.Contains(status, "allow (incoming)")
	for _, line := range strings.Split(status, "\n") {
		if i := strings.Index(line, "]"); strings.HasPrefix(line, "[") && i > 0 {
			line = line[i+1:]
		}
		line, _, _ = strings.Cut(line, "#")
		f := ufwColumns.Split(strings.TrimSpace(line), -1)
		if len(f) < 3 || f[1] != "ALLOW" && f[1] != "ALLOW IN" {
			continue
		}
		to := strings.TrimSpace(strings.Replace(f[0], "(v6)", "", 1))
		if !strings.Contains(to, " on ") {
			ok = ok || to == "Anywhere" || ufwPortMatch(to, port)
		}
	}
	return ok
}

func ufwPortMatch(spec string, port int) bool {
	ports, proto, _ := strings.Cut(spec, "/")
	if proto != "" && proto != "udp" {
		return false
	}
	for _, p := range strings.Split(ports, ",") {
		lo, hi, isRange := strings.Cut(p, ":")
		if !isRange {
			hi = lo
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 == nil && err2 == nil && a <= port && port <= b {
			return true
		}
	}
	return false
}
