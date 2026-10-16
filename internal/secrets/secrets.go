package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"github.com/k2so-dev/awgx/internal/awgconf"
)

type Box struct {
	identity *age.X25519Identity
}

func LoadOrCreate(path string) (*Box, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
			return nil, err
		}
		return &Box{identity: id}, nil
	}
	if err != nil {
		return nil, err
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &Box{identity: id}, nil
}

func (b *Box) Seal(plain []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := age.Encrypt(&out, b.identity.Recipient())
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (b *Box) Open(sealed []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(sealed), b.identity)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func (b *Box) SealKey(k awgconf.Key) ([]byte, error) {
	if k.IsZero() {
		return nil, nil
	}
	return b.Seal(k[:])
}

func (b *Box) OpenKey(sealed []byte) (awgconf.Key, error) {
	var k awgconf.Key
	if len(sealed) == 0 {
		return k, nil
	}
	plain, err := b.Open(sealed)
	if err != nil {
		return k, err
	}
	if len(plain) != len(k) {
		return k, errors.New("sealed key has wrong length")
	}
	copy(k[:], plain)
	return k, nil
}
