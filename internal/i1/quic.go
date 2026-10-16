package i1

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	QUICInitialSize = 1200
	quicVersion1    = 1
	aeadTagSize     = 16
	pnLen           = 2
)

var initialSalt = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
}

type initialKeys struct {
	aead cipher.AEAD
	iv   []byte
	hp   cipher.Block
}

func clientInitialKeys(dcid []byte) (initialKeys, error) {
	initial, err := hkdf.Extract(sha256.New, dcid, initialSalt)
	if err != nil {
		return initialKeys{}, err
	}
	secret := expandLabel(initial, "client in", 32)
	block, err := aes.NewCipher(expandLabel(secret, "quic key", 16))
	if err != nil {
		return initialKeys{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return initialKeys{}, err
	}
	hp, err := aes.NewCipher(expandLabel(secret, "quic hp", 16))
	if err != nil {
		return initialKeys{}, err
	}
	return initialKeys{aead: aead, iv: expandLabel(secret, "quic iv", 12), hp: hp}, nil
}

func expandLabel(secret []byte, label string, length int) []byte {
	full := "tls13 " + label
	info := make([]byte, 0, 4+len(full))
	info = binary.BigEndian.AppendUint16(info, uint16(length))
	info = append(info, byte(len(full)))
	info = append(info, full...)
	info = append(info, 0)
	out, err := hkdf.Expand(sha256.New, secret, string(info), length)
	if err != nil {
		panic(err)
	}
	return out
}

func (k initialKeys) nonce(pn uint64) []byte {
	n := make([]byte, len(k.iv))
	copy(n, k.iv)
	for i := 0; i < 8; i++ {
		n[len(n)-1-i] ^= byte(pn >> (8 * i))
	}
	return n
}

func (k initialKeys) mask(sample []byte) []byte {
	m := make([]byte, aes.BlockSize)
	k.hp.Encrypt(m, sample)
	return m
}

type QUICParams struct {
	Fingerprint string
	SNI         string
	SCIDLen     int
}

func QUICInitial(p QUICParams) ([]byte, error) {
	dcid := make([]byte, 8)
	scid := make([]byte, p.SCIDLen)
	if _, err := rand.Read(dcid); err != nil {
		return nil, err
	}
	if _, err := rand.Read(scid); err != nil {
		return nil, err
	}
	hello, err := clientHello(p.Fingerprint, p.SNI, scid)
	if err != nil {
		return nil, err
	}
	keys, err := clientInitialKeys(dcid)
	if err != nil {
		return nil, err
	}

	header := []byte{0xc0 | (pnLen - 1)}
	header = binary.BigEndian.AppendUint32(header, quicVersion1)
	header = append(header, byte(len(dcid)))
	header = append(header, dcid...)
	header = append(header, byte(len(scid)))
	header = append(header, scid...)
	header = append(header, 0)
	payloadLen := QUICInitialSize - len(header) - 2 - pnLen - aeadTagSize
	header = binary.BigEndian.AppendUint16(header, 0x4000|uint16(pnLen+payloadLen+aeadTagSize))
	pnOffset := len(header)
	header = append(header, 0, 0)

	frame := []byte{0x06, 0x00}
	frame = appendVarint(frame, uint64(len(hello)))
	frame = append(frame, hello...)
	if len(frame) > payloadLen {
		return nil, fmt.Errorf("client hello of %d bytes does not fit into one initial packet", len(hello))
	}
	plain := make([]byte, payloadLen)
	copy(plain, frame)

	packet := keys.aead.Seal(header, keys.nonce(0), plain, header)
	mask := keys.mask(packet[pnOffset+4 : pnOffset+4+aes.BlockSize])
	packet[0] ^= mask[0] & 0x0f
	for i := 0; i < pnLen; i++ {
		packet[pnOffset+i] ^= mask[1+i]
	}
	return packet, nil
}

func appendVarint(b []byte, v uint64) []byte {
	switch {
	case v < 1<<6:
		return append(b, byte(v))
	case v < 1<<14:
		return binary.BigEndian.AppendUint16(b, 0x4000|uint16(v))
	case v < 1<<30:
		return binary.BigEndian.AppendUint32(b, 0x80000000|uint32(v))
	}
	return binary.BigEndian.AppendUint64(b, 0xc000000000000000|v)
}

func readVarint(b []byte) (uint64, int, error) {
	if len(b) == 0 {
		return 0, 0, errors.New("short varint")
	}
	n := 1 << (b[0] >> 6)
	if len(b) < n {
		return 0, 0, errors.New("short varint")
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, n, nil
}
