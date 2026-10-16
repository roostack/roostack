package node

import (
	"os"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// A node checks the package policy again on what the controller sends,
// rather than trust that the controller did (SPEC.md §8.3.4): a registry
// package that breaks it is refused, saying why; one of the Owner's own
// may break it.
func TestDecodeChecksPolicy(t *testing.T) {
	data, err := os.ReadFile(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	risky := strings.Replace(string(data), "\n  app:\n",
		"\n  app:\n    quadlet:\n      Container:\n        Volume: /var/lib/roostack:/lab\n", 1)
	if risky == string(data) {
		t.Fatal("the fixture has no app container to change")
	}
	p, err := pkgspec.Parse([]byte(risky))
	if err != nil {
		t.Fatal(err)
	}
	wire := func(local bool) WireAssignment {
		in := &instance.Instance{Package: "vaultwarden", Version: p.Version, Settings: map[string]any{},
			Routes: map[string]instance.RouteOverride{"web": {Port: 18000}}, Local: local}
		b, err := in.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return WireAssignment{Version: 1, Services: []WireService{{Instance: b, Package: []byte(risky)}}}
	}

	_, err = Decode(wire(false))
	if err == nil || !strings.Contains(err.Error(), "breaks the package policy") || !strings.Contains(err.Error(), "Container.Volume=/var/lib/roostack:/lab") {
		t.Errorf("a registry package breaking the policy: %v", err)
	}
	if a, err := Decode(wire(true)); err != nil || len(a.Services) != 1 {
		t.Errorf("one of the Owner's own: %v", err)
	}
	if _, err := Decode(WireAssignment{Version: 1, Proxied: []WireProxied{{WireService: wire(false).Services[0]}}}); err == nil {
		t.Error("a proxied service breaking the policy was accepted")
	}
}
