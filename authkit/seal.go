package authkit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// Sealer encrypts secrets kept in a database (Cloud's two-factor secrets
// and the emails waiting in its outbox) with a key kept outside it
// (AES-256-GCM): a stolen database alone gives no one the codes. Roostack,
// whose database and keys are on one machine, doesn't use it.
//
// Seal and Open seal a value on its own. SealFor and OpenFor seal it for
// its owner (a user's or an admin's ID), so a sealed value copied to
// another owner's row, by someone who can write the database but hasn't
// the key, doesn't open there. The two forms have their own prefixes and
// neither opens as the other.
type Sealer struct {
	aead cipher.AEAD
}

// The prefixes mark a sealed value and its format. Each is also GCM's
// additional data, so a value's prefix can't be swapped for the other's:
// Seal's is the prefix alone, SealFor's the prefix and the owner (the
// prefix is fixed, so the owner can't be confused with it).
const (
	sealedPrefix    = "sealed1:"
	sealedForPrefix = "sealedfor1:"
)

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

// IsSealed is whether s is in Seal's form ("sealed1:…"), without opening
// it, for telling values sealed on their own from others in a column.
// It's false for SealFor's, which have their own prefix ("sealedfor1:…").
func IsSealed(s string) bool { return strings.HasPrefix(s, sealedPrefix) }

// Seal encrypts plaintext, with a random nonce each time.
func (s *Sealer) Seal(plaintext string) (string, error) {
	return s.seal(sealedPrefix, sealedPrefix, plaintext)
}

// Open decrypts what Seal made, and fails if it was changed or sealed
// with another key. It doesn't open what SealFor made.
func (s *Sealer) Open(sealed string) (string, error) {
	return s.open(sealedPrefix, sealedPrefix, sealed)
}

// SealFor encrypts plaintext for owner, with a random nonce each time:
// only OpenFor with the same owner opens it. The owner isn't stored in
// the value, only bound to it (as GCM's additional data), so the caller
// gives it again to open it, usually the ID of the row the value is in.
func (s *Sealer) SealFor(owner, plaintext string) (string, error) {
	if owner == "" {
		return "", errors.New("authkit: sealing for no one")
	}
	return s.seal(sealedForPrefix, sealedForPrefix+owner, plaintext)
}

// OpenFor decrypts what SealFor made for owner, and fails if it was
// changed, sealed with another key or sealed for someone else (which GCM
// can't tell apart). It doesn't open what Seal made, even when that
// plaintext starts with the owner: a caller with values sealed before it
// used SealFor tells them apart by their prefix (IsSealed) and opens them
// with Open.
func (s *Sealer) OpenFor(owner, sealed string) (string, error) {
	if owner == "" {
		return "", errors.New("authkit: opening for no one")
	}
	out, err := s.open(sealedForPrefix, sealedForPrefix+owner, sealed)
	if errors.Is(err, errWrongKey) {
		return "", errors.New("authkit: a sealed value can't be opened with this key, or was sealed for someone else")
	}
	return out, err
}

var errWrongKey = errors.New("authkit: a sealed value can't be opened with this key")

// seal encrypts plaintext with additional data ad, and writes it after
// prefix.
func (s *Sealer) seal(prefix, ad, plaintext string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(ad))
	return prefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// open decrypts what seal made with the same prefix and ad.
func (s *Sealer) open(prefix, ad, sealed string) (string, error) {
	b64, ok := strings.CutPrefix(sealed, prefix)
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
	out, err := s.aead.Open(nil, data[:n], data[n:], []byte(ad))
	if err != nil {
		return "", errWrongKey
	}
	return string(out), nil
}
