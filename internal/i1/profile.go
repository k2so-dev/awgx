package i1

import (
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"slices"
	"strings"

	"github.com/k2so-dev/awgx/internal/cps"
	"github.com/k2so-dev/awgx/internal/randx"
)

const (
	None   = "none"
	Noise  = "noise"
	QUIC   = "quic"
	STUN   = "stun"
	DNS    = "dns"
	Custom = "custom"
)

func Names() []string {
	return []string{None, Noise, QUIC, STUN, DNS, Custom}
}

func Valid(name string) bool {
	return slices.Contains(Names(), name)
}

func Static(name string) bool {
	return name == QUIC || name == STUN || name == DNS || name == Custom
}

type Settings struct {
	Profile     string `json:"profile"`
	Fingerprint string `json:"fingerprint,omitempty"`
	SNI         string `json:"sni,omitempty"`
	NoiseMin    int    `json:"noise_min,omitempty"`
	NoiseMax    int    `json:"noise_max,omitempty"`
	Custom      string `json:"custom,omitempty"`
}

func (s Settings) Over(old Settings) Settings {
	s.Fingerprint = cmp.Or(s.Fingerprint, old.Fingerprint)
	s.SNI = cmp.Or(s.SNI, old.SNI)
	s.Custom = cmp.Or(s.Custom, old.Custom)
	if s.NoiseMin == 0 && s.NoiseMax == 0 {
		s.NoiseMin, s.NoiseMax = old.NoiseMin, old.NoiseMax
	}
	return s
}

func (s Settings) WithDefaults() Settings {
	if s.Profile == "" {
		s.Profile = None
	}
	if s.Fingerprint == "" {
		s.Fingerprint = "chrome"
	}
	if s.NoiseMin == 0 {
		s.NoiseMin = 32
	}
	if s.NoiseMax == 0 {
		s.NoiseMax = 64
	}
	return s
}

func Generate(s Settings) (string, error) {
	s = s.WithDefaults()
	switch s.Profile {
	case None:
		return "", nil
	case Noise:
		if s.NoiseMin < 1 || s.NoiseMax < s.NoiseMin {
			return "", fmt.Errorf("invalid noise range %d-%d", s.NoiseMin, s.NoiseMax)
		}
		return fmt.Sprintf("<r %d>", randx.Between(s.NoiseMin, s.NoiseMax)), nil
	case QUIC:
		if s.SNI == "" {
			return "", fmt.Errorf("quic profile needs an SNI")
		}
		pkt, err := QUICInitial(QUICParams{Fingerprint: s.Fingerprint, SNI: s.SNI, SCIDLen: scidLen(s.Fingerprint)})
		if err != nil {
			return "", err
		}
		if _, err := DecryptQUICInitial(pkt); err != nil {
			return "", fmt.Errorf("quic self-check: %w", err)
		}
		return cps.Bytes(pkt).String(), nil
	case STUN:
		return cps.Bytes(stunBindingRequest()).String(), nil
	case DNS:
		return cps.Bytes(dnsQuery()).String(), nil
	case Custom:
		p, err := cps.Parse(s.Custom)
		if err != nil {
			return "", err
		}
		if len(p) == 0 {
			return "", fmt.Errorf("custom profile needs a CPS string")
		}
		return p.String(), nil
	}
	return "", fmt.Errorf("unknown I1 profile %q", s.Profile)
}

func scidLen(fingerprint string) int {
	if fingerprint == "chrome" {
		return 0
	}
	return 8
}

func stunBindingRequest() []byte {
	var attrs []byte
	user := randLetters(4) + ":" + randLetters(4)
	attrs = stunAttr(attrs, 0x0006, []byte(user))
	attrs = stunAttr(attrs, 0xc057, []byte{0x00, 0x01, 0x00, 0x0a})
	attrs = stunAttr(attrs, 0x802a, randBytes(8))
	attrs = stunAttr(attrs, 0x0024, binary.BigEndian.AppendUint32(nil, uint32(randx.Between(1845494015, 2130706431))))
	header := func(bodyLen int) []byte {
		h := []byte{0x00, 0x01}
		h = binary.BigEndian.AppendUint16(h, uint16(bodyLen))
		h = binary.BigEndian.AppendUint32(h, 0x2112a442)
		return h
	}
	tid := randBytes(12)
	msg := append(append(header(len(attrs)+24), tid...), attrs...)
	mac := hmac.New(sha1.New, randBytes(22))
	mac.Write(msg)
	attrs = stunAttr(attrs, 0x0008, mac.Sum(nil))
	msg = append(append(header(len(attrs)+8), tid...), attrs...)
	crc := crc32.ChecksumIEEE(msg) ^ 0x5354554e
	attrs = stunAttr(attrs, 0x8028, binary.BigEndian.AppendUint32(nil, crc))
	return append(append(header(len(attrs)), tid...), attrs...)
}

func stunAttr(b []byte, typ uint16, value []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, typ)
	b = binary.BigEndian.AppendUint16(b, uint16(len(value)))
	b = append(b, value...)
	for len(value)%4 != 0 {
		b = append(b, 0)
		value = append(value, 0)
	}
	return b
}

func dnsQuery() []byte {
	b := randBytes(2)
	b = append(b, 0x01, 0x20, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01)
	tlds := []string{"com", "net", "org", "io"}
	name := strings.ToLower(randLetters(randx.Between(6, 14))) + "." + tlds[randx.Between(0, len(tlds)-1)]
	for _, label := range strings.Split(name, ".") {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0, 0x00, 0x01, 0x00, 0x01)
	b = append(b, 0, 0x00, 0x29, 0x04, 0xd0, 0, 0, 0, 0, 0x00, 0x00)
	return b
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func randLetters(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[randx.Between(0, len(letters)-1)]
	}
	return string(b)
}
