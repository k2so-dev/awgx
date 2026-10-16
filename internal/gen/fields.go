package gen

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/awgconf"
)

type Fields struct {
	Jc       string `json:"jc"`
	Jmin     string `json:"jmin"`
	Jmax     string `json:"jmax"`
	S1       string `json:"s1"`
	S2       string `json:"s2"`
	S3       string `json:"s3"`
	S4       string `json:"s4"`
	H1       string `json:"h1"`
	H2       string `json:"h2"`
	H3       string `json:"h3"`
	H4       string `json:"h4"`
	I2       string `json:"i2"`
	I3       string `json:"i3"`
	I4       string `json:"i4"`
	I5       string `json:"i5"`
	Trailers bool   `json:"trailers"`
	NoCookie bool   `json:"no_cookie"`
}

func FieldsOf(o awgconf.Obfuscation) Fields {
	u := func(v uint16) string { return strconv.Itoa(int(v)) }
	return Fields{
		Jc: u(o.Jc), Jmin: u(o.Jmin), Jmax: u(o.Jmax),
		S1: u(o.S1), S2: u(o.S2), S3: u(o.S3), S4: u(o.S4),
		H1: o.H1.String(), H2: o.H2.String(), H3: o.H3.String(), H4: o.H4.String(),
		I2: o.I[1], I3: o.I[2], I4: o.I[3], I5: o.I[4],
		Trailers: o.RandomTrailers, NoCookie: o.DisableCookies,
	}
}

func (f Fields) Apply(o awgconf.Obfuscation) (awgconf.Obfuscation, error) {
	nums := []struct {
		name string
		s    string
		dst  *uint16
	}{
		{"Jc", f.Jc, &o.Jc}, {"Jmin", f.Jmin, &o.Jmin}, {"Jmax", f.Jmax, &o.Jmax},
		{"S1", f.S1, &o.S1}, {"S2", f.S2, &o.S2}, {"S3", f.S3, &o.S3}, {"S4", f.S4, &o.S4},
	}
	for _, n := range nums {
		v, err := strconv.ParseUint(strings.TrimSpace(n.s), 10, 16)
		if err != nil {
			return o, fmt.Errorf("%s: not a number", n.name)
		}
		*n.dst = uint16(v)
	}
	for i, s := range []string{f.H1, f.H2, f.H3, f.H4} {
		r, err := awgconf.ParseRange(s, math.MaxUint32)
		if err != nil {
			return o, fmt.Errorf("H%d: %w", i+1, err)
		}
		*[]*awgconf.Range{&o.H1, &o.H2, &o.H3, &o.H4}[i] = r
	}
	for i, s := range []string{f.I2, f.I3, f.I4, f.I5} {
		o.I[i+1] = strings.TrimSpace(s)
	}
	o.RandomTrailers, o.DisableCookies = f.Trailers, f.NoCookie
	return o, nil
}
