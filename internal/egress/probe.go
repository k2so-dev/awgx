package egress

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"

	"github.com/k2so-dev/awgx/internal/relay"
	"golang.org/x/sys/unix"
)

var (
	ProbeTCP  = []string{"1.1.1.1:443", "8.8.8.8:443"}
	ProbeName = "cloudflare.com"
)

const probeTimeout = 4 * time.Second

func markDialer(mark uint32) *net.Dialer {
	d := &net.Dialer{Timeout: probeTimeout}
	if mark != 0 {
		d.Control = func(network, address string, c syscall.RawConn) error {
			var serr error
			err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark))
			})
			if err != nil {
				return err
			}
			return serr
		}
	}
	return d
}

func ProbeTCPConnect(ctx context.Context, mark uint32, targets []string) error {
	var errs []error
	for _, t := range targets {
		c, err := markDialer(mark).DialContext(ctx, "tcp4", t)
		if err == nil {
			c.(*net.TCPConn).SetLinger(0)
			c.Close()
			return nil
		}
		errs = append(errs, err)
	}
	return fmt.Errorf("tcp: %w", errors.Join(errs...))
}

func ProbeDNS(ctx context.Context, mark uint32, server netip.Addr) error {
	c, err := markDialer(mark).DialContext(ctx, "udp", net.JoinHostPort(server.String(), "53"))
	if err != nil {
		return fmt.Errorf("dns: %w", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(probeTimeout))
	q, id := dnsQuery(ProbeName)
	if _, err := c.Write(q); err != nil {
		return fmt.Errorf("dns: %w", err)
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return fmt.Errorf("dns via %s: %w", server, err)
	}
	return dnsCheck(buf[:n], id)
}

func dnsQuery(name string) ([]byte, uint16) {
	var idb [2]byte
	rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	q := []byte{idb[0], idb[1], 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			q = append(q, byte(i-start))
			q = append(q, name[start:i]...)
			start = i + 1
		}
	}
	return append(q, 0, 0, 1, 0, 1), id
}

func dnsCheck(b []byte, id uint16) error {
	if len(b) < 12 || binary.BigEndian.Uint16(b) != id {
		return errors.New("dns: malformed reply")
	}
	if rcode := b[3] & 0x0f; rcode != 0 {
		return fmt.Errorf("dns: rcode %d", rcode)
	}
	if binary.BigEndian.Uint16(b[6:]) == 0 {
		return errors.New("dns: empty answer")
	}
	return nil
}

func ProbeSocks(ctx context.Context, s relay.Socks, dns netip.Addr) error {
	var errs []error
	connected := false
	for _, t := range ProbeTCP {
		c, err := s.Connect(ctx, netip.MustParseAddrPort(t))
		if err == nil {
			c.SetLinger(0)
			c.Close()
			connected = true
			break
		}
		errs = append(errs, err)
	}
	if !connected {
		return fmt.Errorf("socks5 connect: %w", errors.Join(errs...))
	}
	if s.NoUDP {
		return nil
	}
	a, err := s.Associate(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	a.UDP.SetDeadline(time.Now().Add(probeTimeout))
	q, id := dnsQuery(ProbeName)
	pkt := relay.AppendAddr([]byte{0, 0, 0}, netip.AddrPortFrom(dns, 53))
	if _, err := a.UDP.Write(append(pkt, q...)); err != nil {
		return err
	}
	buf := make([]byte, 1500)
	n, err := a.UDP.Read(buf)
	if err != nil {
		return fmt.Errorf("socks5 udp: no reply from %s: %w", dns, err)
	}
	_, off, err := relay.ParseUDPHeader(buf[:n])
	if err != nil {
		return err
	}
	return dnsCheck(buf[off:n], id)
}

func ParseSocksAddress(s string) (string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("invalid port in %q", s)
	}
	a, err := netip.ParseAddr(host)
	if err != nil || a.Zone() != "" {
		return "", fmt.Errorf("socks5 address must be an IP without a zone: %q", s)
	}
	return net.JoinHostPort(host, port), nil
}
