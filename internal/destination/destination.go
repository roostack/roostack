// Package destination reads and writes backup destination references,
// "kind:id", as the store, lab.yaml and the backup forms keep them
// (SPEC.md §10.10). Code switches on a Ref's Kind rather than on a
// string's prefix.
package destination

import (
	"strconv"
	"strings"
)

// The kinds of reference.
const (
	Cloud    = "cloud"    // Roostack Cloud backup: "cloud"
	Server   = "server"   // a server's backup store in this lab: "server:<server ID>"
	Drive    = "drive"    // a drive on the server: "drive:<drive ID>"
	Folder   = "folder"   // a folder on the server: "folder:<path>"
	Local    = "local"    // a folder on the server, as older references name it: "local:<path>"
	Provider = "provider" // online storage or a friend's lab: "provider:<provider ID>"
	Setup    = "setup"    // set up by the server's own setup wizard: "setup"
)

// Ref is a backup destination's reference.
type Ref struct {
	Kind string
	// ID follows the colon: a server, drive or provider ID, or a path.
	ID string
}

// Parse reads a reference. One without a colon is a kind alone.
func Parse(ref string) Ref {
	kind, id, _ := strings.Cut(ref, ":")
	return Ref{Kind: kind, ID: id}
}

// String is the reference as it's kept: "kind:id", or the kind alone.
func (r Ref) String() string {
	if r.ID == "" {
		return r.Kind
	}
	return r.Kind + ":" + r.ID
}

// New is the reference to id of a kind.
func New(kind, id string) string { return Ref{Kind: kind, ID: id}.String() }

// OfProvider is the reference to a backup provider (store.BackupProvider).
func OfProvider(id int64) string { return New(Provider, strconv.FormatInt(id, 10)) }
