package node

import (
	"os"
	"testing"
)

func TestIdentity(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreate(dir)
	if err != nil || !ValidID(a.ID) || a.Name == "" {
		t.Fatalf("created %+v, %v", a, err)
	}
	b, err := LoadOrCreate(dir)
	if err != nil || b != a {
		t.Errorf("identity changed: %+v then %+v (%v)", a, b, err)
	}
	os.WriteFile(File(dir), []byte(`{"id":"../evil"}`), 0o640)
	if _, err := LoadOrCreate(dir); err == nil {
		t.Error("an invalid ID must be rejected")
	}
}
