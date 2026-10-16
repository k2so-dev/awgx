package render

import (
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/state"
)

const Binary = "/usr/local/bin/awgx"

func Server(inst state.Instance, peers []state.Peer, now time.Time) awgconf.Config {
	obf := inst.Obfuscation
	obf.I = [5]string{}
	c := awgconf.Config{
		Interface: awgconf.Interface{
			PrivateKey:  inst.PrivateKey,
			ListenPort:  inst.ListenPort,
			Address:     serverAddresses(inst),
			MTU:         inst.MTU,
			Obfuscation: obf,
		},
	}
	for _, p := range peers {
		if p.Active(now) {
			c.Peers = append(c.Peers, ServerPeer(p))
		}
	}
	return c
}

func ServerPeer(p state.Peer) awgconf.Peer {
	return awgconf.Peer{
		PublicKey:    p.PublicKey,
		PresharedKey: p.PresharedKey,
		AllowedIPs:   PeerAddresses(p),
	}
}

func serverAddresses(inst state.Instance) []netip.Prefix {
	out := []netip.Prefix{netip.PrefixFrom(inst.Address4(), inst.Subnet4.Bits())}
	if inst.IPv6() {
		out = append(out, netip.PrefixFrom(inst.Address6(), inst.Subnet6.Bits()))
	}
	return out
}

func PeerAddresses(p state.Peer) []netip.Prefix {
	out := []netip.Prefix{netip.PrefixFrom(p.Address4, 32)}
	if p.Address6.IsValid() {
		out = append(out, netip.PrefixFrom(p.Address6, 128))
	}
	return out
}

func Client(inst state.Instance, p state.Peer) awgconf.Config {
	obf := inst.Obfuscation
	if p.I1 != nil {
		obf.I[0] = p.I1CPS
	}
	return awgconf.Config{
		Interface: awgconf.Interface{
			PrivateKey:  p.PrivateKey,
			Address:     PeerAddresses(p),
			DNS:         inst.DNS,
			MTU:         inst.MTU,
			Obfuscation: obf,
		},
		Peers: []awgconf.Peer{{
			PublicKey:           inst.PrivateKey.Public(),
			PresharedKey:        p.PresharedKey,
			Endpoint:            net.JoinHostPort(inst.Endpoint, strconv.Itoa(inst.ListenPort)),
			AllowedIPs:          p.AllowedIPs,
			PersistentKeepalive: p.Keepalive,
		}},
	}
}
