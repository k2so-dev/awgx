package state

import (
	"net/netip"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/i1"
)

type Instance struct {
	Name        string              `json:"name"`
	ListenPort  int                 `json:"listen_port"`
	Endpoint    string              `json:"endpoint"`
	MTU         int                 `json:"mtu"`
	Subnet4     netip.Prefix        `json:"subnet4"`
	Subnet6     netip.Prefix        `json:"subnet6"`
	DNS         []netip.Addr        `json:"dns"`
	Preset      string              `json:"preset"`
	Obfuscation awgconf.Obfuscation `json:"obfuscation"`
	I1          i1.Settings         `json:"i1"`
	I1RotatedAt time.Time           `json:"i1_rotated_at"`
	PrivateKey  awgconf.Key         `json:"-"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func (i Instance) Address4() netip.Addr {
	return i.Subnet4.Addr().Next()
}

func (i Instance) Address6() netip.Addr {
	if !i.IPv6() {
		return netip.Addr{}
	}
	return i.Subnet6.Addr().Next()
}

func (i Instance) IPv6() bool {
	return i.Subnet6.IsValid()
}

type Peer struct {
	ID           int64          `json:"-"`
	Instance     string         `json:"instance"`
	Name         string         `json:"name"`
	PublicKey    awgconf.Key    `json:"public_key"`
	PrivateKey   awgconf.Key    `json:"-"`
	PresharedKey awgconf.Key    `json:"-"`
	Address4     netip.Addr     `json:"address4"`
	Address6     netip.Addr     `json:"address6"`
	AllowedIPs   []netip.Prefix `json:"allowed_ips"`
	Egress       string         `json:"egress,omitempty"`
	Killswitch   bool           `json:"killswitch,omitempty"`
	Keepalive    awgconf.Range  `json:"keepalive"`
	I1           *i1.Settings   `json:"i1,omitempty"`
	I1CPS        string         `json:"-"`
	NetLabel     string         `json:"net_label,omitempty"`
	Enabled      bool           `json:"enabled"`
	ExpiresAt    time.Time      `json:"expires_at,omitzero"`
	Note         string         `json:"note,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

func (p Peer) Expired(now time.Time) bool {
	return !p.ExpiresAt.IsZero() && now.After(p.ExpiresAt)
}

func (p Peer) Active(now time.Time) bool {
	return p.Enabled && !p.Expired(now)
}

func (p Peer) I1Profile(inst Instance) string {
	if p.I1 != nil {
		return p.I1.Profile
	}
	return inst.I1.WithDefaults().Profile
}

type Artifact struct {
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

type JournalEntry struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Detail string    `json:"detail"`
	Source string    `json:"source"`
}
