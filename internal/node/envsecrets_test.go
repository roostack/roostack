package node

import (
	"testing"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Only a name in exactly an extra variable's form, for one of the
// service's containers, is taken for its secret: never a secret setting,
// nor another service whose name starts the same.
func TestEnvSecretOf(t *testing.T) {
	p := &pkgspec.Package{Containers: map[string]*pkgspec.Container{"app": {}, "db": {}}}
	for name, want := range map[string]bool{
		"roostack-forgejo-env-app-FORGEJO__mailer__PASSWD-3": true,
		"roostack-forgejo-env-db-POSTGRES_X-1":               true,
		"roostack-forgejo-admin_password":                    false,
		"roostack-forgejo-env-web-X-1":                       false, // no such container
		"roostack-forgejo-env-app-X":                         false, // no revision
		"roostack-forgejo-env-app-1X-2":                      false, // not a variable's name
		"roostack-forgejo-envoy-app-X-1":                     false,
		"roostack-forgejo-work-env-app-X-1":                  false, // another service
	} {
		if got := envSecretOf(p, "forgejo", name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}
