package relay

import (
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	echoTimeout = 10 * time.Second
	maxEchoes   = 4096
)

type echoKey struct {
	dst     netip.Addr
	id, seq uint16
}

type echoReq struct {
	src netip.Addr
	id  uint16
	at  time.Time
}

type echo struct {
	tun     *os.File
	v4, v6  *net.IPConn
	blocked func(netip.Addr) bool
	mu      sync.Mutex
	next    uint16
	pending map[echoKey]echoReq
}

func (s *Server) StartEcho(dev string) error {
	var fd int
	err := s.ns.do(func() error {
		var err error
		fd, err = unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		ifr, err := unix.NewIfreq(dev)
		if err != nil {
			unix.Close(fd)
			return err
		}
		ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
		if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
			unix.Close(fd)
			return err
		}
		return unix.SetNonblock(fd, true)
	})
	if err != nil {
		return err
	}
	e := &echo{tun: os.NewFile(uintptr(fd), dev), blocked: s.blocked, pending: map[echoKey]echoReq{}}
	if e.v4, err = net.ListenIP("ip4:icmp", nil); err != nil {
		e.tun.Close()
		return err
	}
	if c, err := net.ListenIP("ip6:ipv6-icmp", nil); err == nil {
		e.v6 = c
		go e.replies(c, false)
	}
	go e.replies(e.v4, true)
	go e.requests()
	s.echo = e
	return nil
}

func (e *echo) requests() {
	buf := make([]byte, 65536)
	for {
		n, err := e.tun.Read(buf)
		if err != nil {
			return
		}
		e.request(buf[:n])
	}
}

func (e *echo) request(b []byte) {
	var src, dst netip.Addr
	var msg []byte
	switch {
	case len(b) >= 20 && b[0]>>4 == 4 && b[9] == 1:
		ihl := int(b[0]&0x0f) * 4
		if len(b) < ihl+8 || b[ihl] != 8 {
			return
		}
		src, dst, msg = netip.AddrFrom4([4]byte(b[12:16])), netip.AddrFrom4([4]byte(b[16:20])), b[ihl:]
	case len(b) >= 48 && b[0]>>4 == 6 && b[6] == 58 && b[40] == 128:
		if e.v6 == nil {
			return
		}
		src, dst, msg = netip.AddrFrom16([16]byte(b[8:24])), netip.AddrFrom16([16]byte(b[24:40])), b[40:]
	default:
		return
	}
	if e.blocked(dst) {
		return
	}
	e.mu.Lock()
	if len(e.pending) >= maxEchoes {
		e.mu.Unlock()
		return
	}
	e.next++
	id, seq := e.next, binary.BigEndian.Uint16(msg[6:8])
	e.pending[echoKey{dst, id, seq}] = echoReq{src: src, id: binary.BigEndian.Uint16(msg[4:6]), at: time.Now()}
	e.mu.Unlock()
	out := append([]byte(nil), msg...)
	binary.BigEndian.PutUint16(out[4:6], id)
	conn := e.v4
	if dst.Is4() {
		out[2], out[3] = 0, 0
		binary.BigEndian.PutUint16(out[2:4], checksum(out, 0))
	} else {
		conn = e.v6
	}
	conn.WriteToIP(out, &net.IPAddr{IP: dst.AsSlice()})
}

func (e *echo) replies(c *net.IPConn, v4 bool) {
	buf := make([]byte, 65536)
	want := byte(129)
	if v4 {
		want = 0
	}
	for {
		n, from, err := c.ReadFromIP(buf)
		if err != nil {
			return
		}
		msg := buf[:n]
		if n < 8 || msg[0] != want || msg[1] != 0 {
			continue
		}
		dst, _ := netip.AddrFromSlice(from.IP)
		key := echoKey{dst.Unmap(), binary.BigEndian.Uint16(msg[4:6]), binary.BigEndian.Uint16(msg[6:8])}
		e.mu.Lock()
		req, ok := e.pending[key]
		delete(e.pending, key)
		e.mu.Unlock()
		if ok {
			e.tun.Write(echoReply(key.dst, req, msg))
		}
	}
}

func echoReply(from netip.Addr, req echoReq, msg []byte) []byte {
	icmp := append([]byte(nil), msg...)
	binary.BigEndian.PutUint16(icmp[4:6], req.id)
	icmp[2], icmp[3] = 0, 0
	if from.Is4() {
		h := make([]byte, 20, 20+len(icmp))
		h[0], h[8], h[9] = 0x45, 64, 1
		binary.BigEndian.PutUint16(h[2:4], uint16(20+len(icmp)))
		f, t := from.As4(), req.src.As4()
		copy(h[12:16], f[:])
		copy(h[16:20], t[:])
		binary.BigEndian.PutUint16(h[10:12], checksum(h, 0))
		binary.BigEndian.PutUint16(icmp[2:4], checksum(icmp, 0))
		return append(h, icmp...)
	}
	h := make([]byte, 40, 40+len(icmp))
	h[0], h[6], h[7] = 0x60, 58, 64
	binary.BigEndian.PutUint16(h[4:6], uint16(len(icmp)))
	f, t := from.As16(), req.src.As16()
	copy(h[8:24], f[:])
	copy(h[24:40], t[:])
	var sum uint32
	for i := 8; i < 40; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(h[i:]))
	}
	sum += uint32(len(icmp)) + 58
	binary.BigEndian.PutUint16(icmp[2:4], checksum(icmp, sum))
	return append(h, icmp...)
}

func checksum(b []byte, sum uint32) uint16 {
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	return ^uint16(sum)
}

func (e *echo) sweep() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, r := range e.pending {
		if time.Since(r.at) > echoTimeout {
			delete(e.pending, k)
		}
	}
}
