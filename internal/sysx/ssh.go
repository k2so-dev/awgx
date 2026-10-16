package sysx

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

type SSHSession struct {
	Client netip.Addr `json:"client"`
	Port   int        `json:"port"`
}

func (s SSHSession) String() string {
	if !s.Client.IsValid() {
		return ""
	}
	return netip.AddrPortFrom(s.Client, uint16(s.Port)).String()
}

func CurrentSSH() SSHSession {
	pid := os.Getpid()
	for range 32 {
		if s, ok := sshFromEnviron(pid); ok {
			return s
		}
		ppid := parentPID(pid)
		if ppid <= 1 {
			break
		}
		pid = ppid
	}
	return SSHSession{}
}

func sshFromEnviron(pid int) (SSHSession, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return SSHSession{}, false
	}
	for _, kv := range bytes.Split(b, []byte{0}) {
		v, ok := bytes.CutPrefix(kv, []byte("SSH_CONNECTION="))
		if !ok {
			continue
		}
		f := strings.Fields(string(v))
		if len(f) != 4 {
			return SSHSession{}, false
		}
		client, err := netip.ParseAddr(f[0])
		if err != nil {
			return SSHSession{}, false
		}
		port, err := strconv.Atoi(f[3])
		if err != nil {
			return SSHSession{}, false
		}
		return SSHSession{Client: client.Unmap(), Port: port}, true
	}
	return SSHSession{}, false
}

func parentPID(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}
