package gen

import (
	"fmt"
	"math"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/randx"
	"github.com/k2so-dev/awgx/internal/validate"
)

const (
	PathMTU     = 1280
	hMin        = 5
	maxAttempts = 100
)

func Obfuscation(p Preset) (awgconf.Obfuscation, error) {
	for range maxAttempts {
		o, err := draw(p)
		if err != nil {
			return o, err
		}
		if !validate.Obfuscation(o, PathMTU).Failed() {
			return o, nil
		}
	}
	return awgconf.Obfuscation{}, fmt.Errorf("preset %s: no valid parameters after %d attempts", p.Name, maxAttempts)
}

func draw(p Preset) (awgconf.Obfuscation, error) {
	hpk, err := awgconf.NewRandomKey()
	if err != nil {
		return awgconf.Obfuscation{}, err
	}
	o := awgconf.Obfuscation{
		HeaderProtectionKey:    hpk,
		RandomTrailers:         p.RandomTrailers,
		DisableCookies:         p.DisableCookies,
		ContentPaddingAddition: p.CPA,
		RekeyAfterTime:         p.RekeyAfterTime,
		RejectAfterTime:        p.RejectAfter,
	}
	o.Jc = uint16(randx.Between(p.Jc.lo, p.Jc.hi))
	o.Jmin = uint16(randx.Between(p.Jmin.lo, p.Jmin.hi))
	o.Jmax = o.Jmin + uint16(randx.Between(p.JmaxExtra.lo, p.JmaxExtra.hi))
	s := [4]uint16{}
	for n := range s {
		s[n] = uint16(randx.Between(p.S.lo, p.S.hi))
		if p.EqualS {
			s[n] = s[0]
		}
	}
	o.S1, o.S2, o.S3, o.S4 = s[0], s[1], s[2], s[3]
	h := Headers(p.HWidth)
	o.H1, o.H2, o.H3, o.H4 = h[0], h[1], h[2], h[3]
	return o, nil
}

func Headers(width span) [4]awgconf.Range {
	quarter := (math.MaxInt32 - hMin) / 4
	var h [4]awgconf.Range
	for n := range h {
		w := randx.Between(width.lo, width.hi) - 1
		start := hMin + n*quarter
		lo := randx.Between(start, start+quarter-w-1)
		h[n] = awgconf.Range{Lo: uint32(lo), Hi: uint32(lo + w)}
	}
	s := h[:]
	shuffle(s)
	return h
}

func ListenPort() int {
	return randx.Between(20000, 60999)
}
