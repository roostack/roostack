package node

import (
	"os"
	"path/filepath"
	"testing"

	"roostack.cloud/roostack/internal/labdns"
)

// TestApplyLabDNS: the names file is written where Roostack's own DNS
// server runs (which starts it), left alone when nothing changed, and
// deleted elsewhere (which stops it).
func TestApplyLabDNS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns", "names.json")
	n := &Local{}
	var said []string
	printf := func(f string, _ ...any) { said = append(said, f) }
	c := &labdns.Config{Domain: "lab.example.com", Names: map[string]string{"roost.lab.example.com": "192.168.1.5"}}
	if err := n.applyLabDNS(path, c, printf); err != nil {
		t.Fatal(err)
	}
	if got, err := labdns.Read(path); err != nil || got.Names["roost.lab.example.com"] != "192.168.1.5" {
		t.Fatalf("names file = %+v, %v", got, err)
	}
	info, _ := os.Stat(path)
	if err := n.applyLabDNS(path, c, printf); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.Stat(path); !again.ModTime().Equal(info.ModTime()) || len(said) != 1 {
		t.Errorf("rewritten with nothing changed, or said again: %v", said)
	}
	if err := n.applyLabDNS(path, nil, printf); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || len(said) != 2 {
		t.Errorf("names file still there (%v), or not said: %v", err, said)
	}
	if err := n.applyLabDNS(path, nil, printf); err != nil || len(said) != 2 {
		t.Errorf("gone already: %v, said %v", err, said)
	}
}
