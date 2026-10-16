package perf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/k2so-dev/awgx/internal/fmtx"
)

const (
	modeSend    = 's'
	modeReceive = 'r'
	AgentPort   = 5201
)

type Throughput struct {
	Direction string        `json:"direction"`
	Bytes     uint64        `json:"bytes"`
	Duration  time.Duration `json:"duration"`
	Mbps      float64       `json:"mbps"`
}

func result(dir string, n uint64, d time.Duration) Throughput {
	return Throughput{Direction: dir, Bytes: n, Duration: d.Round(time.Millisecond), Mbps: float64(n) * 8 / d.Seconds() / 1e6}
}

var buf = make([]byte, 128<<10)

func pump(c net.Conn, d time.Duration) (uint64, time.Duration) {
	start := time.Now()
	c.SetWriteDeadline(start.Add(d))
	var n uint64
	for {
		w, err := c.Write(buf)
		n += uint64(w)
		if err != nil {
			return n, time.Since(start)
		}
	}
}

func drain(c net.Conn, d time.Duration) (uint64, time.Duration) {
	start := time.Now()
	c.SetReadDeadline(start.Add(d + 5*time.Second))
	b := make([]byte, 128<<10)
	var n uint64
	for {
		r, err := c.Read(b)
		n += uint64(r)
		if err != nil {
			return n, time.Since(start)
		}
	}
}

func Serve(ctx context.Context, l net.Listener) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handle(c)
	}
}

func handle(c net.Conn) {
	defer c.Close()
	head := make([]byte, 3)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	d := time.Duration(int(head[1])<<8|int(head[2])) * time.Second
	if d <= 0 || d > 120*time.Second {
		return
	}
	switch head[0] {
	case modeSend:
		drain(c, d)
	case modeReceive:
		pump(c, d)
	}
}

type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func Transfer(ctx context.Context, dial DialFunc, addr string, send bool, d time.Duration, streams int, label string) (Throughput, error) {
	mode := byte(modeReceive)
	if send {
		mode = modeSend
	}
	secs := int(d.Seconds())
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for range max(streams, 1) {
		c, err := dial(ctx, "tcp", addr)
		if err != nil {
			return Throughput{Direction: label}, err
		}
		conns = append(conns, c)
		if _, err := c.Write([]byte{mode, byte(secs >> 8), byte(secs)}); err != nil {
			return Throughput{Direction: label}, err
		}
	}
	type part struct {
		n  uint64
		el time.Duration
	}
	parts := make(chan part, len(conns))
	for _, c := range conns {
		go func() {
			var n uint64
			var el time.Duration
			if send {
				n, el = pump(c, d)
			} else {
				n, el = drain(c, d)
			}
			parts <- part{n, el}
		}()
	}
	var total uint64
	var longest time.Duration
	for range conns {
		p := <-parts
		total += p.n
		longest = max(longest, p.el)
	}
	if total == 0 {
		return result(label, 0, longest), errors.New("no data passed")
	}
	return result(label, total, longest), nil
}

func Measure(ctx context.Context, addr string, d time.Duration, sendLabel, recvLabel string) ([]Throughput, error) {
	dial := (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	var out []Throughput
	for _, send := range []bool{true, false} {
		label := recvLabel
		if send {
			label = sendLabel
		}
		t, err := Transfer(ctx, dial, addr, send, d, 1, label)
		out = append(out, t)
		if err != nil {
			return out, fmt.Errorf("%s: %w", label, err)
		}
	}
	return out, nil
}

func Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

func AgentAddr(host string) string {
	return net.JoinHostPort(host, strconv.Itoa(AgentPort))
}

func Describe(t []Throughput) []string {
	var out []string
	for _, x := range t {
		out = append(out, fmt.Sprintf("%-15s %8.1f Mbit/s (%s in %s)", x.Direction, x.Mbps, fmtx.Bytes(x.Bytes), x.Duration))
	}
	return out
}
