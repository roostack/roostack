package store

import (
	"context"
	"errors"
	"path/filepath"
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
		return s.AddVPNDevice(ctx, VPNDevice{UserID: user, Name: key, PublicKey: key, Reach: ReachLab}, limit)
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
	c.SSH, c.Reach = true, ReachEverything
	if err := s.SetVPNDevice(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.VPNDevice(ctx, c.ID); !got.SSH || got.Reach != ReachEverything || got.Role != "owner" {
		t.Errorf("changed: %+v", got)
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
