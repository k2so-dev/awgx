package awg

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/sysx"
	"golang.org/x/mod/semver"
)

const (
	ConfDir       = "/etc/amnezia/amneziawg"
	ModuleVersion = "/sys/module/amneziawg/version"
)

type Tool struct {
	Run sysx.Runner
}

type PeerStatus struct {
	PublicKey     awgconf.Key `json:"public_key"`
	Endpoint      string      `json:"endpoint"`
	AllowedIPs    []string    `json:"allowed_ips"`
	LastHandshake time.Time   `json:"last_handshake,omitzero"`
	RxBytes       uint64      `json:"rx_bytes"`
	TxBytes       uint64      `json:"tx_bytes"`
}

type Device struct {
	PublicKey  awgconf.Key  `json:"public_key"`
	ListenPort int          `json:"listen_port"`
	Peers      []PeerStatus `json:"peers"`
}

const dumpInterfaceFields = 29

func ParseDump(out string) (Device, error) {
	var d Device
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return d, errors.New("empty dump")
	}
	head := strings.Split(lines[0], "\t")
	if len(head) != dumpInterfaceFields {
		return d, fmt.Errorf("unexpected dump format: %d interface fields, want %d (tools older than 3.1?)", len(head), dumpInterfaceFields)
	}
	d.PublicKey, _ = awgconf.ParseKey(head[1])
	d.ListenPort, _ = strconv.Atoi(head[2])
	for _, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			return d, fmt.Errorf("unexpected peer line with %d fields", len(f))
		}
		var p PeerStatus
		var err error
		if p.PublicKey, err = awgconf.ParseKey(f[0]); err != nil {
			return d, err
		}
		if f[2] != "(none)" {
			p.Endpoint = f[2]
		}
		if f[3] != "(none)" {
			p.AllowedIPs = strings.Split(f[3], ",")
		}
		if ts, _ := strconv.ParseInt(f[4], 10, 64); ts > 0 {
			p.LastHandshake = time.Unix(ts, 0)
		}
		p.RxBytes, _ = strconv.ParseUint(f[5], 10, 64)
		p.TxBytes, _ = strconv.ParseUint(f[6], 10, 64)
		d.Peers = append(d.Peers, p)
	}
	return d, nil
}

func (t Tool) Show(ctx context.Context, dev string) (Device, error) {
	out, err := t.Run.Run(ctx, "awg", "show", dev, "dump")
	if err != nil {
		return Device{}, err
	}
	return ParseDump(string(out))
}

func (t Tool) SetPeer(ctx context.Context, dev string, pub, psk awgconf.Key, allowed []netip.Prefix) error {
	ips := make([]string, len(allowed))
	for i, a := range allowed {
		ips[i] = a.String()
	}
	args := []string{"set", dev, "peer", pub.String()}
	var input []byte
	if !psk.IsZero() {
		args = append(args, "preshared-key", "/dev/stdin")
		input = []byte(psk.String() + "\n")
	}
	args = append(args, "allowed-ips", strings.Join(ips, ","))
	_, err := t.Run.RunInput(ctx, input, "awg", args...)
	return err
}

func (t Tool) RemovePeer(ctx context.Context, dev string, pub awgconf.Key) error {
	_, err := t.Run.Run(ctx, "awg", "set", dev, "peer", pub.String(), "remove")
	return err
}

func (t Tool) SetConf(ctx context.Context, dev string, conf string) error {
	_, err := t.Run.RunInput(ctx, []byte(conf), "awg", "setconf", dev, "/dev/stdin")
	return err
}

var toolsVersion = regexp.MustCompile(`amneziawg-tools v(\S+)`)

func (t Tool) ToolsVersion(ctx context.Context) (string, error) {
	out, err := t.Run.Run(ctx, "awg", "--version")
	if err != nil {
		return "", err
	}
	m := toolsVersion.FindStringSubmatch(string(out))
	if m == nil {
		return "", fmt.Errorf("unexpected awg --version output: %q", strings.TrimSpace(string(out)))
	}
	return m[1], nil
}

func KernelModuleVersion() string {
	return sysx.ReadString(ModuleVersion)
}

func Supports31(version string) bool {
	return semver.Compare("v"+strings.TrimPrefix(strings.TrimSpace(version), "v"), "v3.1.0") >= 0
}

func PeerDiff(dev Device, want []awgconf.Key) (missing, extra int) {
	have := map[awgconf.Key]bool{}
	for _, p := range dev.Peers {
		have[p.PublicKey] = true
	}
	wanted := map[awgconf.Key]bool{}
	for _, k := range want {
		wanted[k] = true
		if !have[k] {
			missing++
		}
	}
	for k := range have {
		if !wanted[k] {
			extra++
		}
	}
	return missing, extra
}

func (t Tool) SyncConf(ctx context.Context, dev string, conf string) error {
	_, err := t.Run.RunInput(ctx, []byte(conf), "awg", "syncconf", dev, "/dev/stdin")
	return err
}
