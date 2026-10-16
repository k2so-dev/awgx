package upgrade

import (
	"testing"

	"github.com/jedisct1/go-minisign"
)

func TestNewer(t *testing.T) {
	tests := []struct {
		cur, tag string
		want     bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"0.2.0", "v0.2.0", false},
		{"v0.2.0", "v0.2.0-beta.1", false},
		{"dev", "v0.1.0", true},
		{"1d20818-dirty", "v0.1.0", true},
		{"v1.0.0-3-gabc1234", "v1.0.0", false},
		{"v1.0.0-3-gabc1234-dirty", "v1.0.1", true},
		{"v1.0.0-beta.1-2-gabc1234", "v1.0.0", true},
		{"v0.1.0-alpha.1", "v0.1.0-alpha.2", true},
		{"v0.1.0-alpha.2", "v0.1.0-beta.1", true},
	}
	for _, tt := range tests {
		if got := Newer(tt.cur, tt.tag); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v", tt.cur, tt.tag, got)
		}
	}
}

func TestChannel(t *testing.T) {
	for cur, want := range map[string]string{
		"v0.1.0-alpha.1":             "beta",
		"v0.1.0-rc.1-4-gabc1234":     "beta",
		"v0.1.0":                     "stable",
		"v0.1.0-3-gabc1234-dirty":    "stable",
		"a9bd93a":                    "stable",
		"v0.1.0-beta.1-2-gabc-dirty": "beta",
	} {
		if got := Channel(cur); got != want {
			t.Errorf("Channel(%q) = %q, want %q", cur, got, want)
		}
	}
}

func TestVerify(t *testing.T) {
	pk, err := minisign.NewPublicKey("RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := minisign.DecodeSignature("untrusted comment: signature from minisign secret key\nRWQf6LRCGA9i59SLOFxz6NxvASXDJeRtuZykwQepbDEGt87ig1BNpWaVWuNrm73YiIiJbq71Wi+dP9eKL8OC351vwIasSSbXxwA=\ntrusted comment: timestamp:1555779966\tfile:test\nQtKMXWyYcwdpZAlPF7tE2ENJkRd1ujvKjlj1m9RtHTBnZPa5WKU5uWRs5GoP5M/VqE81QFuMKI5k/SfNQUaOAA==")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := pk.Verify([]byte("test"), sig); !ok {
		t.Fatal("known good signature rejected")
	}
	if ok, _ := pk.Verify([]byte("tampered"), sig); ok {
		t.Fatal("tampered data accepted")
	}
}
