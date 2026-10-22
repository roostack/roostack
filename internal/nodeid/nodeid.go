// Package nodeid is this server's identity in a lab (SPEC.md §10): a
// stable node ID and a name. It has no dependencies so any package can
// check node IDs.
package nodeid

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"roostack.cloud/roostack/internal/fsutil"
)

// Identity names this server. It's local to the server: it must never
// be part of the state replica other nodes receive (SPEC.md §10.1).
type Identity struct {
	// ID is stable for the life of the install, e.g. "n-3f9c2a7b1d".
	ID string `json:"id"`
	// Name is for people, e.g. the hostname ("nuc", "vps-sydney").
	Name string `json:"name"`
}

// File is where the identity is kept, in Roostack's state directory.
func File(stateDir string) string { return filepath.Join(stateDir, "node.json") }

var idRe = regexp.MustCompile(`^n-[0-9a-f]{10}$`)

// ValidID reports whether id looks like a node ID.
func ValidID(id string) bool { return idRe.MatchString(id) }

// LoadOrCreate reads this server's identity, creating it on first use.
func LoadOrCreate(stateDir string) (Identity, error) {
	path := File(stateDir)
	data, err := os.ReadFile(path)
	if err == nil {
		var id Identity
		if err := json.Unmarshal(data, &id); err != nil {
			return Identity{}, fmt.Errorf("%s: %w", path, err)
		}
		if !ValidID(id.ID) {
			return Identity{}, fmt.Errorf("%s: invalid node ID %q", path, id.ID)
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, err
	}
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return Identity{}, err
	}
	name, _ := os.Hostname()
	id := Identity{ID: "n-" + hex.EncodeToString(b), Name: name}
	return id, id.Save(stateDir)
}

// Save writes the identity (e.g. after renaming the server).
func (id Identity) Save(stateDir string) error {
	path := File(stateDir)
	data, _ := json.MarshalIndent(id, "", "  ")
	return fsutil.WriteFile(path, append(data, '\n'), 0o640)
}
