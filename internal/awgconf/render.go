package awgconf

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/k2so-dev/awgx/internal/fmtx"
)

func (c Config) String() string {
	var b strings.Builder
	i := c.Interface
	b.WriteString("[Interface]\n")
	kv(&b, "PrivateKey", i.PrivateKey.String())
	if i.ListenPort != 0 {
		kv(&b, "ListenPort", i.ListenPort)
	}
	if len(i.Address) > 0 {
		kv(&b, "Address", joinPrefixes(i.Address))
	}
	if len(i.DNS) > 0 {
		kv(&b, "DNS", joinAddrs(i.DNS))
	}
	if i.MTU != 0 {
		kv(&b, "MTU", i.MTU)
	}
	if i.Table != "" {
		kv(&b, "Table", i.Table)
	}
	writeObfuscation(&b, i.Obfuscation)
	for _, cmd := range i.PostUp {
		kv(&b, "PostUp", cmd)
	}
	for _, cmd := range i.PostDown {
		kv(&b, "PostDown", cmd)
	}
	for _, p := range c.Peers {
		b.WriteString("\n[Peer]\n")
		kv(&b, "PublicKey", p.PublicKey.String())
		if !p.PresharedKey.IsZero() {
			kv(&b, "PresharedKey", p.PresharedKey.String())
		}
		if p.Endpoint != "" {
			kv(&b, "Endpoint", p.Endpoint)
		}
		kv(&b, "AllowedIPs", joinPrefixes(p.AllowedIPs))
		if !p.PersistentKeepalive.IsZero() {
			kv(&b, "PersistentKeepalive", p.PersistentKeepalive)
		}
	}
	return b.String()
}

func writeObfuscation(b *strings.Builder, o Obfuscation) {
	if o.Jc > 0 {
		kv(b, "Jc", o.Jc)
		kv(b, "Jmin", o.Jmin)
		kv(b, "Jmax", o.Jmax)
	}
	kv(b, "S1", o.S1)
	kv(b, "S2", o.S2)
	kv(b, "S3", o.S3)
	kv(b, "S4", o.S4)
	kv(b, "H1", o.H1)
	kv(b, "H2", o.H2)
	kv(b, "H3", o.H3)
	kv(b, "H4", o.H4)
	for n, v := range o.I {
		if v != "" {
			kv(b, fmt.Sprintf("I%d", n+1), v)
		}
	}
	if !o.HeaderProtectionKey.IsZero() {
		kv(b, "HeaderProtectionKey", o.HeaderProtectionKey.String())
	}
	kv(b, "RandomTrailers", fmtx.OnOff(o.RandomTrailers))
	kv(b, "DisableCookies", fmtx.OnOff(o.DisableCookies))
	ranges := []struct {
		name string
		r    Range
	}{
		{"ContentPaddingAddition", o.ContentPaddingAddition},
		{"RekeyAfterTime", o.RekeyAfterTime},
		{"RekeyTimeout", o.RekeyTimeout},
		{"RejectAfterTime", o.RejectAfterTime},
		{"KeepaliveTimeout", o.KeepaliveTimeout},
		{"MaxHandshakeAttempts", o.MaxHandshakeAttempts},
	}
	for _, r := range ranges {
		if !r.r.IsZero() {
			kv(b, r.name, r.r)
		}
	}
}

func kv(b *strings.Builder, k string, v any) {
	fmt.Fprintf(b, "%s = %v\n", k, v)
}

func joinPrefixes(ps []netip.Prefix) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}

func joinAddrs(as []netip.Addr) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

func (c Config) Strip() Config {
	c.Interface.Address = nil
	c.Interface.DNS = nil
	c.Interface.MTU = 0
	c.Interface.Table = ""
	c.Interface.PostUp = nil
	c.Interface.PostDown = nil
	return c
}
