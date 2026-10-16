package awg

import (
	"strings"
	"testing"
)

const dump = "SERVERPRIV=\tGwEtWodHIO3TO6ZedwRApQBcdwqdEYTbeEoHD3Folz4=\t51888\t4\t40\t70\t15\t15\t15\t15\t100000-100100\t200000-200100\t300000-300100\t400000-400100\t(null)\t(null)\t(null)\t(null)\t(null)\tUnvMwGWyEgNkyAJpuy0+kWvbZ8Kv1EIMWWSX1ixZdeU=\t0-32\t100-110\t0\t170-180\t0\t0\ton\toff\toff\n" +
	"k0Nz35yKWZ+r1qFsL/ipm3s+V2CXgFGCujhuVj5Orzc=\t(hidden)\t10.99.0.2:38815\t10.98.0.2/32\t1759400000\t566\t771\toff\n" +
	"GwEtWodHIO3TO6ZedwRApQBcdwqdEYTbeEoHD3Folz4=\t(none)\t(none)\t(none)\t0\t0\t0\t20-30\n"

func TestParseDump(t *testing.T) {
	d, err := ParseDump(dump)
	if err != nil {
		t.Fatal(err)
	}
	if d.ListenPort != 51888 || len(d.Peers) != 2 {
		t.Fatalf("unexpected device: %+v", d)
	}
	p := d.Peers[0]
	if p.Endpoint != "10.99.0.2:38815" || p.RxBytes != 566 || p.TxBytes != 771 || p.LastHandshake.Unix() != 1759400000 {
		t.Fatalf("unexpected peer: %+v", p)
	}
	if d.Peers[1].Endpoint != "" || !d.Peers[1].LastHandshake.IsZero() || d.Peers[1].AllowedIPs != nil {
		t.Fatalf("unexpected empty peer: %+v", d.Peers[1])
	}
}

func TestParseDumpOldTools(t *testing.T) {
	old := "priv\tpub\t51820\toff\n"
	if _, err := ParseDump(old); err == nil || !strings.Contains(err.Error(), "3.1") {
		t.Fatalf("want old tools error, got %v", err)
	}
}

func TestSupports31(t *testing.T) {
	for v, want := range map[string]bool{"3.1.20260812": true, "3.1.20260812-dirty": true, "3.0.20260805": false, "1.0.20260618": false, "4.0": true, "": false} {
		if Supports31(v) != want {
			t.Errorf("Supports31(%q) != %v", v, want)
		}
	}
}
