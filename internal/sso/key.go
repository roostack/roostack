// Package sso is Roostack's identity provider (SPEC.md §5.2): the OIDC
// endpoints apps sign people in through, and forward auth, the wall in
// front of apps that have no sign-in of their own. People sign in on
// Roostack's own pages (internal/web); this package hands out and checks
// what apps get. It uses the standard library only.
//
// It is kept narrow: the authorisation-code flow (with PKCE when an app
// sends it) for apps Roostack registered itself, no implicit flow, no
// registration by apps, no consent screen.
package sso

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

// Key signs ID tokens: RSA, as almost every app expects RS256.
type Key struct {
	priv *rsa.PrivateKey
	// ID names it in tokens and the published key set ("kid").
	ID string
}

// keyBits is the RSA key's size.
const keyBits = 2048

// LoadKey reads the signing key at path, making one (private, 0600) if
// there's none: it's backed up with the lab, so a restored lab's apps keep
// trusting it.
func LoadKey(path string) (*Key, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newKey(path)
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("sso: %s isn't a PEM key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("sso: %s: %w", path, err)
	}
	priv, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("sso: %s isn't an RSA key", path)
	}
	return keyOf(priv), nil
}

func newKey(path string) (*Key, error) {
	priv, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Written whole or not at all, never readable by others.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return keyOf(priv), nil
}

func keyOf(priv *rsa.PrivateKey) *Key {
	sum := sha256.Sum256(append(priv.N.Bytes(), big.NewInt(int64(priv.E)).Bytes()...))
	return &Key{priv: priv, ID: b64(sum[:12])}
}

// JWKS is the public key set apps check ID tokens with.
func (k *Key) JWKS() []byte {
	pub := k.priv.PublicKey
	data, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": k.ID,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
	return data
}

// Sign makes a signed token (JWS compact, RS256) of claims.
func (k *Key) Sign(claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.ID})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64(header) + "." + b64(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
