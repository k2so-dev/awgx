package relay

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	SpecPath     = "/run/awgx/relay.json"
	StatsPath    = "/run/awgx/relay-stats.json"
	dialTimeout  = 10 * time.Second
	udpIdle      = 2 * time.Minute
	udpShortIdle = 20 * time.Second
	maxSessions  = 16384
	maxReplies   = 16384
	maxQueued    = 32
	failedHold   = 5 * time.Second
)

type Match struct {
	Addrs  []netip.Prefix `json:"addrs"`
	Except []netip.Addr   `json:"except,omitempty"`
}

type Listener struct {
	Port    int     `json:"port"`
	Name    string  `json:"name"`
	Socks   *Socks  `json:"socks,omitempty"`
	Sources []Match `json:"sources,omitempty"`
}

type Spec struct {
	Listeners []Listener `json:"listeners"`
}

func WriteSpec(path string, s Spec) (bool, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return false, err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return false, nil
	}
	return true, writeFile(path, b)
}

func ReadSpec(path string) (Spec, error) {
	var s Spec
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Stats struct {
	Port int    `json:"port"`
	TCP  uint64 `json:"tcp"`
	UDP  uint64 `json:"udp"`
}

func ReadStats(path string) (map[int]Stats, error) {
	var list []Stats
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	m := map[int]Stats{}
	for _, s := range list {
		m[s.Port] = s
	}
	return m, nil
}

type nsThread struct {
	req chan func()
}

func startNS(path string) (*nsThread, error) {
	t := &nsThread{req: make(chan func())}
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		f, err := os.Open(path)
		if err == nil {
			err = unix.Setns(int(f.Fd()), unix.CLONE_NEWNET)
			f.Close()
		}
		errc <- err
		if err != nil {
			return
		}
		for fn := range t.req {
			fn()
		}
	}()
	return t, <-errc
}

func (t *nsThread) do(fn func() error) error {
	done := make(chan error, 1)
	t.req <- func() { done <- fn() }
	return <-done
}

type port struct {
	cfg      atomic.Pointer[Listener]
	tcp      []net.Listener
	udp      []*net.UDPConn
	mu       sync.Mutex
	closed   bool
	sessions map[netip.AddrPort]*session
	conns    map[*net.TCPConn]*flow
	stats    struct {
		tcp, udp atomic.Uint64
	}
}

type flow struct {
	src     netip.Addr
	in, out *net.TCPConn
}

func (f *flow) close() {
	f.in.SetLinger(0)
	f.in.Close()
	f.out.Close()
}

type Server struct {
	Log     func(string)
	ns      *nsThread
	mu      sync.Mutex
	ports   map[int]*port
	replyMu sync.Mutex
	replies map[netip.AddrPort]*reply
	localMu sync.RWMutex
	local   []netip.Addr
	echo    *echo
}

type reply struct {
	ready chan struct{}
	conn  *net.UDPConn
	err   error
	last  atomic.Int64
}

func NewServer(nsPath string, log func(string)) (*Server, error) {
	t, err := startNS(nsPath)
	if err != nil {
		return nil, fmt.Errorf("enter %s: %w", nsPath, err)
	}
	s := &Server{Log: log, ns: t, ports: map[int]*port{}, replies: map[netip.AddrPort]*reply{}}
	s.refreshLocal()
	return s, nil
}

func (s *Server) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if n%5 == 0 {
				s.refreshLocal()
				s.sweepReplies()
				if s.echo != nil {
					s.echo.sweep()
				}
			}
			s.WriteStats()
		}
	}
}

func (s *Server) refreshLocal() {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	var out []netip.Addr
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	s.localMu.Lock()
	s.local = out
	s.localMu.Unlock()
}

func (s *Server) blocked(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() {
		return true
	}
	s.localMu.RLock()
	defer s.localMu.RUnlock()
	return slices.Contains(s.local, a)
}

func (s *Server) Apply(spec Spec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[int]Listener{}
	for _, l := range spec.Listeners {
		want[l.Port] = l
	}
	for n, p := range s.ports {
		if _, ok := want[n]; !ok {
			p.close()
			delete(s.ports, n)
		}
	}
	var errs []error
	for n, l := range want {
		if p, ok := s.ports[n]; ok {
			old := p.cfg.Load()
			p.cfg.Store(&l)
			p.prune(func(src netip.Addr) bool { return sameUpstream(old, &l) && l.Owns(src) })
			continue
		}
		p, err := s.listen(l)
		if err != nil {
			errs = append(errs, fmt.Errorf("relay %s :%d: %w", l.Name, n, err))
			continue
		}
		s.ports[n] = p
	}
	return errors.Join(errs...)
}

func sameUpstream(a, b *Listener) bool {
	if a.Socks == nil || b.Socks == nil {
		return a.Socks == b.Socks
	}
	return *a.Socks == *b.Socks
}

func (l *Listener) Owns(a netip.Addr) bool {
	for _, m := range l.Sources {
		if slices.Contains(m.Except, a) {
			continue
		}
		for _, p := range m.Addrs {
			if p.Contains(a) {
				return true
			}
		}
	}
	return false
}

func transparent(network string, udp bool) *net.ListenConfig {
	v6 := network == "tcp6" || network == "udp6"
	return &net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			s := int(fd)
			set := func(level, opt int) {
				if serr == nil {
					serr = unix.SetsockoptInt(s, level, opt, 1)
				}
			}
			set(unix.SOL_SOCKET, unix.SO_REUSEADDR)
			if v6 {
				set(unix.SOL_IPV6, unix.IPV6_TRANSPARENT)
				if udp {
					set(unix.SOL_IPV6, unix.IPV6_RECVORIGDSTADDR)
				}
			} else {
				set(unix.SOL_IP, unix.IP_TRANSPARENT)
				if udp {
					set(unix.SOL_IP, unix.IP_RECVORIGDSTADDR)
				}
			}
		})
		if err != nil {
			return err
		}
		return serr
	}}
}

func (s *Server) listen(l Listener) (*port, error) {
	p := &port{sessions: map[netip.AddrPort]*session{}, conns: map[*net.TCPConn]*flow{}}
	p.cfg.Store(&l)
	err := s.ns.do(func() error {
		for _, f := range []struct{ tcp, udp, addr string }{{"tcp4", "udp4", "0.0.0.0"}, {"tcp6", "udp6", "::"}} {
			addr := net.JoinHostPort(f.addr, fmt.Sprint(l.Port))
			tl, err := transparent(f.tcp, false).Listen(context.Background(), f.tcp, addr)
			if err != nil {
				if f.tcp == "tcp6" {
					continue
				}
				return err
			}
			p.tcp = append(p.tcp, tl)
			ul, err := transparent(f.udp, true).ListenPacket(context.Background(), f.udp, addr)
			if err != nil {
				return err
			}
			u := ul.(*net.UDPConn)
			u.SetReadBuffer(4 << 20)
			p.udp = append(p.udp, u)
		}
		return nil
	})
	if err != nil {
		p.close()
		return nil, err
	}
	for _, tl := range p.tcp {
		go s.serveTCP(p, tl)
	}
	for _, u := range p.udp {
		go s.serveUDP(p, u)
	}
	return p, nil
}

func (p *port) close() {
	for _, l := range p.tcp {
		l.Close()
	}
	for _, u := range p.udp {
		u.Close()
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.prune(func(netip.Addr) bool { return false })
}

func (p *port) prune(keep func(netip.Addr) bool) {
	p.mu.Lock()
	var drop []*flow
	for c, f := range p.conns {
		if !keep(f.src) {
			drop = append(drop, f)
			delete(p.conns, c)
		}
	}
	var stale []*session
	for k, ss := range p.sessions {
		if !keep(k.Addr()) {
			stale = append(stale, ss)
			delete(p.sessions, k)
		}
	}
	p.mu.Unlock()
	for _, f := range drop {
		f.close()
	}
	for _, ss := range stale {
		ss.close()
	}
}

func (s *Server) serveTCP(p *port, l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go s.handleTCP(p, c.(*net.TCPConn))
	}
}

func (s *Server) handleTCP(p *port, in *net.TCPConn) {
	defer in.Close()
	p.stats.tcp.Add(1)
	dst := in.LocalAddr().(*net.TCPAddr).AddrPort()
	dst = netip.AddrPortFrom(dst.Addr().Unmap(), dst.Port())
	src := in.RemoteAddr().(*net.TCPAddr).AddrPort().Addr().Unmap()
	if s.blocked(dst.Addr()) {
		in.SetLinger(0)
		return
	}
	cfg := p.cfg.Load()
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	out, err := dialTCP(ctx, cfg, dst)
	cancel()
	if err != nil {
		in.SetLinger(0)
		return
	}
	defer out.Close()
	f := &flow{src: src, in: in, out: out}
	p.mu.Lock()
	cur := p.cfg.Load()
	if p.closed || cur != cfg && !(sameUpstream(cfg, cur) && cur.Owns(src)) {
		p.mu.Unlock()
		f.close()
		return
	}
	p.conns[in] = f
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conns, in)
		p.mu.Unlock()
	}()
	in.SetKeepAlive(true)
	out.SetKeepAlive(true)
	done := make(chan struct{})
	go func() {
		io.Copy(out, in)
		out.CloseWrite()
		close(done)
	}()
	io.Copy(in, out)
	in.CloseWrite()
	<-done
}

func dialTCP(ctx context.Context, cfg *Listener, dst netip.AddrPort) (*net.TCPConn, error) {
	if cfg.Socks != nil {
		return cfg.Socks.Connect(ctx, dst)
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", dst.String())
	if err != nil {
		return nil, err
	}
	return c.(*net.TCPConn), nil
}

type datagram struct {
	data []byte
	dst  netip.AddrPort
}

type session struct {
	src        netip.AddrPort
	mu         sync.Mutex
	conn       *net.UDPConn
	assoc      *Association
	queue      []datagram
	failed     bool
	closed     bool
	last       atomic.Int64
	sent, recv atomic.Uint32
}

func (ss *session) close() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed {
		return
	}
	ss.closed = true
	if ss.assoc != nil {
		ss.assoc.Close()
	} else if ss.conn != nil {
		ss.conn.Close()
	}
}

func (ss *session) idle() time.Duration {
	if ss.sent.Load() <= 1 && ss.recv.Load() <= 1 {
		return udpShortIdle
	}
	return udpIdle
}

func (ss *session) send(b []byte, dst netip.AddrPort) error {
	ss.last.Store(time.Now().UnixNano())
	ss.sent.Add(1)
	ss.mu.Lock()
	if ss.conn == nil {
		if !ss.failed && !ss.closed && len(ss.queue) < maxQueued {
			ss.queue = append(ss.queue, datagram{append([]byte(nil), b...), dst})
		}
		ss.mu.Unlock()
		return nil
	}
	conn, assoc := ss.conn, ss.assoc
	ss.mu.Unlock()
	return write(conn, assoc != nil, b, dst)
}

func write(conn *net.UDPConn, socks bool, b []byte, dst netip.AddrPort) error {
	if socks {
		pkt := AppendAddr(make([]byte, 3, len(b)+22), dst)
		_, err := conn.Write(append(pkt, b...))
		return err
	}
	_, err := conn.WriteToUDPAddrPort(b, dst)
	return err
}

func origDst(oob []byte) (netip.AddrPort, bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return netip.AddrPort{}, false
	}
	for _, m := range msgs {
		switch {
		case m.Header.Level == unix.SOL_IP && m.Header.Type == unix.IP_ORIGDSTADDR && len(m.Data) >= 8:
			return netip.AddrPortFrom(netip.AddrFrom4([4]byte(m.Data[4:8])), binary.BigEndian.Uint16(m.Data[2:4])), true
		case m.Header.Level == unix.SOL_IPV6 && m.Header.Type == unix.IPV6_ORIGDSTADDR && len(m.Data) >= 24:
			return netip.AddrPortFrom(netip.AddrFrom16([16]byte(m.Data[8:24])).Unmap(), binary.BigEndian.Uint16(m.Data[2:4])), true
		}
	}
	return netip.AddrPort{}, false
}

func (s *Server) serveUDP(p *port, l *net.UDPConn) {
	buf := make([]byte, 65535)
	oob := make([]byte, 256)
	for {
		n, oobn, _, src, err := l.ReadMsgUDPAddrPort(buf, oob)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		dst, ok := origDst(oob[:oobn])
		if !ok || s.blocked(dst.Addr()) {
			continue
		}
		src = netip.AddrPortFrom(src.Addr().Unmap(), src.Port())
		ss := s.session(p, src)
		if ss == nil {
			continue
		}
		ss.send(buf[:n], dst)
	}
}

func (s *Server) session(p *port, src netip.AddrPort) *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ss := p.sessions[src]; ss != nil || p.closed || len(p.sessions) >= maxSessions {
		return ss
	}
	cfg := p.cfg.Load()
	if cfg.Socks != nil && cfg.Socks.NoUDP {
		return nil
	}
	ss := &session{src: src}
	ss.last.Store(time.Now().UnixNano())
	p.sessions[src] = ss
	p.stats.udp.Add(1)
	go s.open(p, ss, cfg)
	return ss
}

func (s *Server) open(p *port, ss *session, cfg *Listener) {
	var conn *net.UDPConn
	var assoc *Association
	var err error
	if cfg.Socks != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		var a Association
		a, err = cfg.Socks.Associate(ctx)
		cancel()
		if err == nil {
			assoc, conn = &a, a.UDP
		}
	} else if conn, err = net.ListenUDP("udp", nil); err == nil {
		conn.SetReadBuffer(1 << 20)
	}
	ss.mu.Lock()
	if err != nil || ss.closed {
		ss.failed = true
		ss.queue = nil
		ss.mu.Unlock()
		if err == nil && assoc != nil {
			assoc.Close()
		} else if err == nil {
			conn.Close()
		}
		time.AfterFunc(failedHold, func() { p.forget(ss) })
		return
	}
	ss.conn, ss.assoc = conn, assoc
	for _, d := range ss.queue {
		write(conn, assoc != nil, d.data, d.dst)
	}
	ss.queue = nil
	ss.mu.Unlock()
	if assoc != nil {
		go func() {
			io.Copy(io.Discard, assoc.Control)
			ss.close()
		}()
	}
	s.sessionLoop(p, ss)
}

func (p *port) forget(ss *session) {
	p.mu.Lock()
	if p.sessions[ss.src] == ss {
		delete(p.sessions, ss.src)
	}
	p.mu.Unlock()
}

func (s *Server) sessionLoop(p *port, ss *session) {
	defer func() {
		ss.close()
		p.forget(ss)
	}()
	buf := make([]byte, 65535)
	for {
		ss.conn.SetReadDeadline(time.Now().Add(ss.idle()))
		n, from, err := ss.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && time.Since(time.Unix(0, ss.last.Load())) < ss.idle() {
				continue
			}
			return
		}
		data := buf[:n]
		if ss.assoc != nil {
			addr, off, err := ParseUDPHeader(data)
			if err != nil {
				continue
			}
			from, data = addr, data[off:]
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		r, err := s.reply(from)
		if err != nil {
			continue
		}
		ss.last.Store(time.Now().UnixNano())
		ss.recv.Add(1)
		r.conn.WriteToUDPAddrPort(data, ss.src)
	}
}

func (s *Server) reply(from netip.AddrPort) (*reply, error) {
	s.replyMu.Lock()
	r := s.replies[from]
	if r == nil {
		if len(s.replies) >= maxReplies {
			s.replyMu.Unlock()
			return nil, errors.New("too many reply sockets")
		}
		r = &reply{ready: make(chan struct{})}
		r.last.Store(time.Now().UnixNano())
		s.replies[from] = r
		s.replyMu.Unlock()
		r.conn, r.err = s.replySocket(from)
		close(r.ready)
		if r.err != nil {
			s.replyMu.Lock()
			if s.replies[from] == r {
				delete(s.replies, from)
			}
			s.replyMu.Unlock()
		}
	} else {
		s.replyMu.Unlock()
		<-r.ready
	}
	if r.err != nil {
		return nil, r.err
	}
	r.last.Store(time.Now().UnixNano())
	return r, nil
}

func (s *Server) replySocket(from netip.AddrPort) (*net.UDPConn, error) {
	network := "udp4"
	if from.Addr().Is6() {
		network = "udp6"
	}
	var conn *net.UDPConn
	err := s.ns.do(func() error {
		c, err := transparent(network, false).ListenPacket(context.Background(), network, from.String())
		if err != nil {
			return err
		}
		conn = c.(*net.UDPConn)
		return nil
	})
	return conn, err
}

func (s *Server) sweepReplies() {
	cut := time.Now().Add(-udpIdle).UnixNano()
	s.replyMu.Lock()
	defer s.replyMu.Unlock()
	for k, r := range s.replies {
		select {
		case <-r.ready:
		default:
			continue
		}
		if r.err == nil && r.last.Load() < cut {
			r.conn.Close()
			delete(s.replies, k)
		}
	}
}

func (s *Server) WriteStats() {
	s.mu.Lock()
	var out []Stats
	for n, p := range s.ports {
		out = append(out, Stats{Port: n, TCP: p.stats.tcp.Load(), UDP: p.stats.udp.Load()})
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b Stats) int { return a.Port - b.Port })
	if b, err := json.Marshal(out); err == nil {
		writeFile(StatsPath, b)
	}
}
