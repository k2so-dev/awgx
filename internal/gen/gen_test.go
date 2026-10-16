package gen

import (
	"testing"

	"github.com/k2so-dev/awgx/internal/randx"
	"github.com/k2so-dev/awgx/internal/validate"
)

func TestPresetsAlwaysValid(t *testing.T) {
	for _, p := range Presets {
		seen := map[string]bool{}
		for range 200 {
			o, err := Obfuscation(p)
			if err != nil {
				t.Fatalf("%s: %v", p.Name, err)
			}
			if is := validate.Obfuscation(o, PathMTU); is.Failed() {
				t.Fatalf("%s: %v", p.Name, is)
			}
			k := o.HeaderProtectionKey.String()
			if seen[k] {
				t.Fatalf("%s: repeated header protection key", p.Name)
			}
			seen[k] = true
		}
	}
}

func TestBetweenBounds(t *testing.T) {
	counts := map[int]int{}
	for range 6000 {
		v := randx.Between(1, 6)
		if v < 1 || v > 6 {
			t.Fatalf("out of range: %d", v)
		}
		counts[v]++
	}
	for v := 1; v <= 6; v++ {
		if counts[v] < 800 {
			t.Errorf("value %d drawn %d times, distribution looks biased", v, counts[v])
		}
	}
}

func TestPresetByName(t *testing.T) {
	if _, err := PresetByName("balanced"); err != nil {
		t.Fatal(err)
	}
	if _, err := PresetByName("nope"); err == nil {
		t.Fatal("want error for unknown preset")
	}
}

func TestToggles(t *testing.T) {
	on := true
	p, _ := PresetByName("balanced")
	o, err := Obfuscation(p.With(Toggles{RandomTrailers: &on, DisableCookies: &on}))
	if err != nil {
		t.Fatal(err)
	}
	if !o.RandomTrailers || !o.DisableCookies || o.S1 != o.S2 || o.S1 != o.S3 || o.S1 != o.S4 {
		t.Fatalf("toggles not applied: %+v", o)
	}
}
