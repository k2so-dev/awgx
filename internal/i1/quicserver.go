package i1

import (
	"crypto/aes"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/cryptobyte"
)

type ClientHelloInfo struct {
	SNI  string
	ALPN []string
	QUIC bool
}

func DecryptQUICInitial(packet []byte) (ClientHelloInfo, error) {
	var info ClientHelloInfo
	if len(packet) < 7 || packet[0]&0xf0 != 0xc0 {
		return info, errors.New("not a QUIC long header initial packet")
	}
	if binary.BigEndian.Uint32(packet[1:5]) != quicVersion1 {
		return info, errors.New("not QUIC v1")
	}
	pos := 5
	dcidLen := int(packet[pos])
	pos++
	if len(packet) < pos+dcidLen+1 {
		return info, errors.New("short packet")
	}
	dcid := packet[pos : pos+dcidLen]
	pos += dcidLen
	pos += 1 + int(packet[pos])
	tokenLen, n, err := readVarint(packet[pos:])
	if err != nil {
		return info, err
	}
	pos += n + int(tokenLen)
	length, n, err := readVarint(packet[pos:])
	if err != nil {
		return info, err
	}
	pos += n
	if len(packet) < pos+int(length) || pos+4+aes.BlockSize > len(packet) {
		return info, errors.New("packet length mismatch")
	}

	keys, err := clientInitialKeys(dcid)
	if err != nil {
		return info, err
	}
	buf := append([]byte(nil), packet[:pos+int(length)]...)
	mask := keys.mask(buf[pos+4 : pos+4+aes.BlockSize])
	buf[0] ^= mask[0] & 0x0f
	pnSize := int(buf[0]&0x03) + 1
	var pn uint64
	for i := 0; i < pnSize; i++ {
		buf[pos+i] ^= mask[1+i]
		pn = pn<<8 | uint64(buf[pos+i])
	}
	header := buf[:pos+pnSize]
	plain, err := keys.aead.Open(nil, keys.nonce(pn), buf[pos+pnSize:], header)
	if err != nil {
		return info, fmt.Errorf("decrypt: %w", err)
	}
	hello, err := cryptoData(plain)
	if err != nil {
		return info, err
	}
	return parseClientHello(hello)
}

func cryptoData(plain []byte) ([]byte, error) {
	var data []byte
	for len(plain) > 0 {
		switch plain[0] {
		case 0x00:
			plain = plain[1:]
		case 0x06:
			off, n, err := readVarint(plain[1:])
			if err != nil {
				return nil, err
			}
			l, m, err := readVarint(plain[1+n:])
			if err != nil {
				return nil, err
			}
			start := 1 + n + m
			if off != uint64(len(data)) || len(plain) < start+int(l) {
				return nil, errors.New("malformed CRYPTO frame")
			}
			data = append(data, plain[start:start+int(l)]...)
			plain = plain[start+int(l):]
		default:
			return nil, fmt.Errorf("unexpected frame type 0x%02x", plain[0])
		}
	}
	if len(data) == 0 {
		return nil, errors.New("no CRYPTO data")
	}
	return data, nil
}

func parseClientHello(b []byte) (ClientHelloInfo, error) {
	var info ClientHelloInfo
	s := cryptobyte.String(b)
	var msgType uint8
	var body cryptobyte.String
	if !s.ReadUint8(&msgType) || msgType != 1 || !s.ReadUint24LengthPrefixed(&body) {
		return info, errors.New("not a ClientHello")
	}
	var version uint16
	var random, sessionID, suites, compression, exts cryptobyte.String
	if !body.ReadUint16(&version) || !body.ReadBytes((*[]byte)(&random), 32) ||
		!body.ReadUint8LengthPrefixed(&sessionID) || !body.ReadUint16LengthPrefixed(&suites) ||
		!body.ReadUint8LengthPrefixed(&compression) || !body.ReadUint16LengthPrefixed(&exts) {
		return info, errors.New("malformed ClientHello")
	}
	for !exts.Empty() {
		var typ uint16
		var data cryptobyte.String
		if !exts.ReadUint16(&typ) || !exts.ReadUint16LengthPrefixed(&data) {
			return info, errors.New("malformed extension")
		}
		switch typ {
		case 0:
			var list cryptobyte.String
			var nameType uint8
			var name cryptobyte.String
			if !data.ReadUint16LengthPrefixed(&list) || !list.ReadUint8(&nameType) || !list.ReadUint16LengthPrefixed(&name) {
				return info, errors.New("malformed SNI")
			}
			info.SNI = string(name)
		case 16:
			var list cryptobyte.String
			if !data.ReadUint16LengthPrefixed(&list) {
				return info, errors.New("malformed ALPN")
			}
			for !list.Empty() {
				var proto cryptobyte.String
				if !list.ReadUint8LengthPrefixed(&proto) {
					return info, errors.New("malformed ALPN")
				}
				info.ALPN = append(info.ALPN, string(proto))
			}
		case 57:
			info.QUIC = true
		}
	}
	return info, nil
}
