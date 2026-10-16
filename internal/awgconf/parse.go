package awgconf

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"net/netip"
	"strconv"
	"strings"
)

func Parse(r io.Reader) (Config, error) {
	var c Config
	var peer *Peer
	section := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text, _, _ := strings.Cut(sc.Text(), "#")
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		switch strings.ToLower(text) {
		case "[interface]":
			section = "interface"
			continue
		case "[peer]":
			section = "peer"
			c.Peers = append(c.Peers, Peer{})
			peer = &c.Peers[len(c.Peers)-1]
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return c, fmt.Errorf("line %d: expected key = value", line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		var err error
		switch section {
		case "interface":
			err = setInterface(&c.Interface, key, value)
		case "peer":
			err = setPeer(peer, key, value)
		default:
			err = fmt.Errorf("key outside of a section")
		}
		if err != nil {
			return c, fmt.Errorf("line %d: %w", line, err)
		}
	}
	return c, sc.Err()
}

func setInterface(i *Interface, key, value string) error {
	o := &i.Obfuscation
	var err error
	switch key {
	case "privatekey":
		i.PrivateKey, err = ParseKey(value)
	case "listenport":
		i.ListenPort, err = parseInt(value, 65535)
	case "address":
		i.Address, err = parsePrefixes(value, i.Address)
	case "dns":
		i.DNS, err = parseAddrs(value, i.DNS)
	case "mtu":
		i.MTU, err = parseInt(value, 65535)
	case "table":
		i.Table = value
	case "postup":
		i.PostUp = append(i.PostUp, value)
	case "postdown":
		i.PostDown = append(i.PostDown, value)
	case "jc":
		o.Jc, err = parseU16(value)
	case "jmin":
		o.Jmin, err = parseU16(value)
	case "jmax":
		o.Jmax, err = parseU16(value)
	case "s1":
		o.S1, err = parseU16(value)
	case "s2":
		o.S2, err = parseU16(value)
	case "s3":
		o.S3, err = parseU16(value)
	case "s4":
		o.S4, err = parseU16(value)
	case "h1":
		o.H1, err = ParseRange(value, math.MaxUint32)
	case "h2":
		o.H2, err = ParseRange(value, math.MaxUint32)
	case "h3":
		o.H3, err = ParseRange(value, math.MaxUint32)
	case "h4":
		o.H4, err = ParseRange(value, math.MaxUint32)
	case "i1", "i2", "i3", "i4", "i5":
		o.I[key[1]-'1'] = value
	case "headerprotectionkey":
		o.HeaderProtectionKey, err = ParseKey(value)
	case "randomtrailers":
		o.RandomTrailers, err = parseBool(value)
	case "disablecookies":
		o.DisableCookies, err = parseBool(value)
	case "contentpaddingaddition":
		o.ContentPaddingAddition, err = ParseRange(value, math.MaxUint16)
	case "rekeyaftertime":
		o.RekeyAfterTime, err = ParseRange(value, math.MaxUint16)
	case "rekeytimeout":
		o.RekeyTimeout, err = ParseRange(value, math.MaxUint16)
	case "rejectaftertime":
		o.RejectAfterTime, err = ParseRange(value, math.MaxUint16)
	case "keepalivetimeout":
		o.KeepaliveTimeout, err = ParseRange(value, math.MaxUint16)
	case "maxhandshakeattempts":
		o.MaxHandshakeAttempts, err = ParseRange(value, math.MaxUint16)
	case "fwmark", "saveconfig", "preup", "predown":
	default:
		return fmt.Errorf("unknown interface key %q", key)
	}
	return err
}

func setPeer(p *Peer, key, value string) error {
	var err error
	switch key {
	case "publickey":
		p.PublicKey, err = ParseKey(value)
	case "presharedkey":
		p.PresharedKey, err = ParseKey(value)
	case "endpoint":
		p.Endpoint = value
	case "allowedips":
		p.AllowedIPs, err = parsePrefixes(value, p.AllowedIPs)
	case "persistentkeepalive":
		if value == "off" {
			return nil
		}
		p.PersistentKeepalive, err = ParseRange(value, math.MaxUint16)
	case "advancedsecurity":
	default:
		return fmt.Errorf("unknown peer key %q", key)
	}
	return err
}

func parseInt(s string, max int) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 || v > max {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	return v, nil
}

func parseU16(s string) (uint16, error) {
	v, err := parseInt(s, math.MaxUint16)
	return uint16(v), err
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "1", "true":
		return true, nil
	case "off", "0", "false":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q", s)
}

func parsePrefixes(s string, acc []netip.Prefix) ([]netip.Prefix, error) {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			a, err := netip.ParseAddr(part)
			if err != nil {
				return nil, err
			}
			acc = append(acc, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, err
		}
		acc = append(acc, p)
	}
	return acc, nil
}

func parseAddrs(s string, acc []netip.Addr) ([]netip.Addr, error) {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil && searchDomain(part) {
			continue
		}
		if err != nil {
			return nil, err
		}
		acc = append(acc, a)
	}
	return acc, nil
}

func searchDomain(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) &&
		!strings.ContainsAny(s, ":/ ")
}
