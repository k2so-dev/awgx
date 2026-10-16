package relay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

const handshakeTimeout = 8 * time.Second

type Socks struct {
	Address  string `json:"address"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	NoUDP    bool   `json:"no_udp,omitempty"`
}

func (s Socks) handshake(c net.Conn) error {
	methods := []byte{5, 1, 0}
	if s.User != "" {
		methods = []byte{5, 1, 2}
	}
	if _, err := c.Write(methods); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(c, resp); err != nil {
		return err
	}
	if resp[0] != 5 || resp[1] == 0xff {
		return errors.New("socks5: no acceptable auth method")
	}
	if resp[1] == 2 {
		req := []byte{1, byte(len(s.User))}
		req = append(req, s.User...)
		req = append(req, byte(len(s.Password)))
		req = append(req, s.Password...)
		if _, err := c.Write(req); err != nil {
			return err
		}
		if _, err := io.ReadFull(c, resp); err != nil {
			return err
		}
		if resp[1] != 0 {
			return errors.New("socks5: authentication failed")
		}
	}
	return nil
}

func (s Socks) request(c net.Conn, cmd byte, target netip.AddrPort) (netip.AddrPort, error) {
	req := AppendAddr([]byte{5, cmd, 0}, target)
	if _, err := c.Write(req); err != nil {
		return netip.AddrPort{}, err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return netip.AddrPort{}, err
	}
	if head[1] != 0 {
		return netip.AddrPort{}, fmt.Errorf("socks5: request rejected with code %d", head[1])
	}
	var addr []byte
	switch head[3] {
	case 1:
		addr = make([]byte, 4)
	case 4:
		addr = make([]byte, 16)
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return netip.AddrPort{}, err
		}
		addr = make([]byte, l[0])
	default:
		return netip.AddrPort{}, errors.New("socks5: bad address type")
	}
	if _, err := io.ReadFull(c, addr); err != nil {
		return netip.AddrPort{}, err
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(c, port); err != nil {
		return netip.AddrPort{}, err
	}
	ip, _ := netip.AddrFromSlice(addr)
	return netip.AddrPortFrom(ip.Unmap(), binary.BigEndian.Uint16(port)), nil
}

func AppendAddr(b []byte, ap netip.AddrPort) []byte {
	if ap.Addr().Unmap().Is4() {
		a := ap.Addr().Unmap().As4()
		b = append(append(b, 1), a[:]...)
	} else {
		a := ap.Addr().As16()
		b = append(append(b, 4), a[:]...)
	}
	return binary.BigEndian.AppendUint16(b, ap.Port())
}

func ParseUDPHeader(b []byte) (netip.AddrPort, int, error) {
	if len(b) < 4 || b[2] != 0 {
		return netip.AddrPort{}, 0, errors.New("socks5 udp: bad header")
	}
	var n int
	switch b[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	default:
		return netip.AddrPort{}, 0, errors.New("socks5 udp: unsupported address type")
	}
	if len(b) < 4+n+2 {
		return netip.AddrPort{}, 0, errors.New("socks5 udp: short packet")
	}
	ip, _ := netip.AddrFromSlice(b[4 : 4+n])
	port := binary.BigEndian.Uint16(b[4+n:])
	return netip.AddrPortFrom(ip.Unmap(), port), 4 + n + 2, nil
}

func (s Socks) open(ctx context.Context) (*net.TCPConn, error) {
	c, err := (&net.Dialer{Timeout: handshakeTimeout}).DialContext(ctx, "tcp", s.Address)
	if err != nil {
		return nil, fmt.Errorf("socks5 %s: %w", s.Address, err)
	}
	c.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := s.handshake(c); err != nil {
		c.Close()
		return nil, err
	}
	return c.(*net.TCPConn), nil
}

func (s Socks) Connect(ctx context.Context, target netip.AddrPort) (*net.TCPConn, error) {
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.request(c, 1, target); err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

type Association struct {
	Control *net.TCPConn
	UDP     *net.UDPConn
}

func (a Association) Close() {
	a.Control.Close()
	a.UDP.Close()
}

func (s Socks) Associate(ctx context.Context) (Association, error) {
	c, err := s.open(ctx)
	if err != nil {
		return Association{}, err
	}
	relay, err := s.request(c, 3, netip.AddrPortFrom(netip.IPv4Unspecified(), 0))
	if err != nil {
		c.Close()
		return Association{}, fmt.Errorf("socks5 udp associate: %w", err)
	}
	if !relay.Addr().IsValid() || relay.Addr().IsUnspecified() {
		host, _, _ := net.SplitHostPort(s.Address)
		ip, err := netip.ParseAddr(host)
		if err != nil {
			c.Close()
			return Association{}, fmt.Errorf("socks5 udp associate: unusable relay %s", relay)
		}
		relay = netip.AddrPortFrom(ip, relay.Port())
	}
	u, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(relay))
	if err != nil {
		c.Close()
		return Association{}, err
	}
	c.SetDeadline(time.Time{})
	return Association{Control: c, UDP: u}, nil
}
