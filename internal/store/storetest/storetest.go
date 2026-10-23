// Package storetest opens a new store for a test (quality review T9).
package storetest

import (
	"path/filepath"
	"testing"

	"roostack.cloud/roostack/internal/store"
)

// Open opens a new, empty store in the test's temporary folder, closed
// when the test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
