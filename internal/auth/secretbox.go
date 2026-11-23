package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
)

const keyFileLen = 32

// sealer encrypts TOTP secrets at rest with AES-256-GCM. The key lives in a separate 0600 file, so a copy
// of the database alone (a backup, a stolen file) does not reveal the second factor of every user.
type sealer struct{ aead cipher.AEAD }

// loadOrCreateKey reads the key file or creates it (O_EXCL, 0600) when it does not exist.
// An existing file that is a link, has a wrong size or is readable by group or others is refused.
func loadOrCreateKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, keyFileLen)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("impossibile creare la chiave dei segreti: %w", err)
		}
		_, werr := f.Write(key)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(path)
			return nil, werr
		}
		return key, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("la chiave dei segreti deve essere un file normale, non un link")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("la chiave dei segreti e' leggibile da gruppo o altri: chmod 600")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	key, err := io.ReadAll(io.LimitReader(f, keyFileLen+1))
	if err != nil {
		return nil, err
	}
	if len(key) != keyFileLen {
		return nil, errors.New("la chiave dei segreti ha una lunghezza sbagliata")
	}
	return key, nil
}

func newSealer(key []byte) (*sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead}, nil
}

// seal encrypts plain; aad binds the ciphertext to its owner (the username) so rows cannot be swapped.
func (s *sealer) seal(plain []byte, aad string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, []byte(aad)), nil
}

func (s *sealer) open(box []byte, aad string) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(box) < n+s.aead.Overhead() {
		return nil, errors.New("segreto cifrato non valido")
	}
	return s.aead.Open(nil, box[:n], box[n:], []byte(aad))
}
