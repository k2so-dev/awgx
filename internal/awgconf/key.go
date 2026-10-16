package awgconf

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

type Key [32]byte

func NewPrivateKey() (Key, error) {
	k, err := NewRandomKey()
	if err != nil {
		return k, err
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	return k, nil
}

func NewRandomKey() (Key, error) {
	var k Key
	_, err := rand.Read(k[:])
	return k, err
}

func ParseKey(s string) (Key, error) {
	var k Key
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != len(k) {
		return k, fmt.Errorf("invalid key %q", mask(s))
	}
	copy(k[:], b)
	return k, nil
}

func (k Key) Public() Key {
	var p Key
	pub, _ := curve25519.X25519(k[:], curve25519.Basepoint)
	copy(p[:], pub)
	return p
}

func (k Key) IsZero() bool {
	return k == Key{}
}

func (k Key) String() string {
	return base64.StdEncoding.EncodeToString(k[:])
}

func (k Key) MarshalText() ([]byte, error) {
	return []byte(k.String()), nil
}

func (k *Key) UnmarshalText(b []byte) error {
	v, err := ParseKey(string(b))
	if err != nil {
		return err
	}
	*k = v
	return nil
}

func mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + "****"
}
