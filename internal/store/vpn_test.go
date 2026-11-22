package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// The lab's VPN devices (SPEC.md §10.11): each gets the next free address,
// a person's are limited, a freed address is used again, and a person's
// devices go with them.
func TestVPNDevices(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sam, _ := s.CreateUser(ctx, User{Username: "sam", PasswordHash: "x", Role: "member"})
	jo, _ := s.CreateUser(ctx, User{Username: "jo", PasswordHash: "x", Role: "owner"})
	add := func(user int64, key string, limit int) (VPNDevice, error) {
		return s.AddVPNDevice(ctx, VPNDevice{UserID: user, Name: key, PublicKey: key}, limit)
	}
	a, err := add(sam, "k1", 2)
	if err != nil || a.Address != "100.96.128.2" {
		t.Fatalf("first: %+v %v", a, err)
	}
	b, _ := add(sam, "k2", 2)
	if _, err := add(sam, "k3", 2); !errors.Is(err, ErrDeviceLimit) {
		t.Errorf("past the limit: %v", err)
	}
	c, _ := add(jo, "k4", 0)
	if b.Address != "100.96.128.3" || c.Address != "100.96.128.4" {
		t.Errorf("addresses %s %s", b.Address, c.Address)
	}
	if _, err := add(jo, "k1", 0); err == nil {
		t.Error("a key used twice")
	}
	s.DeleteVPNDevice(ctx, a.ID)
	if d, _ := add(jo, "k5", 0); d.Address != "100.96.128.2" {
		t.Errorf("a freed address: %s", d.Address)
	}
	c.SSH, c.Home, c.Internet = true, true, true
	if err := s.SetVPNDevice(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.VPNDevice(ctx, c.ID); !got.SSH || !got.Home || !got.Internet || got.Role != "owner" {
		t.Errorf("changed: %+v", got)
	}
	if err := s.SetVPNDeviceDNS(ctx, c.ID, "192.168.1.5"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.VPNDevice(ctx, c.ID); got.DNS != "192.168.1.5" || !got.SSH {
		t.Errorf("its file's DNS server: %+v", got)
	}
	if list, _ := s.UserVPNDevices(ctx, sam); len(list) != 1 {
		t.Errorf("sam's: %+v", list)
	}
	if err := s.DeleteUser(ctx, sam); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.VPNDevices(ctx); len(list) != 2 {
		t.Errorf("after removing sam: %+v", list)
	}
}

// A database from before step 42, where a device had one of three choices:
// the home network stays the home network, and everything is the internet.
func TestVPNDeviceReachMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roostack.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(slices.Clone(schema), migrations[:41-schemaVersion]...) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`PRAGMA user_version = 41`,
		`INSERT INTO users (id, username, password_hash, role, created_at) VALUES (1, 'sam', 'x', 'owner', 0)`,
		`INSERT INTO vpn_devices (user_id, name, public_key, address, reach, created_at) VALUES
			(1, 'a', 'ka', '100.96.128.2', 'lab', 0), (1, 'b', 'kb', '100.96.128.3', 'home', 0), (1, 'c', 'kc', '100.96.128.4', 'everything', 0)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.VPNDevices(context.Background())
	if err != nil || len(list) != 3 || list[0].Home || list[0].Internet || !list[1].Home || list[1].Internet || list[2].Home || !list[2].Internet {
		t.Errorf("%v %+v", err, list)
	}
}
