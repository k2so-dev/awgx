package i1

import (
	"encoding/hex"
	"slices"
	"testing"
)

func TestQUICInitialSelfCheck(t *testing.T) {
	for _, fp := range Fingerprints() {
		t.Run(fp, func(t *testing.T) {
			pkt, err := QUICInitial(QUICParams{Fingerprint: fp, SNI: "example.com"})
			if err != nil {
				t.Fatal(err)
			}
			if len(pkt) != QUICInitialSize {
				t.Fatalf("size = %d, want %d", len(pkt), QUICInitialSize)
			}
			info, err := DecryptQUICInitial(pkt)
			if err != nil {
				t.Fatal(err)
			}
			if info.SNI != "example.com" || !slices.Equal(info.ALPN, []string{"h3"}) || !info.QUIC {
				t.Fatalf("unexpected hello: %+v", info)
			}
		})
	}
}

func TestClientInitialKeysRFC9001(t *testing.T) {
	dcid, _ := hex.DecodeString("8394c8f03e515708")
	k, err := clientInitialKeys(dcid)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(k.iv); got != "fa044b2f42a3fd3b46fb255c" {
		t.Fatalf("iv = %s", got)
	}
	sample, _ := hex.DecodeString("d1b1c98dd7689fb8ec11d242b123dc9b")
	if got := hex.EncodeToString(k.mask(sample)[:5]); got != "437b9aec36" {
		t.Fatalf("mask = %s", got)
	}
}
