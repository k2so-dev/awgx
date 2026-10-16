package render

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/gen"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
)

func fixture(t *testing.T, profile string) (state.Instance, state.Peer) {
	t.Helper()
	preset, _ := gen.PresetByName("stealth")
	obf, err := gen.Obfuscation(preset)
	if err != nil {
		t.Fatal(err)
	}
	obf.I[0], err = i1.Generate(i1.Settings{Profile: profile, SNI: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	spriv, _ := awgconf.NewPrivateKey()
	ppriv, _ := awgconf.NewPrivateKey()
	psk, _ := awgconf.NewRandomKey()
	inst := state.Instance{
		Name: "awg0", ListenPort: 443, Endpoint: "203.0.113.7", MTU: 1380,
		Subnet4: netip.MustParsePrefix("10.66.1.0/24"), Subnet6: netip.MustParsePrefix("fd00:aa::/64"),
		DNS:         []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")},
		Obfuscation: obf, I1: i1.Settings{Profile: profile}, PrivateKey: spriv,
	}
	p := state.Peer{
		Instance: "awg0", Name: "alice", PublicKey: ppriv.Public(), PrivateKey: ppriv, PresharedKey: psk,
		Address4: netip.MustParseAddr("10.66.1.2"), Address6: netip.MustParseAddr("fd00:aa::2"),
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		Keepalive:  awgconf.Range{Lo: 20, Hi: 30}, Enabled: true,
	}
	return inst, p
}

func TestServerAndClientAgree(t *testing.T) {
	inst, p := fixture(t, i1.QUIC)
	disabled := p
	disabled.Name, disabled.Enabled = "bob", false
	server, err := awgconf.Parse(strings.NewReader(Server(inst, []state.Peer{p, disabled}, time.Now()).String()))
	if err != nil {
		t.Fatal(err)
	}
	client, err := awgconf.Parse(strings.NewReader(Client(inst, p).String()))
	if err != nil {
		t.Fatal(err)
	}
	if !server.Interface.SameServerSide(client.Interface.Obfuscation) {
		t.Fatal("server-side parameters differ between server and client configs")
	}
	if len(server.Peers) != 1 || server.Peers[0].PublicKey != p.PublicKey {
		t.Fatalf("server must list only enabled peers: %+v", server.Peers)
	}
	if server.Interface.I[0] != "" {
		t.Fatal("server config must not carry I1")
	}
	if client.Interface.I[0] != inst.Obfuscation.I[0] {
		t.Fatal("client config lost I1")
	}
	if client.Peers[0].PublicKey != inst.PrivateKey.Public() || client.Peers[0].Endpoint != "203.0.113.7:443" {
		t.Fatalf("bad client peer: %+v", client.Peers[0])
	}
	if is := validate.Obfuscation(client.Interface.Obfuscation, gen.PathMTU); is.Failed() {
		t.Fatal(is)
	}
}

func TestPeerI1Override(t *testing.T) {
	inst, p := fixture(t, i1.None)
	p.I1 = &i1.Settings{Profile: i1.Noise}
	p.I1CPS = "<r 100>"
	if got := Client(inst, p).Interface.I[0]; got != "<r 100>" {
		t.Fatalf("I1 = %q", got)
	}
}

func decodeVPN(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	if binary.BigEndian.Uint32(payload[:4]) == 0 {
		t.Fatal("missing qCompress length")
	}
	r, err := zlib.NewReader(bytes.NewReader(payload[4:]))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	if int(binary.BigEndian.Uint32(payload[:4])) != len(data) {
		t.Fatal("qCompress length mismatch")
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestVPNURL(t *testing.T) {
	inst, p := fixture(t, i1.STUN)
	url, err := VPNURL(inst, p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(url, "vpn://"))
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeVPN(t, raw)
	c := doc["containers"].([]any)[0].(map[string]any)
	if c["container"] != "amnezia-awg2" || doc["defaultContainer"] != "amnezia-awg2" {
		t.Fatalf("bad container: %v", c["container"])
	}
	awg := c["awg"].(map[string]any)
	if awg["protocol_version"] != "3.1" || awg["HeaderProtectionKey"] != inst.Obfuscation.HeaderProtectionKey.String() {
		t.Fatal("bad awg object")
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(awg["last_config"].(string)), &last); err != nil {
		t.Fatal(err)
	}
	if last["port"].(float64) != 443 || last["RandomTrailers"] != "on" || last["I1"] != inst.Obfuscation.I[0] {
		t.Fatalf("bad last_config: %v", last)
	}
}

func TestQRChunks(t *testing.T) {
	inst, p := fixture(t, i1.QUIC)
	chunks, err := QRPayloads(inst, p)
	if err != nil {
		t.Fatal(err)
	}
	var joined []byte
	for i, c := range chunks {
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) == 1 {
			joined = b
			break
		}
		if int16(binary.BigEndian.Uint16(b[:2])) != qrChunkMagic || int(b[2]) != len(chunks) || int(b[3]) != i {
			t.Fatalf("bad chunk header %d", i)
		}
		n := binary.BigEndian.Uint32(b[4:8])
		joined = append(joined, b[8:8+n]...)
	}
	decodeVPN(t, joined)
}

func TestIPv6Off(t *testing.T) {
	inst, p := fixture(t, "none")
	inst.Subnet6 = netip.Prefix{}
	p.Address6 = netip.Addr{}
	srv := Server(inst, []state.Peer{p}, time.Now()).String()
	cli := Client(inst, p).String()
	if strings.Contains(srv, "fd00") || strings.Contains(cli, "fd00") || strings.Contains(srv, "invalid") || strings.Contains(cli, "invalid") {
		t.Fatalf("IPv6 leaked into configs:\n%s\n%s", srv, cli)
	}
	if !strings.Contains(cli, "::/0") {
		t.Fatalf("client must keep ::/0 so IPv6 does not bypass the tunnel:\n%s", cli)
	}
}
