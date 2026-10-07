package authkit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// Sealer encrypts secrets kept in a database (Cloud's two-factor secrets)
// with a key kept outside it (AES-256-GCM): a stolen database alone gives
// no one the codes. Roostack, whose database and keys are on one machine,
// doesn't use it.
type Sealer struct {
	aead cipher.AEAD
}

// sealedPrefix marks a sealed value and its format.
const sealedPrefix = "sealed1:"

// NewSealer uses a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("authkit: a sealing key is 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext, with a random nonce each time.
func (s *Sealer) Seal(plaintext string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(sealedPrefix))
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts what Seal made, and fails if it was changed or sealed
// with another key.
func (s *Sealer) Open(sealed string) (string, error) {
	b64, ok := strings.CutPrefix(sealed, sealedPrefix)
	// Go's decoder skips line breaks even when strict (found by fuzzing).
	if !ok || strings.ContainsAny(b64, "\r\n") {
		return "", errors.New("authkit: not a sealed value")
	}
	// Strict: base64 would otherwise ignore the last character's unused
	// bits, so a value would have several spellings (found by fuzzing).
	data, err := base64.RawStdEncoding.Strict().DecodeString(b64)
	if err != nil || len(data) < s.aead.NonceSize() {
		return "", errors.New("authkit: a sealed value is damaged")
	}
	n := s.aead.NonceSize()
	out, err := s.aead.Open(nil, data[:n], data[n:], []byte(sealedPrefix))
	if err != nil {
		return "", errors.New("authkit: a sealed value can't be opened with this key")
	}
	return string(out), nil
}
