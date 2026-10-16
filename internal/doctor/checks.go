package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

var checks = []check{
	{"kernel.headers", "kernel", checkHeaders, false},
	{"kernel.module", "kernel", checkModule, false},
	{"kernel.dkms", "kernel", checkDKMS, false},
	{"kernel.secureboot", "kernel", checkSecureBoot, false},
	{"virt", "virtualization", checkVirt, false},
	{"awg.tools", "awg", checkTools, false},
	{"awg.mode", "awg", checkMode, true},
	{"awg.legacy", "awg", checkLegacy, false},
	{"net.dataplane", "network", checkDataplane, false},
	{"net.uplink", "network", checkUplink, false},
	{"net.ipv6", "network", checkIPv6, false},
	{"net.port", "network", checkPort, true},
	{"firewall", "firewall", checkFirewall, true},
	{"time.ntp", "time", checkNTP, false},
	{"buffers", "buffers", checkBuffers, false},
	{"perf.bbr", "perf", checkBBR, false},
	{"perf.shaping", "perf", checkShaping, false},
	{"uplink.port", "network", checkUplinkPort, false},
	{"dns", "dns", checkDNS, false},
	{"awgx.state", "awgx", checkState, true},
	{"awgx.serve", "awgx", checkServe, false},
	{"awgx.update", "awgx", checkUpdate, false},
}

func checkUpdate(ctx context.Context, e *Env) Result {
	if e.Store == nil {
		return ok("not installed yet")
	}
	if v, _ := e.Store.Setting(state.SettingUpdate); v != "" {
		return ok("awgx " + v + " is available: awgx upgrade --self")
	}
	return ok("no update notice")
}

func checkServe(ctx context.Context, e *Env) Result {
	if e.Store == nil {
		return ok("not installed yet")
	}
	out, _ := e.Run.Run(ctx, "systemctl", "is-enabled", "awgx-serve.service")
	if strings.TrimSpace(string(out)) == "enabled" {
		return ok("awgx-serve is on")
	}
	var needs []string
	for _, inst := range e.Instances {
		peers, _ := e.Store.Peers(inst.Name)
		if slices.ContainsFunc(peers, func(p state.Peer) bool { return !p.ExpiresAt.IsZero() }) {
			needs = append(needs, "clients with an expiry date")
			break
		}
	}
	profiles, _ := e.Store.Profiles()
	if slices.ContainsFunc(profiles, func(p state.EgressProfile) bool { return len(p.Hops) > 1 || p.Fallback != "" }) {
		needs = append(needs, "egress failover")
	}
	if slices.ContainsFunc(profiles, func(p state.EgressProfile) bool { return p.Kind == "socks5" || p.Kind == "xui" }) {
		needs = append(needs, "SOCKS5 and 3x-ui egress")
	}
	if len(needs) > 0 {
		var fix *Fix
		if e.StartServe != nil {
			fix = &Fix{Description: "systemctl enable --now awgx-serve", Apply: e.StartServe}
		}
		return warn("awgx-serve is off but "+strings.Join(needs, " and ")+" need it", fix)
	}
	return ok("awgx-serve is off: no traffic stats, client expiry or egress failover")
}

func checkHeaders(ctx context.Context, e *Env) Result {
	if sysx.Exists(filepath.Join("/lib/modules", e.Kernel, "build")) {
		return ok("headers for " + e.Kernel + " are installed")
	}
	pkg := "linux-headers-" + e.Kernel
	return warn("headers for "+e.Kernel+" are missing, the module cannot be built", &Fix{
		Description: "apt-get install " + pkg,
		Apply: func(ctx context.Context) error {
			_, err := e.Run.Run(ctx, "apt-get", "install", "-y", pkg)
			return err
		},
	})
}

func checkModule(ctx context.Context, e *Env) Result {
	v := awg.KernelModuleVersion()
	if v == "" {
		if _, err := e.Run.Run(ctx, "modinfo", "-F", "version", "amneziawg"); err != nil {
			if e.Instance != nil {
				return fail("amneziawg module is not built for kernel "+e.Kernel+": the tunnel cannot start", dkmsFix(e, []string{e.Kernel}))
			}
			return warn("amneziawg module is not installed (awgx install)", nil)
		}
		return warn("amneziawg module is installed but not loaded", &Fix{
			Description: "modprobe amneziawg",
			Apply: func(ctx context.Context) error {
				_, err := e.Run.Run(ctx, "modprobe", "amneziawg")
				return err
			},
		})
	}
	if !awg.Supports31(v) {
		return fail("amneziawg module "+v+" does not support AWG 3.1 (awgx install)", nil)
	}
	return ok("amneziawg module " + v + " is loaded")
}

func checkDKMS(ctx context.Context, e *Env) Result {
	if !sysx.Has("dkms") {
		return ok("dkms is not installed yet")
	}
	out, err := e.Run.Run(ctx, "dkms", "status", "amneziawg")
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return ok("no amneziawg DKMS module registered")
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, e.Kernel) && strings.Contains(line, "installed") {
			return ok("DKMS module is built for " + e.Kernel)
		}
	}
	return fail("amneziawg DKMS module is not built for running kernel "+e.Kernel, nil)
}

const secureBootVar = "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

func checkSecureBoot(ctx context.Context, e *Env) Result {
	b, err := os.ReadFile(secureBootVar)
	if err != nil || len(b) < 5 || b[4] != 1 {
		return ok("Secure Boot is off")
	}
	out, err := e.Run.Run(ctx, "modinfo", "-F", "signer", "amneziawg")
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return ok("Secure Boot is on, module is signed by " + strings.TrimSpace(string(out)))
	}
	return warn("Secure Boot is on: the DKMS module must be signed with an enrolled MOK key", nil)
}

var containerVirt = []string{"lxc", "lxc-libvirt", "openvz", "systemd-nspawn", "docker", "podman", "wsl"}

func checkVirt(ctx context.Context, e *Env) Result {
	out, _ := e.Run.Run(ctx, "systemd-detect-virt")
	v := strings.TrimSpace(string(out))
	if v == "" {
		v = "unknown"
	}
	if slices.Contains(containerVirt, v) {
		return fail("running in "+v+": kernel modules cannot be loaded, kernel mode is impossible", nil)
	}
	if v == "none" {
		return ok("bare metal")
	}
	return ok("virtualization: " + v)
}

func checkTools(ctx context.Context, e *Env) Result {
	if !sysx.Has("awg") {
		return warn("amneziawg-tools are not installed (awgx install)", nil)
	}
	v, err := awg.Tool{Run: e.Run}.ToolsVersion(ctx)
	if err != nil {
		return fail(err.Error(), nil)
	}
	if !awg.Supports31(v) {
		return fail("amneziawg-tools "+v+" do not support AWG 3.1 (awgx install)", nil)
	}
	return ok("amneziawg-tools " + v)
}

func checkLegacy(ctx context.Context, e *Env) Result {
	items := Legacy(ctx, e)
	if len(items) == 0 {
		return ok("no foreign WireGuard/AmneziaWG setups found")
	}
	var names []string
	for _, l := range items {
		names = append(names, l.Name)
	}
	return warn("found: "+strings.Join(names, ", ")+" (awgx setup --remove-old)", nil)
}

func checkDataplane(ctx context.Context, e *Env) Result {
	if e.Store == nil {
		return ok("awgx is not installed")
	}
	restart := &Fix{
		Description: "systemctl restart " + netx.DataplaneUnit,
		Apply: func(ctx context.Context) error {
			_, err := e.Run.Run(ctx, "systemctl", "restart", netx.DataplaneUnit)
			return err
		},
	}
	if !sysx.Exists("/etc/systemd/system/" + netx.DataplaneUnit) {
		return fail("the data plane is not installed, this setup was made by an older awgx: run awgx reinstall", nil)
	}
	if _, err := e.Run.Run(ctx, "systemctl", "is-active", "--quiet", netx.DataplaneUnit); err != nil {
		return fail(netx.DataplaneUnit+" is not running: peers have no network", restart)
	}
	if !netx.NSExists() {
		return fail("network namespace "+netx.NS+" is missing", restart)
	}
	out, err := netx.InNS(e.Run).Run(ctx, "sysctl", "-n", "net.ipv4.ip_forward")
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		return fail("forwarding is off in network namespace "+netx.NS, &Fix{
			Description: "enable forwarding in " + netx.NS,
			Apply:       func(ctx context.Context) error { return netx.EnsureNS(ctx, e.Run) },
		})
	}
	return ok("data plane runs in network namespace " + netx.NS)
}

func checkUplink(ctx context.Context, e *Env) Result {
	up, err := netx.DefaultUplink(ctx, e.Run)
	if err != nil {
		return fail("no default route: "+err.Error(), nil)
	}
	msg := fmt.Sprintf("uplink %s, MTU %d, source %s", up.Dev, up.MTU, up.Source)
	if up.Source.IsPrivate() && (e.Instance == nil || e.Instance.Endpoint == "") {
		return warn(msg+": source address is private, pass the public address with --endpoint", nil)
	}
	return ok(msg)
}

func checkIPv6(ctx context.Context, e *Env) Result {
	if !netx.HasIPv6Route(ctx, e.Run) {
		if e.Instance != nil && e.Instance.IPv6() {
			return warn("no IPv6 default route but IPv6 is on in "+e.Instance.Name+": peers get IPv6 addresses without IPv6 internet, turn it off", nil)
		}
		return ok("no IPv6 default route, IPv6 in the tunnel is off")
	}
	out, _ := e.Run.Run(ctx, "ip", "-j", "-6", "route", "show", "default")
	var routes []struct {
		Dev      string `json:"dev"`
		Protocol string `json:"protocol"`
	}
	json.Unmarshal(out, &routes)
	for _, r := range routes {
		if r.Protocol == "ra" && sysx.Sysctl("net.ipv6.conf.all.forwarding") == "1" && sysx.ReadString("/proc/sys/net/ipv6/conf/"+r.Dev+"/accept_ra") == "1" {
			return warn("IPv6 default route on "+r.Dev+" comes from router advertisements, which the kernel ignores once IPv6 forwarding is on: the route expires and IPv6 is lost",
				e.dropIn("accept-ra", "net/ipv6/conf/"+r.Dev+"/accept_ra = 2"))
		}
	}
	return ok("IPv6 default route present")
}

func checkPort(ctx context.Context, e *Env) Result {
	out, _ := e.Run.Run(ctx, "ss", "-Hulnp", "sport = :443")
	busy443 := strings.TrimSpace(string(out)) != ""
	if e.Instance == nil {
		if busy443 {
			return ok("443/udp is used by another process, quic profile will need another server")
		}
		return ok("no instance yet")
	}
	inst := e.Instance
	if inst.I1.Profile == "quic" && inst.ListenPort == 443 && strings.Contains(string(out), "users:") {
		return fail("quic profile on 443/udp while another process listens there: visitors' browsers will send QUIC into AWG", nil)
	}
	out, _ = e.Run.Run(ctx, "ss", "-Hulnp", fmt.Sprintf("sport = :%d", inst.ListenPort))
	if strings.TrimSpace(string(out)) == "" {
		return warn(fmt.Sprintf("nothing listens on %d/udp, interface %s is down", inst.ListenPort, inst.Name), nil)
	}
	if strings.Contains(string(out), "users:") {
		return fail(fmt.Sprintf("port %d/udp is taken by another process", inst.ListenPort), nil)
	}
	return ok(fmt.Sprintf("%s listens on %d/udp", inst.Name, inst.ListenPort))
}

func checkFirewall(ctx context.Context, e *Env) Result {
	var notes []string
	fw := netx.Firewall(ctx, e.Run)
	var missing string
	if fw != "" && e.Instance != nil {
		missing = firewallMissing(ctx, e, fw)
	}
	switch {
	case fw == "":
	case e.Instance == nil:
		notes = append(notes, fw+" is active, awgx opens instance ports in it")
	case missing != "":
		inst := e.Instance
		return warn(fmt.Sprintf("%s is active and does not allow %s", fw, missing), &Fix{
			Description: fmt.Sprintf("allow %d/udp in %s", inst.ListenPort, fw),
			Apply: func(ctx context.Context) error {
				return netx.OpenFirewall(ctx, e.Run, fw, inst.Name, inst.ListenPort)
			},
		})
	default:
		notes = append(notes, fw+" allows the port")
	}
	if sysx.Has("iptables-legacy") {
		if out, err := e.Run.Run(ctx, "iptables-legacy", "-S"); err == nil && len(strings.Split(strings.TrimSpace(string(out)), "\n")) > 3 {
			return warn("iptables-legacy rules present: they are evaluated besides nftables", nil)
		}
	}
	notes = append(notes, "peer traffic stays in the "+netx.NS+" network namespace, the host firewall only sees the UDP port")
	return ok(strings.Join(notes, ", "))
}

func firewallMissing(ctx context.Context, e *Env, fw string) string {
	inst := e.Instance
	var portOK bool
	if fw == netx.UFW {
		out, _ := e.Run.Run(ctx, "ufw", "status", "verbose")
		portOK = netx.UFWAllows(string(out), inst.ListenPort)
	} else {
		ports, _ := e.Run.Run(ctx, "firewall-cmd", "--list-ports")
		portOK = slices.Contains(strings.Fields(string(ports)), fmt.Sprintf("%d/udp", inst.ListenPort))
	}
	if !portOK {
		return fmt.Sprintf("%d/udp", inst.ListenPort)
	}
	return ""
}

var ntpDaemons = []string{"chrony", "chronyd", "ntp", "ntpd", "ntpsec", "openntpd"}

func checkNTP(ctx context.Context, e *Env) Result {
	out, err := e.Run.Run(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	if err == nil && strings.TrimSpace(string(out)) == "yes" {
		return ok("clock is NTP synchronized")
	}
	for _, d := range ntpDaemons {
		if out, err := e.Run.Run(ctx, "systemctl", "is-active", d); err == nil && strings.TrimSpace(string(out)) == "active" {
			return warn("clock is not NTP synchronized yet, "+d+" is running", nil)
		}
	}
	if out, err := e.Run.Run(ctx, "systemd-detect-virt", "--container"); err == nil && strings.TrimSpace(string(out)) != "none" {
		return warn("clock is not NTP synchronized, in a container the host keeps the time", nil)
	}
	var cmds [][]string
	if _, err := e.Run.Run(ctx, "systemctl", "cat", "systemd-timesyncd"); err != nil {
		cmds = append(cmds, []string{"apt-get", "install", "-y", "systemd-timesyncd"})
	}
	cmds = append(cmds, []string{"systemctl", "enable", "--now", "systemd-timesyncd"})
	var desc []string
	for _, c := range cmds {
		desc = append(desc, strings.Join(c, " "))
	}
	return warn("clock is not NTP synchronized, handshakes depend on time", &Fix{
		Description: strings.Join(desc, "; "),
		Apply: func(ctx context.Context) error {
			for _, c := range cmds {
				if _, err := e.Run.Run(ctx, c[0], c[1:]...); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

const (
	minBuffer = 2500000
	rcvbufKey = "doctor.rcvbuf_errors"
)

func checkBuffers(ctx context.Context, e *Env) Result {
	rmem, _ := strconv.Atoi(sysx.Sysctl("net.core.rmem_max"))
	wmem, _ := strconv.Atoi(sysx.Sysctl("net.core.wmem_max"))
	errs := udpCounter("RcvbufErrors")
	if rmem < minBuffer || wmem < minBuffer {
		return warn(fmt.Sprintf("rmem_max %d, wmem_max %d are low for high throughput", rmem, wmem),
			e.dropIn("buffers", "net.core.rmem_max = 7500000", "net.core.wmem_max = 7500000"))
	}
	if e.Store != nil {
		prev, _ := e.Store.Setting(rcvbufKey)
		e.Store.SetSetting(rcvbufKey, strconv.Itoa(errs))
		if n, err := strconv.Atoi(prev); err == nil && errs > n {
			return warn(fmt.Sprintf("UDP receive buffer errors grew by %d since the last check: the CPU cannot keep up or the buffers are too small", errs-n), nil)
		}
	}
	return ok(fmt.Sprintf("rmem_max %d, wmem_max %d, UDP receive buffer errors are not growing", rmem, wmem))
}

func udpCounter(name string) int {
	f, err := os.Open("/proc/net/snmp")
	if err != nil {
		return 0
	}
	defer f.Close()
	var header []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "Udp:" {
			continue
		}
		if header == nil {
			header = fields
			continue
		}
		if i := slices.Index(header, name); i > 0 && i < len(fields) {
			v, _ := strconv.Atoi(fields[i])
			return v
		}
	}
	return 0
}

func checkBBR(ctx context.Context, e *Env) Result {
	qdisc := sysx.Sysctl("net.core.default_qdisc")
	cc := sysx.Sysctl("net.ipv4.tcp_congestion_control")
	if qdisc == "fq" && cc == "bbr" {
		return ok("fq + bbr")
	}
	fix := e.dropIn("bbr", "net.core.default_qdisc = fq", "net.ipv4.tcp_congestion_control = bbr")
	apply := fix.Apply
	fix.Apply = func(ctx context.Context) error {
		if err := os.WriteFile("/etc/modules-load.d/90-awgx-bbr.conf", []byte("tcp_bbr\n"), 0o644); err != nil {
			return err
		}
		if _, err := e.Run.Run(ctx, "modprobe", "tcp_bbr"); err != nil {
			return err
		}
		return apply(ctx)
	}
	return warn(fmt.Sprintf("qdisc %s, congestion control %s, fq + bbr is recommended", qdisc, cc), fix)
}

func checkDNS(ctx context.Context, e *Env) Result {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(ctx, "ppa.launchpadcontent.net"); err != nil {
		return fail("DNS resolution from the server fails: "+err.Error(), nil)
	}
	return ok("DNS resolution works")
}

func checkState(ctx context.Context, e *Env) Result {
	if e.Store == nil {
		return ok("awgx is not installed")
	}
	if err := e.Store.Check(); err != nil {
		return fail(err.Error(), nil)
	}
	if e.Instance == nil {
		return ok("state is consistent")
	}
	inst := *e.Instance
	if !netx.LinkExists(inst.Name) {
		return warn("interface "+inst.Name+" is down", &Fix{
			Description: "systemctl restart " + netx.DataplaneUnit,
			Apply: func(ctx context.Context) error {
				_, err := e.Run.Run(ctx, "systemctl", "restart", netx.DataplaneUnit)
				return err
			},
		})
	}
	dev, err := awg.Tool{Run: netx.InNS(e.Run)}.Show(ctx, inst.Name)
	if err != nil {
		return fail(err.Error(), nil)
	}
	peers, err := e.Store.Peers(inst.Name)
	if err != nil {
		return fail(err.Error(), nil)
	}
	var want []awgconf.Key
	for _, p := range peers {
		if p.Active(time.Now()) {
			want = append(want, p.PublicKey)
		}
	}
	missing, extra := awg.PeerDiff(dev, want)
	if dev.PublicKey != inst.PrivateKey.Public() || missing+extra > 0 {
		var fix *Fix
		if e.Reconcile != nil {
			fix = &Fix{
				Description: "reconcile: awg syncconf " + inst.Name + " from state",
				Apply: func(ctx context.Context) error {
					return e.Reconcile(ctx, inst.Name)
				},
			}
		}
		return warn(fmt.Sprintf("state and kernel differ: %d peers missing, %d unknown", missing, extra), fix)
	}
	return ok(fmt.Sprintf("state matches kernel: %d peers", len(dev.Peers)))
}

func checkMode(ctx context.Context, e *Env) Result {
	if e.Instance == nil {
		return ok("no instance yet")
	}
	if !netx.NSExists() {
		return warn(e.Instance.Name+" is down", nil)
	}
	links, err := netx.Links(ctx, netx.InNS(e.Run))
	if err != nil {
		return fail(err.Error(), nil)
	}
	for _, l := range links {
		if l.Name != e.Instance.Name {
			continue
		}
		if l.Kind != "amneziawg" {
			return fail(fmt.Sprintf("%s is a %s interface, not the amneziawg kernel module", l.Name, l.Kind), nil)
		}
		if len(e.Failed) > 0 {
			return fail("the module did not build for installed kernels "+strings.Join(e.Failed, ", ")+": after a reboot into them the tunnel will not start", dkmsFix(e, e.Failed))
		}
		return ok("kernel mode")
	}
	return warn(e.Instance.Name+" is down", nil)
}

func dkmsFix(e *Env, kernels []string) *Fix {
	return &Fix{
		Description: "build the module with dkms autoinstall for " + strings.Join(kernels, ", "),
		Apply: func(ctx context.Context) error {
			for _, k := range kernels {
				if _, err := e.Run.Run(ctx, "dkms", "autoinstall", "-k", k); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func checkUplinkPort(ctx context.Context, e *Env) Result {
	if e.Store == nil {
		return ok("not installed yet")
	}
	ups, _ := e.Store.Uplinks()
	tool := awg.Tool{Run: netx.InNS(e.Run)}
	var set, stale []string
	ports := map[string]int{}
	for _, u := range ups {
		c, err := awgconf.Parse(strings.NewReader(u.Conf))
		if err != nil || c.Interface.ListenPort == 0 {
			continue
		}
		want := c.Interface.ListenPort
		set = append(set, fmt.Sprintf("%s %d", u.Name, want))
		if !netx.LinkExists(u.Name) {
			continue
		}
		if d, err := tool.Show(ctx, u.Name); err == nil && d.ListenPort != want {
			stale = append(stale, fmt.Sprintf("%s listens on %d", u.Name, d.ListenPort))
			ports[u.Name] = want
		}
	}
	if len(set) == 0 {
		return ok("uplink ports are random")
	}
	if len(stale) == 0 {
		return ok("uplink ports: " + strings.Join(set, ", "))
	}
	return warn("uplink port differs from the state: "+strings.Join(stale, ", "), &Fix{
		Description: "set the uplink ports from the state: " + strings.Join(set, ", "),
		Apply: func(ctx context.Context) error {
			var errs []error
			for name, port := range ports {
				_, err := tool.Run.Run(ctx, "awg", "set", name, "listen-port", strconv.Itoa(port))
				errs = append(errs, err)
			}
			return errors.Join(errs...)
		},
	})
}

func checkShaping(ctx context.Context, e *Env) Result {
	if e.Store == nil {
		return ok("not installed yet")
	}
	var names []string
	for _, i := range e.Instances {
		names = append(names, i.Name)
	}
	ups, _ := e.Store.Uplinks()
	for _, u := range ups {
		names = append(names, u.Name)
	}
	var set, stale []string
	for _, n := range names {
		v, _ := e.Store.Setting(netx.RateSetting + n)
		want, _ := strconv.Atoi(v)
		if want == 0 {
			continue
		}
		set = append(set, fmt.Sprintf("%s %d Mbit/s", n, want))
		if netx.LinkExists(n) && netx.ShapedRate(ctx, netx.InNS(e.Run), n) != want {
			stale = append(stale, n)
		}
	}
	if len(set) == 0 {
		return ok("no rate limits")
	}
	if len(stale) == 0 {
		return ok("rate limits: " + strings.Join(set, ", "))
	}
	var fix *Fix
	if e.ApplyRate != nil {
		fix = &Fix{
			Description: "apply the rate limit on " + strings.Join(stale, ", "),
			Apply: func(ctx context.Context) error {
				var errs []error
				for _, n := range stale {
					errs = append(errs, e.ApplyRate(ctx, n))
				}
				return errors.Join(errs...)
			},
		}
	}
	return warn("rate limit not applied on "+strings.Join(stale, ", "), fix)
}
