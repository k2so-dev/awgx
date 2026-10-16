package gen

import (
	"fmt"
	"slices"

	"github.com/k2so-dev/awgx/internal/awgconf"
)

const Custom = "custom"

type span struct{ lo, hi int }

type Preset struct {
	Name           string
	Jc             span
	Jmin           span
	JmaxExtra      span
	S              span
	EqualS         bool
	RandomTrailers bool
	DisableCookies bool
	HWidth         span
	CPA            awgconf.Range
	RekeyAfterTime awgconf.Range
	RejectAfter    awgconf.Range
}

var Presets = []Preset{
	{
		Name:      "balanced",
		Jc:        span{4, 6},
		Jmin:      span{10, 50},
		JmaxExtra: span{30, 100},
		S:         span{15, 44},
		HWidth:    span{1000, 100000},
	},
	{
		Name:           "stealth",
		Jc:             span{4, 8},
		Jmin:           span{40, 100},
		JmaxExtra:      span{50, 200},
		S:              span{24, 44},
		EqualS:         true,
		RandomTrailers: true,
		DisableCookies: true,
		HWidth:         span{100000, 10000000},
		CPA:            awgconf.Range{Lo: 16, Hi: 64},
		RekeyAfterTime: awgconf.Range{Lo: 100, Hi: 130},
		RejectAfter:    awgconf.Range{Lo: 170, Hi: 180},
	},
	{
		Name:      "speed",
		Jc:        span{2, 3},
		Jmin:      span{10, 20},
		JmaxExtra: span{20, 40},
		S:         span{12, 16},
		HWidth:    span{1, 1000},
	},
}

func PresetByName(name string) (Preset, error) {
	i := slices.IndexFunc(Presets, func(p Preset) bool { return p.Name == name })
	if i < 0 {
		return Preset{}, fmt.Errorf("unknown preset %q", name)
	}
	return Presets[i], nil
}

func PresetNames() []string {
	names := make([]string, len(Presets))
	for i, p := range Presets {
		names[i] = p.Name
	}
	return names
}

type Toggles struct {
	RandomTrailers *bool
	DisableCookies *bool
}

func (p Preset) With(t Toggles) Preset {
	if t.RandomTrailers != nil {
		p.RandomTrailers = *t.RandomTrailers
		p.EqualS = p.EqualS || p.RandomTrailers
	}
	if t.DisableCookies != nil {
		p.DisableCookies = *t.DisableCookies
	}
	return p
}
