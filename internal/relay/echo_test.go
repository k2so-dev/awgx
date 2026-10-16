package relay

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func TestEchoReply(t *testing.T) {
	msg := []byte{0, 0, 0, 0, 0, 7, 0, 3, 'p', 'i', 'n', 'g', '!'}
	from := netip.MustParseAddr("1.1.1.1")
	p := echoReply(from, echoReq{src: netip.MustParseAddr("10.0.0.2"), id: 42}, msg)
	if len(p) != 20+len(msg) || checksum(p[:20], 0) != 0 || checksum(p[20:], 0) != 0 {
		t.Fatalf("bad IPv4 checksums: %x", p)
	}
	if binary.BigEndian.Uint16(p[24:26]) != 42 || binary.BigEndian.Uint16(p[26:28]) != 3 || netip.AddrFrom4([4]byte(p[12:16])) != from {
		t.Fatalf("bad reply: %x", p)
	}
	from6, to6 := netip.MustParseAddr("2606:4700::1111"), netip.MustParseAddr("fd00::2")
	msg[0] = 129
	p = echoReply(from6, echoReq{src: to6, id: 42}, msg)
	var sum uint32
	for i := 8; i < 40; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(p[i:]))
	}
	if checksum(p[40:], sum+uint32(len(msg))+58) != 0 || netip.AddrFrom16([16]byte(p[24:40])) != to6 {
		t.Fatalf("bad IPv6 reply: %x", p)
	}
}
