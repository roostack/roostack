// Package node is one machine in a lab (SPEC.md §10). Every install is a
// node; one node is also the controller. For now there is only ever one.
package node

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Identity names this machine. It's local to the machine: it must never
// be part of the state replica other nodes receive (SPEC.md §10.1).
type Identity struct {
	// ID is stable for the life of the install, e.g. "n-3f9c2a7b1d".
	ID string `json:"id"`
	// Name is for people, e.g. the hostname ("nuc", "vps-sydney").
	Name string `json:"name"`
}

// File is where the identity is kept, in MyLab's state directory.
func File(stateDir string) string { return filepath.Join(stateDir, "node.json") }

var idRe = regexp.MustCompile(`^n-[0-9a-f]{10}$`)

// ValidID reports whether id looks like a node ID.
func ValidID(id string) bool { return idRe.MatchString(id) }

// LoadOrCreate reads this machine's identity, creating it on first use.
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
	data, _ = json.MarshalIndent(id, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o640); err != nil {
		return Identity{}, err
	}
	return id, os.Rename(tmp, path)
}
