package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
)

// TestTestAddressCmd: a test address is set only on a lab without an
// address, as the roostack user that root starts, and taken away again
// (SPEC.md §6.6). Not parallel: it sets the environment.
func TestTestAddressCmd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lab.yaml")
	lab := config.Default()
	lab.Paths.StateDir = dir
	lab.DNS.LANIP = "192.168.1.5"
	if err := lab.Save(path); err != nil {
		t.Fatal(err)
	}
	load := func() config.Lab {
		t.Helper()
		l, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	if err := testAddressCmd([]string{"-lab", path}); err == nil || !strings.Contains(err.Error(), "run it as root") {
		t.Errorf("not started by root: %v", err)
	}
	t.Setenv(asServiceUserEnv, "1")
	if err := testAddressCmd([]string{"-lab", path, "Not_A_Name"}); err == nil || load().Domain != "" {
		t.Errorf("a name that isn't a DNS label: %v", err)
	}
	if err := testAddressCmd([]string{"-lab", path}); err != nil {
		t.Fatal(err)
	}
	if l := load(); l.Domain != "lab.localhost" || !l.TestAddress() || l.DNS.LANIP != "192.168.1.5" {
		t.Errorf("set: %+v", l)
	}
	if err := testAddressCmd([]string{"-lab", path}); !errors.Is(err, nothingChanged) {
		t.Errorf("the same again: %v", err)
	}
	if err := testAddressCmd([]string{"-lab", path, "smiths"}); err != nil || load().Domain != "smiths.localhost" {
		t.Errorf("another name: %v, %s", err, load().Domain)
	}
	if err := testAddressCmd([]string{"-lab", path, "-off"}); err != nil {
		t.Fatal(err)
	}
	if l := load(); l.Domain != "" || l.DNS.Provider != "" || l.DNS.LANIP != "192.168.1.5" {
		t.Errorf("off: %+v", l)
	}
	if err := testAddressCmd([]string{"-lab", path, "-off"}); !errors.Is(err, nothingChanged) {
		t.Errorf("off again: %v", err)
	}

	// Never in place of a real address.
	config.Update(path, func(l *config.Lab) { l.Domain, l.DNS.Provider = "lab.example.com", "cloudflare" })
	if err := testAddressCmd([]string{"-lab", path}); err == nil || !strings.Contains(err.Error(), "already has an address") {
		t.Errorf("a lab with an address: %v", err)
	}
	if err := testAddressCmd([]string{"-lab", path, "-off"}); !errors.Is(err, nothingChanged) || load().Domain != "lab.example.com" {
		t.Errorf("-off on a real address: %v, %s", err, load().Domain)
	}
}
