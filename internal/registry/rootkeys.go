package registry

import (
	"crypto/ed25519"
	"fmt"
)

// rootKeys are the public halves of the registry's root keys, held
// offline by the Owner (SPEC.md §8.3.3, docs/REGISTRY-KEYS.md). They sign
// only keys.json. More than one while a new root key replaces an old one.
//
// Empty until the key ceremony: until then labs can fetch only from a
// registry signed with a development key (devRootKey), and a release
// build refuses to be made (scripts/build-deb.sh).
var rootKeys = []string{}

// devRootKey is a root key for development and testing, given at build
// time: make build REGISTRY_DEV_KEY=ed25519:… (the -X linker flag). Never
// set for a release (scripts/build-deb.sh checks).
var devRootKey string

// RootKeys returns the root keys this build trusts.
func RootKeys() ([]ed25519.PublicKey, error) {
	all := rootKeys
	if devRootKey != "" {
		all = append(all[:len(all):len(all)], devRootKey)
	}
	var keys []ed25519.PublicKey
	for _, s := range all {
		k, err := ParsePublic(s)
		if err != nil {
			return nil, fmt.Errorf("a root key built into Roostack: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}
