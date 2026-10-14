// Package registry is the lab's side of the service registry (SPEC.md
// §8.3.3): the signed index the registry's CI publishes, the root key's
// statement naming the keys that may sign it, building both from a
// checkout of the registry repository, and fetching them onto the
// controller as the catalog.
//
// Trust runs in a chain, each link checked with Ed25519 from Go's
// standard library: the root public keys built into Roostack (rootkeys.go)
// sign keys.json, which names the signing keys; a signing key signs
// index.json; the index holds the SHA-256 of every package file and icon.
// A lab never trusts the address it fetched from, so any mirror will do.
package registry

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Key strings: "ed25519:" and the standard base64 of the key. The private
// form holds the 32-byte seed the key is derived from.
const (
	publicPrefix  = "ed25519:"
	privatePrefix = "ed25519-private:"
)

// EncodePublic writes a public key as the index and key statement name it.
func EncodePublic(k ed25519.PublicKey) string {
	return publicPrefix + base64.StdEncoding.EncodeToString(k)
}

// ParsePublic reads a public key written by EncodePublic.
func ParsePublic(s string) (ed25519.PublicKey, error) {
	b64, ok := strings.CutPrefix(strings.TrimSpace(s), publicPrefix)
	if !ok {
		return nil, fmt.Errorf("%q isn't an Ed25519 public key (ed25519:…)", s)
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%q isn't an Ed25519 public key (ed25519:…)", s)
	}
	return ed25519.PublicKey(b), nil
}

// NewKey makes a key pair and returns the private key in the form
// ParsePrivate reads.
func NewKey() (ed25519.PublicKey, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	return pub, privatePrefix + base64.StdEncoding.EncodeToString(priv.Seed()) + "\n", nil
}

// ParsePrivate reads a private key written by NewKey (a key file, or the
// registry CI's secret).
func ParsePrivate(s string) (ed25519.PrivateKey, error) {
	b64, ok := strings.CutPrefix(strings.TrimSpace(s), privatePrefix)
	if !ok {
		return nil, errors.New("not a Roostack registry private key (ed25519-private:…)")
	}
	seed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("not a Roostack registry private key (ed25519-private:…)")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// LoadPrivate reads a private key file.
func LoadPrivate(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := ParsePrivate(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// Signatures is a .sig file: Ed25519 signatures of the exact bytes of the
// file beside it. A list, so a statement can carry an old and a new root
// key's signatures while Roostack releases move from one to the other.
type Signatures struct {
	Signatures []Signature `json:"signatures"`
}

// Signature is one key's signature.
type Signature struct {
	Key string `json:"key"` // EncodePublic
	Sig string `json:"sig"` // standard base64
}

// Sign signs data with each key and returns the .sig file.
func Sign(data []byte, keys ...ed25519.PrivateKey) []byte {
	var s Signatures
	for _, k := range keys {
		s.Signatures = append(s.Signatures, Signature{
			Key: EncodePublic(k.Public().(ed25519.PublicKey)),
			Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(k, data)),
		})
	}
	out, _ := json.MarshalIndent(s, "", "  ")
	return append(out, '\n')
}

// Verify checks that sig (a .sig file) holds a valid signature of data by
// one of the trusted keys, and returns that key. Signatures by other keys
// are ignored.
func Verify(data, sig []byte, trusted []ed25519.PublicKey) (ed25519.PublicKey, error) {
	var s Signatures
	if err := json.Unmarshal(sig, &s); err != nil {
		return nil, fmt.Errorf("reading the signature: %w", err)
	}
	for _, one := range s.Signatures {
		k, err := ParsePublic(one.Key)
		if err != nil || !contains(trusted, k) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(one.Sig)
		if err != nil {
			continue
		}
		if ed25519.Verify(k, data, raw) {
			return k, nil
		}
	}
	return nil, errors.New("not signed by a key Roostack trusts")
}

// contains says whether keys holds k.
func contains(keys []ed25519.PublicKey, k ed25519.PublicKey) bool {
	return slices.ContainsFunc(keys, func(o ed25519.PublicKey) bool { return o.Equal(k) })
}
