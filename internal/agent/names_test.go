package agent

import (
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
)

// The agent can't import the packages that own these names, so it has its
// own checks (the quality review of 2026-10-21, D4). serviceName may be
// looser than a service's ID, but never stricter; whoName is exactly an
// account's name.
func TestNamesMatchTheirOwners(t *testing.T) {
	for _, name := range []string{"vaultwarden", "immich-2", "a", "a-b-c", "x1", "ab-", "a--b", "1abc", "-a", "A", "a.b", "a/b", "..",
		"a234567890123456789012345678901234567890", "a2345678901234567890123456789012345678901"} {
		if instance.ValidName(name) && !serviceName.MatchString(name) {
			t.Errorf("serviceName refuses the service ID %q", name)
		}
		if serviceName.MatchString(name) && strings.ContainsAny(name, "/. ") {
			t.Errorf("serviceName accepts %q", name)
		}
	}
	for _, who := range []string{"alice", "Alice.Smith", "a_b-c", "1", "", ".alice", "-a", "a b", "a/b", "a@b",
		"a2345678901234567890123456789012", "a23456789012345678901234567890123"} {
		if got, want := whoName.MatchString(who), auth.ValidUsername(who); got != want {
			t.Errorf("whoName(%q) = %v, auth.ValidUsername = %v", who, got, want)
		}
	}
}

// The agent's fixed backup folder is config's default (review D6).
func TestSystemBackupDirIsTheDefault(t *testing.T) {
	if want := config.Default().Paths.BackupStoreDir(); SystemBackupDir != want {
		t.Errorf("SystemBackupDir = %s, config's default BackupStoreDir = %s", SystemBackupDir, want)
	}
}
