package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/gen"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/k2so-dev/awgx/internal/validate"
)

var uplinkNameRe = regexp.MustCompile(`^awg[0-9]{1,4}$`)

type UplinkInfo struct {
	Name          string    `json:"name"`
	Endpoint      string    `json:"endpoint"`
	Address       string    `json:"address"`
	MTU           int       `json:"mtu"`
	Port          int       `json:"port"`
	ListenPort    int       `json:"listen_port"`
	Rate          int       `json:"rate"`
	Routed        bool      `json:"routed"`
	Up            bool      `json:"up"`
	LastHandshake time.Time `json:"last_handshake,omitzero"`
	RxBytes       uint64    `json:"rx_bytes"`
	TxBytes       uint64    `json:"tx_bytes"`
	UsedBy        []string  `json:"used_by,omitempty"`
}

func PrepareUplink(conf string, instances []state.Instance) (awgconf.Config, []string, error) {
	c, err := awgconf.Parse(strings.NewReader(conf))
	if err != nil {
		return c, nil, err
	}
	var warnings []string
	if c.Interface.PrivateKey.IsZero() {
		return c, nil, fmt.Errorf("uplink config has no PrivateKey")
	}
	if len(c.Peers) != 1 || c.Peers[0].Endpoint == "" {
		return c, nil, fmt.Errorf("uplink config needs exactly one peer with an Endpoint")
	}
	if len(c.Interface.Address) == 0 {
		return c, nil, fmt.Errorf("uplink config has no Address")
	}
	is := validate.Obfuscation(c.Interface.Obfuscation, gen.PathMTU)
	if is.Failed() {
		return c, nil, is.Err()
	}
	for _, i := range is {
		warnings = append(warnings, i.String())
	}
	for _, a := range c.Interface.Address {
		for _, inst := range instances {
			if inst.Subnet4.Overlaps(a) || inst.Subnet6.Overlaps(a) {
				return c, nil, fmt.Errorf("uplink address %s overlaps the peer subnet of %s", a, inst.Name)
			}
		}
	}
	p := &c.Peers[0]
	if !slices.Contains(p.AllowedIPs, netip.MustParsePrefix("0.0.0.0/0")) {
		warnings = append(warnings, "uplink AllowedIPs has no 0.0.0.0/0: only listed destinations will pass")
	}
	if p.PersistentKeepalive.IsZero() {
		p.PersistentKeepalive = awgconf.Single(25)
	}
	c.Interface.Table = "off"
	c.Interface.DNS = nil
	c.Interface.PostUp = nil
	c.Interface.PostDown = nil
	return c, warnings, nil
}

func FitUplinkMTU(c *awgconf.Config, link int) string {
	host, _, err := net.SplitHostPort(c.Peers[0].Endpoint)
	if err != nil {
		host = c.Peers[0].Endpoint
	}
	fit := core.InnerMTU(link, c.Interface.Obfuscation.S4, host)
	if c.Interface.MTU != 0 && c.Interface.MTU <= fit {
		return ""
	}
	warning := ""
	if c.Interface.MTU != 0 {
		warning = fmt.Sprintf("uplink MTU lowered from %d to %d to fit this server's link MTU %d; clients routed through it need MTU %d or lower", c.Interface.MTU, fit, link, fit)
	}
	c.Interface.MTU = fit
	return warning
}

func (e *Engine) checkUplinkPort(name string, port int, insts []state.Instance) error {
	if port == 0 {
		return nil
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("listen port must be 1-65535, or 0 for a random one")
	}
	for _, inst := range insts {
		if inst.ListenPort == port {
			return fmt.Errorf("listen port %d is used by instance %s", port, inst.Name)
		}
	}
	ups, err := e.Store.Uplinks()
	if err != nil {
		return err
	}
	for _, u := range ups {
		if u.Name != name && uplinkPort(u.Conf) == port {
			return fmt.Errorf("listen port %d is used by uplink %s", port, u.Name)
		}
	}
	return nil
}

func uplinkPort(conf string) int {
	c, err := awgconf.Parse(strings.NewReader(conf))
	if err != nil {
		return 0
	}
	return c.Interface.ListenPort
}

func (e *Engine) uplinkConf(name string) string {
	return e.App.Paths.Conf(name)
}

func (e *Engine) AddUplink(ctx context.Context, name, conf string, port int, routed bool) ([]string, error) {
	if !uplinkNameRe.MatchString(name) {
		return nil, fmt.Errorf("uplink name must look like awg1")
	}
	insts, err := e.Store.Instances()
	if err != nil {
		return nil, err
	}
	for _, i := range insts {
		if i.Name == name {
			return nil, fmt.Errorf("%s is a server instance", name)
		}
	}
	if _, err := e.Store.Uplink(name); err == nil {
		return nil, fmt.Errorf("uplink %s already exists", name)
	}
	if e.App.InterfaceUp(name) || netx.RootLinkExists(name) {
		return nil, fmt.Errorf("interface %s already exists", name)
	}
	if sysx.Exists(e.uplinkConf(name)) {
		return nil, fmt.Errorf("%s already exists, pick another name or remove it first", e.uplinkConf(name))
	}
	c, warnings, err := PrepareUplink(conf, insts)
	if err != nil {
		return nil, err
	}
	if port != 0 {
		c.Interface.ListenPort = port
	}
	if err := e.checkUplinkPort(name, c.Interface.ListenPort, insts); err != nil {
		return nil, err
	}
	if w := FitUplinkMTU(&c, e.App.LinkMTU(ctx)); w != "" {
		warnings = append(warnings, w)
	}
	path := e.uplinkConf(name)
	if err := os.WriteFile(path, []byte(c.String()), 0o600); err != nil {
		return warnings, err
	}
	e.Store.AddArtifact(path, "file")
	u := state.Uplink{Name: name, Conf: c.String(), Routed: routed}
	if err := e.Store.SaveUplink(&u); err != nil {
		return warnings, err
	}
	if err := e.App.UpUplink(ctx, u); err != nil {
		e.App.DownUplink(ctx, name)
		os.Remove(path)
		e.Store.DeleteUplink(name)
		return warnings, err
	}
	e.Store.Log("uplink-add", name+" -> "+c.Peers[0].Endpoint)
	return warnings, nil
}

type UplinkEdit struct {
	Conf   string
	MTU    int
	Port   *int
	Routed *bool
}

func (e *Engine) UpdateUplink(ctx context.Context, name string, ed UplinkEdit) ([]string, error) {
	u, err := e.Store.Uplink(name)
	if err != nil {
		return nil, err
	}
	if ed.MTU != 0 && (ed.MTU < core.MinTunnelMTU || ed.MTU > core.MaxLinkMTU) {
		return nil, fmt.Errorf("MTU must be %d-%d", core.MinTunnelMTU, core.MaxLinkMTU)
	}
	insts, err := e.Store.Instances()
	if err != nil {
		return nil, err
	}
	conf := u.Conf
	if ed.Conf != "" {
		conf = ed.Conf
	}
	c, warnings, err := PrepareUplink(conf, insts)
	if err != nil {
		return nil, err
	}
	if c.Interface.ListenPort == 0 {
		c.Interface.ListenPort = uplinkPort(u.Conf)
	}
	if ed.Port != nil {
		c.Interface.ListenPort = *ed.Port
	}
	if err := e.checkUplinkPort(name, c.Interface.ListenPort, insts); err != nil {
		return nil, err
	}
	switch {
	case ed.MTU != 0:
		c.Interface.MTU = ed.MTU
	case ed.Conf != "":
		if w := FitUplinkMTU(&c, e.App.LinkMTU(ctx)); w != "" {
			warnings = append(warnings, w)
		}
	}
	if ed.Routed != nil {
		u.Routed = *ed.Routed
	}
	restart := ed.Conf != "" || ed.MTU != 0 || ed.Routed != nil
	u.Conf = c.String()
	if !restart {
		if err := e.App.UpUplink(ctx, u); err != nil {
			return warnings, err
		}
	}
	if err := os.WriteFile(e.uplinkConf(name), []byte(u.Conf), 0o600); err != nil {
		return warnings, err
	}
	if err := e.Store.SaveUplink(&u); err != nil {
		return warnings, err
	}
	if restart {
		if err := e.App.RestartUplink(ctx, u); err != nil {
			return warnings, err
		}
	}
	e.App.ApplyRate(ctx, name)
	e.Store.Log("uplink-set", fmt.Sprintf("%s -> %s, MTU %d, port %s, routed %v", name, c.Peers[0].Endpoint, c.Interface.MTU, fmtx.Port(c.Interface.ListenPort, 0), u.Routed))
	e.Up(ctx)
	return warnings, nil
}

func (e *Engine) uplinkUsers(name string) ([]string, error) {
	var users []string
	draft, err := e.Store.Draft()
	if err != nil {
		return nil, err
	}
	applied, err := e.Store.Applied()
	if err != nil {
		return nil, err
	}
	for _, cfg := range []state.EgressConfig{draft, applied.Config} {
		for _, p := range cfg.Profiles {
			if slices.ContainsFunc(p.Hops, func(h state.Hop) bool { return h.Dev == name }) && !slices.Contains(users, p.Name) {
				users = append(users, p.Name)
			}
		}
	}
	return users, nil
}

func (e *Engine) RemoveUplink(ctx context.Context, name string) error {
	if _, err := e.Store.Uplink(name); err != nil {
		return err
	}
	users, err := e.uplinkUsers(name)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("uplink %s is used by egress profiles %v (draft or applied); remove it there and apply first", name, users)
	}
	e.App.DownUplink(ctx, name)
	os.Remove(e.uplinkConf(name))
	e.App.ClearRate(name)
	e.Store.SetSetting(settingAgent+name, "")
	e.Store.Log("uplink-rm", name)
	return e.Store.DeleteUplink(name)
}

func (e *Engine) Uplinks(ctx context.Context) ([]UplinkInfo, error) {
	ups, err := e.Store.Uplinks()
	if err != nil {
		return nil, err
	}
	var out []UplinkInfo
	for _, u := range ups {
		info := UplinkInfo{Name: u.Name, Routed: u.Routed, Rate: e.App.Rate(u.Name), Up: e.App.InterfaceUp(u.Name)}
		if c, err := awgconf.Parse(strings.NewReader(u.Conf)); err == nil {
			info.Endpoint = c.Peers[0].Endpoint
			info.MTU = c.Interface.MTU
			info.Port = c.Interface.ListenPort
			for _, a := range c.Interface.Address {
				info.Address = strings.TrimSpace(info.Address + " " + a.String())
			}
		}
		if info.Up {
			d, err := e.App.Tool().Show(ctx, u.Name)
			if err == nil {
				info.ListenPort = d.ListenPort
			}
			if err == nil && len(d.Peers) > 0 {
				info.LastHandshake = d.Peers[0].LastHandshake
				info.RxBytes, info.TxBytes = d.Peers[0].RxBytes, d.Peers[0].TxBytes
			}
		}
		info.UsedBy, _ = e.uplinkUsers(u.Name)
		out = append(out, info)
	}
	return out, nil
}

func (e *Engine) TestUplink(ctx context.Context, name string) ([]string, error) {
	if _, err := e.Store.Uplink(name); err != nil {
		return nil, err
	}
	if !e.App.InterfaceUp(name) {
		return nil, fmt.Errorf("interface %s is down, check: systemctl status %s", name, core.DataplaneUnit)
	}
	var lines []string
	if err := e.ProbeHop(ctx, state.Hop{Dev: name}); err != nil {
		return lines, err
	}
	lines = append(lines, "ok   TCP and DNS through "+name)
	d, err := e.App.Tool().Show(ctx, name)
	if err != nil {
		return lines, err
	}
	if len(d.Peers) == 0 || d.Peers[0].LastHandshake.IsZero() {
		return lines, fmt.Errorf("no handshake with the upstream server")
	}
	age := time.Since(d.Peers[0].LastHandshake).Round(time.Second)
	if age > 180*time.Second {
		return lines, fmt.Errorf("last handshake %s ago", age)
	}
	return append(lines, fmt.Sprintf("ok   last handshake %s ago", age)), nil
}
