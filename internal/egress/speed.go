package egress

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/perf"
	"github.com/k2so-dev/awgx/internal/state"
)

const settingAgent = "uplink_agent:"

type SpeedOpts struct {
	Agent   string
	Seconds int
	Streams int
}

func (e *Engine) UplinkAgent(name string) (string, error) {
	if v, _ := e.Store.Setting(settingAgent + name); v != "" {
		return v, nil
	}
	u, err := e.Store.Uplink(name)
	if err != nil {
		return "", err
	}
	c, err := awgconf.Parse(strings.NewReader(u.Conf))
	if err != nil {
		return "", err
	}
	for _, a := range c.Interface.Address {
		if a.Addr().Is4() {
			b := a.Addr().As4()
			b[3] = 1
			return perf.AgentAddr(net.IP(b[:]).String()), nil
		}
	}
	return "", fmt.Errorf("uplink %s has no IPv4 address, pass --agent", name)
}

func agentAddr(s string) (string, error) {
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s, nil
	}
	if net.ParseIP(s) == nil {
		return "", fmt.Errorf("agent must be IP or IP:PORT, got %q", s)
	}
	return perf.AgentAddr(s), nil
}

func (e *Engine) SpeedUplink(ctx context.Context, name string, o SpeedOpts) error {
	u, err := e.Store.Uplink(name)
	if err != nil {
		return err
	}
	if !e.App.InterfaceUp(name) {
		return fmt.Errorf("interface %s is down, check: systemctl status %s", name, core.DataplaneUnit)
	}
	agent := o.Agent
	if agent != "" {
		if agent, err = agentAddr(agent); err != nil {
			return err
		}
		e.Store.SetSetting(settingAgent+name, agent)
	} else if agent, err = e.UplinkAgent(name); err != nil {
		return err
	}
	secs := min(max(o.Seconds, 3), 60)
	streams := min(max(o.Streams, 1), 16)
	d := time.Duration(secs) * time.Second
	log := e.App.Logf
	agentHost, _, _ := net.SplitHostPort(agent)

	log("uplink         %s, %d s per direction, %d stream(s)", name, secs, streams)
	log("agent          %s", agent)
	if c, err := awgconf.Parse(strings.NewReader(u.Conf)); err == nil {
		host, _, err := net.SplitHostPort(c.Peers[0].Endpoint)
		if err != nil {
			host = c.Peers[0].Endpoint
		}
		p, err := perf.PingStats(ctx, e.App.Run, host, 0, 4*time.Second)
		log("underlay ping  %s  %s", host, pingText(p, err))
	}
	return e.withProbe(ctx, state.Hop{Dev: name}, func() error {
		ns := e.App.NS()
		p, err := perf.PingStats(ctx, ns, agentHost, netx.ProbeMark, 4*time.Second)
		log("tunnel ping    %s  %s", agentHost, pingText(p, err))
		dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
			var c net.Conn
			err := netx.Do(func() error {
				var err error
				c, err = markDialer(netx.ProbeMark).DialContext(ctx, network, addr)
				return err
			})
			return c, err
		}
		nsTCP := func() map[string]uint64 {
			var m map[string]uint64
			netx.Do(func() error {
				m = perf.ReadSNMP("/proc/thread-self/net/snmp", "Tcp")
				return nil
			})
			return m
		}
		for _, send := range []bool{true, false} {
			label := "download"
			if send {
				label = "upload"
			}
			pings := make(chan string, 1)
			go func() {
				p, err := perf.PingStats(ctx, ns, agentHost, netx.ProbeMark, d)
				pings <- pingText(p, err)
			}()
			mark := perf.MarkLoadFrom(nsTCP)
			t, err := perf.Transfer(ctx, dial, agent, send, d, streams, label)
			load := mark.Since()
			loaded := <-pings
			if err != nil {
				return fmt.Errorf("%s: %w\nis the agent running? On the remote server: awgx perf agent --listen %s", label, err, agentHost)
			}
			log("%-14s %.1f Mbit/s", label, t.Mbps)
			log("  loaded ping  %s", loaded)
			log("  tcp retrans  %d of %d segments (%.2f%%)", load.Retrans, load.Sent, pct(load.Retrans, load.Sent))
			log("  cpu          %.0f%% busy, %.0f%% softirq", load.Busy, load.SoftIRQ)
		}
		if r := netx.ShapedRate(ctx, ns, name); r > 0 {
			log("shaping        cake %d Mbit/s on upload; download is limited by the remote server", r)
		} else {
			log("shaping        none")
		}
		return nil
	})
}

func pingText(p perf.Ping, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return p.String()
}

func pct(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}
