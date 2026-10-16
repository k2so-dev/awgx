package i1

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/k2so-dev/awgx/internal/cps"
)

func TestGenerate(t *testing.T) {
	tests := []struct {
		s     Settings
		empty bool
		max   int
	}{
		{Settings{Profile: None}, true, 0},
		{Settings{Profile: Noise}, false, 64},
		{Settings{Profile: QUIC, SNI: "example.org"}, false, QUICInitialSize},
		{Settings{Profile: QUIC, SNI: "example.org", Fingerprint: "firefox"}, false, QUICInitialSize},
		{Settings{Profile: STUN}, false, 200},
		{Settings{Profile: DNS}, false, 100},
		{Settings{Profile: Custom, Custom: "<b 0x01020304><r 10>"}, false, 14},
	}
	for _, tt := range tests {
		got, err := Generate(tt.s)
		if err != nil {
			t.Fatalf("%s: %v", tt.s.Profile, err)
		}
		if (got == "") != tt.empty {
			t.Fatalf("%s: got %q", tt.s.Profile, got)
		}
		p, err := cps.Parse(got)
		if err != nil {
			t.Fatalf("%s: generated invalid CPS: %v", tt.s.Profile, err)
		}
		if p.Size() > tt.max {
			t.Fatalf("%s: size %d > %d", tt.s.Profile, p.Size(), tt.max)
		}
	}
}

func TestGenerateErrors(t *testing.T) {
	bad := []Settings{
		{Profile: "mimic"},
		{Profile: QUIC},
		{Profile: Custom},
		{Profile: Custom, Custom: "<x 1>"},
		{Profile: Noise, NoiseMin: 10, NoiseMax: 5},
	}
	for _, s := range bad {
		if _, err := Generate(s); err == nil {
			t.Errorf("%+v: want error", s)
		}
	}
}

func TestSTUNFingerprint(t *testing.T) {
	m := stunBindingRequest()
	if binary.BigEndian.Uint32(m[4:8]) != 0x2112a442 {
		t.Fatal("bad magic cookie")
	}
	if int(binary.BigEndian.Uint16(m[2:4])) != len(m)-20 {
		t.Fatal("bad length")
	}
	fp := m[len(m)-8:]
	if binary.BigEndian.Uint16(fp) != 0x8028 {
		t.Fatal("fingerprint is not the last attribute")
	}
	want := crc32.ChecksumIEEE(m[:len(m)-8]) ^ 0x5354554e
	if binary.BigEndian.Uint32(fp[4:]) != want {
		t.Fatal("bad fingerprint")
	}
}

func TestOverKeepsTuning(t *testing.T) {
	old := Settings{Profile: QUIC, Fingerprint: "firefox", SNI: "a.example", NoiseMin: 10, NoiseMax: 20}
	got := Settings{Profile: QUIC, SNI: "b.example"}.Over(old)
	if got.Fingerprint != "firefox" || got.NoiseMin != 10 || got.NoiseMax != 20 || got.SNI != "b.example" {
		t.Fatalf("got %+v", got)
	}
	if got := (Settings{Profile: QUIC, Fingerprint: "chrome"}).Over(old); got.Fingerprint != "chrome" {
		t.Fatalf("explicit fingerprint replaced: %+v", got)
	}
}
