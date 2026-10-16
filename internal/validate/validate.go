package validate

import (
	"fmt"
	"math"
	"strings"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/cps"
)

type Level string

const (
	Warn Level = "warn"
	Fail Level = "fail"
)

type Issue struct {
	Level   Level  `json:"level"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	return fmt.Sprintf("%s %s: %s", i.Level, i.Field, i.Message)
}

type Issues []Issue

func (is Issues) Failed() bool {
	for _, i := range is {
		if i.Level == Fail {
			return true
		}
	}
	return false
}

func (is Issues) Err() error {
	var msgs []string
	for _, i := range is {
		if i.Level == Fail {
			msgs = append(msgs, i.Field+": "+i.Message)
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid parameters: %s", strings.Join(msgs, "; "))
}

func (is *Issues) add(level Level, field, format string, args ...any) {
	*is = append(*is, Issue{Level: level, Field: field, Message: fmt.Sprintf(format, args...)})
}

const (
	HeaderProtectionMinS = 12
	MaxJc                = 122
	IPUDPOverhead        = 28
	InitSize             = 148
	ResponseSize         = 92
	CookieSize           = 64
	TransportOverhead    = 32
	KernelRejectAfter    = 180
	DefaultRekeyAfter    = 120
	DefaultRekeyTimeout  = 5
	DefaultKeepalive     = 10
)

func Obfuscation(o awgconf.Obfuscation, pathMTU int) Issues {
	var is Issues
	headerProtection(&is, o)
	headers(&is, o)
	paddings(&is, o, pathMTU)
	junk(&is, o, pathMTU)
	packets(&is, o, pathMTU)
	timers(&is, o)
	return is
}

func headerProtection(is *Issues, o awgconf.Obfuscation) {
	if o.HeaderProtectionKey.IsZero() {
		is.add(Fail, "HeaderProtectionKey", "must be set")
		return
	}
	for n, s := range o.S() {
		if s < HeaderProtectionMinS {
			is.add(Fail, fmt.Sprintf("S%d", n+1), "must be >= %d with header protection", HeaderProtectionMinS)
		}
	}
}

func headers(is *Issues, o awgconf.Obfuscation) {
	h := o.H()
	for n, r := range h {
		if r.Lo > r.Hi {
			is.add(Fail, fmt.Sprintf("H%d", n+1), "min > max")
		}
		if r.Lo == 0 {
			is.add(Fail, fmt.Sprintf("H%d", n+1), "must not include 0")
		}
	}
	for a := 0; a < len(h); a++ {
		for b := a + 1; b < len(h); b++ {
			if h[a].Overlaps(h[b]) {
				is.add(Fail, fmt.Sprintf("H%d", b+1), "overlaps H%d", a+1)
			}
		}
	}
}

func paddings(is *Issues, o awgconf.Obfuscation, pathMTU int) {
	s := o.S()
	if o.RandomTrailers {
		if (s[0] != s[1] || s[0] != s[2] || s[0] != s[3]) && !singleHeaders(o) {
			is.add(Fail, "S1-S4", "must be equal with RandomTrailers unless H1-H4 are single values: otherwise data packets are taken for handshakes and dropped")
		}
	} else {
		sizes := map[string]int{
			"S1": int(o.S1) + InitSize,
			"S2": int(o.S2) + ResponseSize,
			"S3": int(o.S3) + CookieSize,
		}
		names := []string{"S1", "S2", "S3"}
		for a := 0; a < len(names); a++ {
			for b := a + 1; b < len(names); b++ {
				if sizes[names[a]] == sizes[names[b]] {
					is.add(Fail, names[b], "handshake size equals %s handshake size", names[a])
				}
			}
		}
	}
	limits := []struct {
		name string
		size int
	}{
		{"S1", int(o.S1) + InitSize},
		{"S2", int(o.S2) + ResponseSize},
		{"S3", int(o.S3) + CookieSize},
	}
	for _, l := range limits {
		if l.size+IPUDPOverhead > pathMTU {
			is.add(Fail, l.name, "packet of %d bytes exceeds path MTU %d", l.size+IPUDPOverhead, pathMTU)
		}
	}
	if int(o.S4)+TransportOverhead+IPUDPOverhead+576 > pathMTU {
		is.add(Fail, "S4", "leaves less than 576 bytes for payload at path MTU %d", pathMTU)
	}
	u16(is, "ContentPaddingAddition", o.ContentPaddingAddition)
}

func singleHeaders(o awgconf.Obfuscation) bool {
	for _, r := range o.H() {
		if r.Lo != r.Hi {
			return false
		}
	}
	return true
}

func junk(is *Issues, o awgconf.Obfuscation, pathMTU int) {
	if o.Jc == 0 {
		return
	}
	if o.Jc > MaxJc {
		is.add(Fail, "Jc", "must be <= %d: amneziawg-go clients send junk, I1-I5 and the init in one batch of 128 packets", MaxJc)
	}
	if o.Jmax == 0 {
		is.add(Fail, "Jmax", "must be > 0 when Jc > 0")
	}
	if o.Jmin > o.Jmax {
		is.add(Fail, "Jmin", "must be <= Jmax")
	}
	if int(o.Jmax)+IPUDPOverhead > pathMTU {
		is.add(Fail, "Jmax", "junk packet of %d bytes exceeds path MTU %d", int(o.Jmax)+IPUDPOverhead, pathMTU)
	}
}

func packets(is *Issues, o awgconf.Obfuscation, pathMTU int) {
	for n, s := range o.I {
		if s == "" {
			continue
		}
		field := fmt.Sprintf("I%d", n+1)
		if strings.Contains(s, "#") {
			is.add(Fail, field, "must not contain #")
		}
		p, err := cps.Parse(s)
		if err != nil {
			is.add(Fail, field, "%v", err)
			continue
		}
		if p.Size()+IPUDPOverhead > pathMTU {
			is.add(Fail, field, "packet of %d bytes exceeds path MTU %d", p.Size()+IPUDPOverhead, pathMTU)
		}
	}
}

func timers(is *Issues, o awgconf.Obfuscation) {
	named := []struct {
		name string
		r    awgconf.Range
	}{
		{"RekeyAfterTime", o.RekeyAfterTime},
		{"RekeyTimeout", o.RekeyTimeout},
		{"RejectAfterTime", o.RejectAfterTime},
		{"KeepaliveTimeout", o.KeepaliveTimeout},
		{"MaxHandshakeAttempts", o.MaxHandshakeAttempts},
	}
	for _, t := range named {
		u16(is, t.name, t.r)
		if t.r.Lo == 0 && t.r.Hi != 0 {
			is.add(Fail, t.name, "min must be > 0")
		}
	}
	rekey := orDefault(o.RekeyAfterTime, DefaultRekeyAfter)
	reject := orDefault(o.RejectAfterTime, KernelRejectAfter)
	keepalive := orDefault(o.KeepaliveTimeout, DefaultKeepalive)
	rekeyTimeout := orDefault(o.RekeyTimeout, DefaultRekeyTimeout)
	if rekey.Hi >= KernelRejectAfter {
		is.add(Fail, "RekeyAfterTime", "max must be < %d, data keys expire at %d s", KernelRejectAfter, KernelRejectAfter)
	}
	if reject.Lo <= rekey.Hi {
		is.add(Fail, "RejectAfterTime", "min must be > RekeyAfterTime max")
	}
	if reject.Hi > KernelRejectAfter {
		is.add(Warn, "RejectAfterTime", "values above %d have no effect in the kernel module", KernelRejectAfter)
	}
	if int(reject.Lo)-int(keepalive.Lo)-int(rekeyTimeout.Lo) <= 0 {
		is.add(Fail, "RejectAfterTime", "min must exceed KeepaliveTimeout min + RekeyTimeout min")
	}
}

func orDefault(r awgconf.Range, def uint32) awgconf.Range {
	if r.IsZero() {
		return awgconf.Single(def)
	}
	return r
}

func u16(is *Issues, field string, r awgconf.Range) {
	if r.Lo > r.Hi {
		is.add(Fail, field, "min > max")
	}
	if r.Hi > math.MaxUint16 {
		is.add(Fail, field, "must be <= %d", math.MaxUint16)
	}
}

func UniqueKeys(keys map[string]awgconf.Key) Issues {
	var is Issues
	seen := map[awgconf.Key]string{}
	for name, k := range keys {
		if k.IsZero() {
			continue
		}
		if other, ok := seen[k]; ok {
			is.add(Fail, name, "repeats key of %s", other)
			continue
		}
		seen[k] = name
	}
	return is
}

func ProfilePort(profile string, port int) Issues {
	var is Issues
	switch profile {
	case "quic":
		if port != 443 {
			is.add(Warn, "I1", "quic profile on port %d: browser fingerprint contradicts the port, use 443/udp", port)
		}
	case "dns":
		if port != 53 {
			is.add(Warn, "I1", "dns profile on port %d: use 53/udp", port)
		}
	}
	return is
}
