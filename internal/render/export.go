package render

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/state"
)

const (
	amneziaContainer = "amnezia-awg2"
	qrChunkSize      = 850
	qrChunkMagic     = 1984
)

func awgKeys(o awgconf.Obfuscation) map[string]string {
	m := map[string]string{
		"Jc": u(o.Jc), "Jmin": u(o.Jmin), "Jmax": u(o.Jmax),
		"S1": u(o.S1), "S2": u(o.S2), "S3": u(o.S3), "S4": u(o.S4),
		"H1": o.H1.String(), "H2": o.H2.String(), "H3": o.H3.String(), "H4": o.H4.String(),
		"HeaderProtectionKey": o.HeaderProtectionKey.String(),
		"RandomTrailers":      fmtx.OnOff(o.RandomTrailers),
		"DisableCookies":      fmtx.OnOff(o.DisableCookies),
	}
	for n, v := range o.I {
		m[fmt.Sprintf("I%d", n+1)] = v
	}
	ranges := map[string]awgconf.Range{
		"ContentPaddingAddition": o.ContentPaddingAddition,
		"RekeyAfterTime":         o.RekeyAfterTime,
		"RekeyTimeout":           o.RekeyTimeout,
		"RejectAfterTime":        o.RejectAfterTime,
		"KeepaliveTimeout":       o.KeepaliveTimeout,
		"MaxHandshakeAttempts":   o.MaxHandshakeAttempts,
	}
	for k, r := range ranges {
		if !r.IsZero() {
			m[k] = r.String()
		}
	}
	return m
}

func AmneziaJSON(inst state.Instance, p state.Peer) ([]byte, error) {
	client := Client(inst, p)
	o := client.Interface.Obfuscation
	allowed := make([]string, len(p.AllowedIPs))
	for i, a := range p.AllowedIPs {
		allowed[i] = a.String()
	}
	last := map[string]any{
		"config":                client.String(),
		"hostName":              inst.Endpoint,
		"port":                  inst.ListenPort,
		"client_ip":             p.Address4.String(),
		"client_priv_key":       p.PrivateKey.String(),
		"client_pub_key":        p.PublicKey.String(),
		"server_pub_key":        inst.PrivateKey.Public().String(),
		"allowed_ips":           allowed,
		"persistent_keep_alive": p.Keepalive.String(),
		"mtu":                   strconv.Itoa(inst.MTU),
	}
	if !p.PresharedKey.IsZero() {
		last["psk_key"] = p.PresharedKey.String()
	}
	keys := awgKeys(o)
	for k, v := range keys {
		last[k] = v
	}
	lastJSON, err := json.Marshal(last)
	if err != nil {
		return nil, err
	}
	server := map[string]any{
		"port":             strconv.Itoa(inst.ListenPort),
		"transport_proto":  "udp",
		"protocol_version": "3.1",
		"last_config":      string(lastJSON),
	}
	for k, v := range keys {
		server[k] = v
	}
	dns := []string{"", ""}
	for i := 0; i < len(inst.DNS) && i < 2; i++ {
		dns[i] = inst.DNS[i].String()
	}
	doc := map[string]any{
		"format_version":   1,
		"hostName":         inst.Endpoint,
		"description":      "awgx " + p.Name,
		"dns1":             dns[0],
		"dns2":             dns[1],
		"defaultContainer": amneziaContainer,
		"containers":       []any{map[string]any{"container": amneziaContainer, "awg": server}},
	}
	return json.Marshal(doc)
}

func qCompress(data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	w, _ := zlib.NewWriterLevel(&b, 8)
	w.Write(data)
	w.Close()
	return b.Bytes()
}

func VPNURL(inst state.Instance, p state.Peer) (string, error) {
	data, err := AmneziaJSON(inst, p)
	if err != nil {
		return "", err
	}
	return "vpn://" + base64.RawURLEncoding.EncodeToString(qCompress(data)), nil
}

func QRPayloads(inst state.Instance, p state.Peer) ([]string, error) {
	data, err := AmneziaJSON(inst, p)
	if err != nil {
		return nil, err
	}
	z := qCompress(data)
	if len(z) <= qrChunkSize*2 {
		return []string{base64.RawURLEncoding.EncodeToString(z)}, nil
	}
	var out []string
	count := (len(z) + qrChunkSize - 1) / qrChunkSize
	for i := 0; i < count; i++ {
		chunk := z[i*qrChunkSize : min((i+1)*qrChunkSize, len(z))]
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, int16(qrChunkMagic))
		b.WriteByte(byte(count))
		b.WriteByte(byte(i))
		binary.Write(&b, binary.BigEndian, uint32(len(chunk)))
		b.Write(chunk)
		out = append(out, base64.RawURLEncoding.EncodeToString(b.Bytes()))
	}
	return out, nil
}

type PeerExport struct {
	Name       string            `json:"name"`
	Address4   string            `json:"address4"`
	Address6   string            `json:"address6"`
	Endpoint   string            `json:"endpoint"`
	PublicKey  string            `json:"public_key"`
	I1Profile  string            `json:"i1_profile"`
	Config     string            `json:"config"`
	VPN        string            `json:"vpn"`
	Parameters map[string]string `json:"parameters"`
}

func Export(inst state.Instance, p state.Peer) (PeerExport, error) {
	vpn, err := VPNURL(inst, p)
	if err != nil {
		return PeerExport{}, err
	}
	client := Client(inst, p)
	return PeerExport{
		Name:       p.Name,
		Address4:   p.Address4.String(),
		Address6:   state.OrEmpty(p.Address6),
		Endpoint:   client.Peers[0].Endpoint,
		PublicKey:  p.PublicKey.String(),
		I1Profile:  p.I1Profile(inst),
		Config:     client.String(),
		VPN:        vpn,
		Parameters: awgKeys(client.Interface.Obfuscation),
	}, nil
}

func u(v uint16) string {
	return strconv.Itoa(int(v))
}
