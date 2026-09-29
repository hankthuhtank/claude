//go:build !windows

package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
)

// aesSealer is used on non-Windows systems (development, CI and tests).
// The key file is created with 0600 permissions next to the vault.
type aesSealer struct{ aead cipher.AEAD }

func newSealer(dir string) (sealer, error) {
	keyPath := filepath.Join(dir, "vault.key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("vault key file is damaged")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &aesSealer{aead: aead}, nil
}

func (s *aesSealer) Scheme() string { return "aes-256-gcm-keyfile" }

func (s *aesSealer) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, []byte("backplane.vault.v1")), nil
}

func (s *aesSealer) Open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("vault entry is damaged")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], []byte("backplane.vault.v1"))
}
