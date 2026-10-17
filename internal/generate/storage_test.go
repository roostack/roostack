package generate

import (
	"slices"
	"strings"
	"testing"
)

// A guest lab's password file has a line per server, and nothing that
// could add a line or a user of its own.
func TestGuestStoreLines(t *testing.T) {
	hash := "$2a$10$" + strings.Repeat("a", 53)
	g := GuestStorage{User: "g3", Users: map[string]string{
		"n-0000000002": hash,
		"n-0000000001": hash,
		"evil":         hash + "\ng3:" + hash, // a second line
		"a:b":          hash,
		"n-3":          "plain",
		"g3":           hash, // the shared user is only ever StoreHash
	}}
	want := []string{"n-0000000001:" + hash, "n-0000000002:" + hash}
	if got := g.StoreLines(); !slices.Equal(got, want) {
		t.Errorf("lines = %q", got)
	}
	// A guest invited before servers had their own users keeps its
	// shared one.
	g.StoreHash = hash
	if got := g.StoreLines(); len(got) != 3 || got[0] != "g3:"+hash {
		t.Errorf("with the shared user: %q", got)
	}
}
