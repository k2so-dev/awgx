package perf

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/sysx"
)

type CPU struct {
	Name    string  `json:"name"`
	Busy    float64 `json:"busy"`
	SoftIRQ float64 `json:"softirq"`
	NetRx   uint64  `json:"net_rx"`
}

type Iface struct {
	Name      string `json:"name"`
	Queues    int    `json:"queues"`
	RPS       bool   `json:"rps"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`
}

type Report struct {
	Interval     time.Duration `json:"interval"`
	CPUs         []CPU         `json:"cpus"`
	Ifaces       []Iface       `json:"ifaces"`
	RcvbufErrors uint64        `json:"rcvbuf_errors"`
	SndbufErrors uint64        `json:"sndbuf_errors"`
	RmemMax      int           `json:"rmem_max"`
	WmemMax      int           `json:"wmem_max"`
	Advice       []string      `json:"advice"`
}

type cpuTimes struct {
	total, idle, softirq uint64
}

func readStat() map[string]cpuTimes {
	m := map[string]cpuTimes{}
	f, err := os.Open("/proc/stat")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || !strings.HasPrefix(fields[0], "cpu") || fields[0] == "cpu" {
			continue
		}
		var t cpuTimes
		for i, v := range fields[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i == 8 || i == 9 {
				continue
			}
			t.total += n
			if i == 3 || i == 4 {
				t.idle += n
			}
			if i == 6 {
				t.softirq = n
			}
		}
		m[fields[0]] = t
	}
	return m
}

func readNetRx() map[string]uint64 {
	m := map[string]uint64{}
	b, err := os.ReadFile("/proc/softirqs")
	if err != nil {
		return m
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 2 {
		return m
	}
	names := strings.Fields(lines[0])
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) == 0 || f[0] != "NET_RX:" {
			continue
		}
		for i, v := range f[1:] {
			if i < len(names) {
				n, _ := strconv.ParseUint(v, 10, 64)
				m[strings.ToLower(names[i])] = n
			}
		}
	}
	return m
}

func ReadSNMP(path, proto string) map[string]uint64 {
	m := map[string]uint64{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	var header []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != proto+":" {
			continue
		}
		if header == nil {
			header = fields[1:]
			continue
		}
		for i, v := range fields[1:] {
			if i < len(header) {
				n, _ := strconv.ParseUint(v, 10, 64)
				m[header[i]] = n
			}
		}
	}
	return m
}

func counter(dev, name string) uint64 {
	v, _ := sysx.ReadInt(filepath.Join("/sys/class/net", dev, "statistics", name))
	return uint64(v)
}

func iface(dev string) Iface {
	i := Iface{Name: dev}
	queues, _ := filepath.Glob(filepath.Join("/sys/class/net", dev, "queues", "rx-*"))
	i.Queues = len(queues)
	for _, q := range queues {
		v := strings.Trim(sysx.ReadString(filepath.Join(q, "rps_cpus")), "0,\n")
		if v != "" {
			i.RPS = true
		}
	}
	return i
}

func Sample(ctx context.Context, devs []string, interval time.Duration) (Report, error) {
	r := Report{Interval: interval}
	stat0, rx0 := readStat(), readNetRx()
	udp0 := ReadSNMP("/proc/net/snmp", "Udp")
	drops0 := map[string][2]uint64{}
	for _, d := range devs {
		drops0[d] = [2]uint64{counter(d, "rx_dropped"), counter(d, "tx_dropped")}
	}
	select {
	case <-ctx.Done():
		return r, ctx.Err()
	case <-time.After(interval):
	}
	stat1, rx1 := readStat(), readNetRx()
	udp1 := ReadSNMP("/proc/net/snmp", "Udp")
	for n := range 4096 {
		name := fmt.Sprintf("cpu%d", n)
		a, ok0 := stat0[name]
		b, ok1 := stat1[name]
		if !ok0 || !ok1 {
			continue
		}
		dt := float64(b.total - a.total)
		c := CPU{Name: name, NetRx: rx1[name] - rx0[name]}
		if dt > 0 {
			c.Busy = 100 * (dt - float64(b.idle-a.idle)) / dt
			c.SoftIRQ = 100 * float64(b.softirq-a.softirq) / dt
		}
		r.CPUs = append(r.CPUs, c)
		if len(r.CPUs) == len(stat1) {
			break
		}
	}
	r.RcvbufErrors = udp1["RcvbufErrors"] - udp0["RcvbufErrors"]
	r.SndbufErrors = udp1["SndbufErrors"] - udp0["SndbufErrors"]
	r.RmemMax, _ = sysx.ReadInt("/proc/sys/net/core/rmem_max")
	r.WmemMax, _ = sysx.ReadInt("/proc/sys/net/core/wmem_max")
	for _, d := range devs {
		i := iface(d)
		i.RxDropped = counter(d, "rx_dropped") - drops0[d][0]
		i.TxDropped = counter(d, "tx_dropped") - drops0[d][1]
		r.Ifaces = append(r.Ifaces, i)
	}
	r.Advice = advise(r)
	return r, nil
}

func advise(r Report) []string {
	var out []string
	if r.RcvbufErrors > 0 {
		out = append(out, fmt.Sprintf("%d UDP receive buffer errors in %s: raise net.core.rmem_max (now %d), awgx doctor --fix sets 32 MiB", r.RcvbufErrors, r.Interval, r.RmemMax))
	}
	if r.SndbufErrors > 0 {
		out = append(out, fmt.Sprintf("%d UDP send buffer errors in %s: raise net.core.wmem_max (now %d)", r.SndbufErrors, r.Interval, r.WmemMax))
	}
	var hot, idle int
	for _, c := range r.CPUs {
		if c.SoftIRQ >= 80 {
			hot++
		} else if c.Busy < 50 {
			idle++
		}
	}
	if hot > 0 && idle > 0 {
		for _, i := range r.Ifaces {
			if !i.RPS && i.Queues <= 1 && !strings.HasPrefix(i.Name, "awg") {
				mask := fmt.Sprintf("%x", (1<<min(len(r.CPUs), 32))-1)
				out = append(out, fmt.Sprintf("one core is saturated by softirq while others idle and %s has a single queue without RPS: echo %s > /sys/class/net/%s/queues/rx-0/rps_cpus", i.Name, mask, i.Name))
			}
		}
	}
	for _, i := range r.Ifaces {
		if i.RxDropped > 0 || i.TxDropped > 0 {
			out = append(out, fmt.Sprintf("%s dropped %d rx / %d tx packets in %s", i.Name, i.RxDropped, i.TxDropped, r.Interval))
		}
	}
	return out
}
