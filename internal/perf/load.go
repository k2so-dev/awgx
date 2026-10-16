package perf

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/k2so-dev/awgx/internal/sysx"
)

type Load struct {
	Busy    float64 `json:"busy"`
	SoftIRQ float64 `json:"softirq"`
	Retrans uint64  `json:"retrans"`
	Sent    uint64  `json:"sent"`
}

type LoadMark struct {
	stat map[string]cpuTimes
	tcp  map[string]uint64
	read func() map[string]uint64
}

func MarkLoadFrom(tcp func() map[string]uint64) LoadMark {
	return LoadMark{stat: readStat(), tcp: tcp(), read: tcp}
}

func (m LoadMark) Since() Load {
	stat, tcp := readStat(), m.read()
	var total, idle, soft uint64
	for name, t := range stat {
		p, ok := m.stat[name]
		if !ok {
			continue
		}
		total += t.total - p.total
		idle += t.idle - p.idle
		soft += t.softirq - p.softirq
	}
	l := Load{Retrans: tcp["RetransSegs"] - m.tcp["RetransSegs"], Sent: tcp["OutSegs"] - m.tcp["OutSegs"]}
	if total > 0 {
		l.Busy = float64(total-idle) * 100 / float64(total)
		l.SoftIRQ = float64(soft) * 100 / float64(total)
	}
	return l
}

type Ping struct {
	Sent     int     `json:"sent"`
	Received int     `json:"received"`
	Loss     float64 `json:"loss"`
	Min      float64 `json:"min"`
	Avg      float64 `json:"avg"`
	Max      float64 `json:"max"`
	Mdev     float64 `json:"mdev"`
}

func (p Ping) String() string {
	if p.Received == 0 {
		return fmt.Sprintf("loss %.1f%% (%d sent, no replies)", p.Loss, p.Sent)
	}
	return fmt.Sprintf("loss %.1f%% (%d/%d)  rtt %.1f/%.1f/%.1f ms  jitter %.1f ms", p.Loss, p.Sent-p.Received, p.Sent, p.Min, p.Avg, p.Max, p.Mdev)
}

var (
	pingCount = regexp.MustCompile(`(\d+) packets transmitted, (\d+) received`)
	pingRTT   = regexp.MustCompile(`= ([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+) ms`)
)

func ParsePing(out string) (Ping, error) {
	var p Ping
	m := pingCount.FindStringSubmatch(out)
	if m == nil {
		return p, fmt.Errorf("unexpected ping output: %q", out)
	}
	p.Sent, _ = strconv.Atoi(m[1])
	p.Received, _ = strconv.Atoi(m[2])
	if p.Sent > 0 {
		p.Loss = float64(p.Sent-p.Received) * 100 / float64(p.Sent)
	}
	if r := pingRTT.FindStringSubmatch(out); r != nil {
		p.Min, _ = strconv.ParseFloat(r[1], 64)
		p.Avg, _ = strconv.ParseFloat(r[2], 64)
		p.Max, _ = strconv.ParseFloat(r[3], 64)
		p.Mdev, _ = strconv.ParseFloat(r[4], 64)
	}
	return p, nil
}

func PingStats(ctx context.Context, run sysx.Runner, host string, mark uint32, d time.Duration) (Ping, error) {
	count := max(int(d/(200*time.Millisecond)), 1)
	args := []string{"-q", "-n", "-c", strconv.Itoa(count), "-i", "0.2", "-W", "1", "-w", strconv.Itoa(int(d.Seconds()) + 2)}
	if mark != 0 {
		args = append(args, "-m", strconv.FormatUint(uint64(mark), 10))
	}
	out, err := run.Run(ctx, "ping", append(args, host)...)
	p, perr := ParsePing(string(out))
	if perr != nil {
		if err != nil {
			return p, err
		}
		return p, perr
	}
	return p, nil
}
