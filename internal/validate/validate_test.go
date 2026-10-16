package validate

import (
	"testing"

	"github.com/k2so-dev/awgx/internal/awgconf"
)

func base() awgconf.Obfuscation {
	return awgconf.Obfuscation{
		Jc: 4, Jmin: 40, Jmax: 70,
		S1: 15, S2: 20, S3: 25, S4: 30,
		H1:                  awgconf.Range{Lo: 100, Hi: 200},
		H2:                  awgconf.Range{Lo: 300, Hi: 400},
		H3:                  awgconf.Range{Lo: 500, Hi: 600},
		H4:                  awgconf.Range{Lo: 700, Hi: 800},
		HeaderProtectionKey: awgconf.Key{1},
	}
}

func TestObfuscation(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(o *awgconf.Obfuscation)
		field string
		level Level
	}{
		{"valid", func(o *awgconf.Obfuscation) {}, "", ""},
		{"hp key missing", func(o *awgconf.Obfuscation) { o.HeaderProtectionKey = awgconf.Key{} }, "HeaderProtectionKey", Fail},
		{"s1 below 12", func(o *awgconf.Obfuscation) { o.S1 = 11 }, "S1", Fail},
		{"s4 below 12", func(o *awgconf.Obfuscation) { o.S4 = 0 }, "S4", Fail},
		{"s 12 allowed", func(o *awgconf.Obfuscation) { o.S1, o.S2, o.S3, o.S4 = 12, 13, 14, 12 }, "", ""},
		{"h overlap", func(o *awgconf.Obfuscation) { o.H2 = awgconf.Range{Lo: 150, Hi: 160} }, "H2", Fail},
		{"h touching", func(o *awgconf.Obfuscation) { o.H2 = awgconf.Range{Lo: 200, Hi: 250} }, "H2", Fail},
		{"h zero", func(o *awgconf.Obfuscation) { o.H1 = awgconf.Range{Lo: 0, Hi: 10} }, "H1", Fail},
		{"h inverted", func(o *awgconf.Obfuscation) { o.H4 = awgconf.Range{Lo: 900, Hi: 850} }, "H4", Fail},
		{"h defaults allowed", func(o *awgconf.Obfuscation) {
			o.H1, o.H2, o.H3, o.H4 = awgconf.Single(1), awgconf.Single(2), awgconf.Single(3), awgconf.Single(4)
		}, "", ""},
		{"trailers need equal s", func(o *awgconf.Obfuscation) { o.RandomTrailers = true }, "S1-S4", Fail},
		{"trailers equal s", func(o *awgconf.Obfuscation) {
			o.RandomTrailers = true
			o.S1, o.S2, o.S3, o.S4 = 20, 20, 20, 20
		}, "", ""},
		{"trailers single h", func(o *awgconf.Obfuscation) {
			o.RandomTrailers = true
			o.H1, o.H2, o.H3, o.H4 = awgconf.Single(1), awgconf.Single(2), awgconf.Single(3), awgconf.Single(4)
		}, "", ""},
		{"init equals response size", func(o *awgconf.Obfuscation) { o.S1, o.S2 = 15, 71 }, "S2", Fail},
		{"response equals cookie size", func(o *awgconf.Obfuscation) { o.S2, o.S3 = 20, 48 }, "S3", Fail},
		{"s1 exceeds mtu", func(o *awgconf.Obfuscation) { o.S1 = 1200 }, "S1", Fail},
		{"s4 too big", func(o *awgconf.Obfuscation) { o.S4 = 900 }, "S4", Fail},
		{"jmin above jmax", func(o *awgconf.Obfuscation) { o.Jmin = 80 }, "Jmin", Fail},
		{"jmax zero", func(o *awgconf.Obfuscation) { o.Jmin, o.Jmax = 0, 0 }, "Jmax", Fail},
		{"jmax exceeds mtu", func(o *awgconf.Obfuscation) { o.Jmax = 1300 }, "Jmax", Fail},
		{"jc too big", func(o *awgconf.Obfuscation) { o.Jc = 123 }, "Jc", Fail},
		{"no junk ok", func(o *awgconf.Obfuscation) { o.Jc, o.Jmin, o.Jmax = 0, 0, 0 }, "", ""},
		{"i1 bad syntax", func(o *awgconf.Obfuscation) { o.I[0] = "<r -5>" }, "I1", Fail},
		{"i2 too big", func(o *awgconf.Obfuscation) { o.I[1] = "<r 1300>" }, "I2", Fail},
		{"i1 hash", func(o *awgconf.Obfuscation) { o.I[0] = "<r 5>#" }, "I1", Fail},
		{"i1 ok", func(o *awgconf.Obfuscation) { o.I[0] = "<r 100><t>" }, "", ""},
		{"cpa above u16", func(o *awgconf.Obfuscation) { o.ContentPaddingAddition = awgconf.Range{Lo: 0, Hi: 70000} }, "ContentPaddingAddition", Fail},
		{"timer min zero", func(o *awgconf.Obfuscation) { o.RekeyTimeout = awgconf.Range{Lo: 0, Hi: 5} }, "RekeyTimeout", Fail},
		{"rekey at kernel expiry", func(o *awgconf.Obfuscation) { o.RekeyAfterTime = awgconf.Range{Lo: 100, Hi: 180} }, "RekeyAfterTime", Fail},
		{"reject not above rekey", func(o *awgconf.Obfuscation) {
			o.RekeyAfterTime = awgconf.Range{Lo: 100, Hi: 150}
			o.RejectAfterTime = awgconf.Range{Lo: 150, Hi: 170}
		}, "RejectAfterTime", Fail},
		{"reject above kernel", func(o *awgconf.Obfuscation) { o.RejectAfterTime = awgconf.Range{Lo: 170, Hi: 200} }, "RejectAfterTime", Warn},
		{"reject below keepalive", func(o *awgconf.Obfuscation) {
			o.RekeyAfterTime = awgconf.Range{Lo: 5, Hi: 6}
			o.RejectAfterTime = awgconf.Single(10)
			o.KeepaliveTimeout = awgconf.Single(8)
		}, "RejectAfterTime", Fail},
		{"timers ok", func(o *awgconf.Obfuscation) {
			o.RekeyAfterTime = awgconf.Range{Lo: 100, Hi: 130}
			o.RejectAfterTime = awgconf.Range{Lo: 170, Hi: 180}
		}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base()
			tt.edit(&o)
			is := Obfuscation(o, 1280)
			if tt.field == "" {
				if len(is) != 0 {
					t.Fatalf("unexpected issues: %v", is)
				}
				return
			}
			for _, i := range is {
				if i.Field == tt.field && i.Level == tt.level {
					return
				}
			}
			t.Fatalf("want %s on %s, got %v", tt.level, tt.field, is)
		})
	}
}

func TestUniqueKeys(t *testing.T) {
	if is := UniqueKeys(map[string]awgconf.Key{"a": {1}, "b": {2}, "c": {}}); len(is) != 0 {
		t.Fatalf("unexpected issues: %v", is)
	}
	if is := UniqueKeys(map[string]awgconf.Key{"a": {1}, "b": {1}}); !is.Failed() {
		t.Fatal("want failure on repeated key")
	}
}

func TestProfilePort(t *testing.T) {
	tests := []struct {
		profile string
		port    int
		warn    bool
	}{
		{"quic", 443, false},
		{"quic", 51820, true},
		{"dns", 53, false},
		{"dns", 5353, true},
		{"stun", 3478, false},
		{"none", 1, false},
	}
	for _, tt := range tests {
		if got := len(ProfilePort(tt.profile, tt.port)) > 0; got != tt.warn {
			t.Errorf("ProfilePort(%s, %d) warn = %v, want %v", tt.profile, tt.port, got, tt.warn)
		}
	}
}
