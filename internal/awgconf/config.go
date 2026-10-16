package awgconf

import "net/netip"

type Obfuscation struct {
	Jc                     uint16    `json:"jc"`
	Jmin                   uint16    `json:"jmin"`
	Jmax                   uint16    `json:"jmax"`
	S1                     uint16    `json:"s1"`
	S2                     uint16    `json:"s2"`
	S3                     uint16    `json:"s3"`
	S4                     uint16    `json:"s4"`
	H1                     Range     `json:"h1"`
	H2                     Range     `json:"h2"`
	H3                     Range     `json:"h3"`
	H4                     Range     `json:"h4"`
	I                      [5]string `json:"i"`
	HeaderProtectionKey    Key       `json:"-"`
	RandomTrailers         bool      `json:"random_trailers"`
	DisableCookies         bool      `json:"disable_cookies"`
	ContentPaddingAddition Range     `json:"content_padding_addition"`
	RekeyAfterTime         Range     `json:"rekey_after_time"`
	RekeyTimeout           Range     `json:"rekey_timeout"`
	RejectAfterTime        Range     `json:"reject_after_time"`
	KeepaliveTimeout       Range     `json:"keepalive_timeout"`
	MaxHandshakeAttempts   Range     `json:"max_handshake_attempts"`
}

func (o Obfuscation) H() [4]Range {
	return [4]Range{o.H1, o.H2, o.H3, o.H4}
}

func (o Obfuscation) S() [4]uint16 {
	return [4]uint16{o.S1, o.S2, o.S3, o.S4}
}

func (o Obfuscation) ServerSide() Obfuscation {
	return Obfuscation{
		S1: o.S1, S2: o.S2, S3: o.S3, S4: o.S4,
		H1: o.H1, H2: o.H2, H3: o.H3, H4: o.H4,
		HeaderProtectionKey: o.HeaderProtectionKey,
		RandomTrailers:      o.RandomTrailers,
		DisableCookies:      o.DisableCookies,
	}
}

func (o Obfuscation) SameServerSide(other Obfuscation) bool {
	return o.ServerSide() == other.ServerSide()
}

type Interface struct {
	PrivateKey Key
	ListenPort int
	Address    []netip.Prefix
	DNS        []netip.Addr
	MTU        int
	Table      string
	PostUp     []string
	PostDown   []string
	Obfuscation
}

type Peer struct {
	PublicKey           Key
	PresharedKey        Key
	Endpoint            string
	AllowedIPs          []netip.Prefix
	PersistentKeepalive Range
}

type Config struct {
	Interface Interface
	Peers     []Peer
}
