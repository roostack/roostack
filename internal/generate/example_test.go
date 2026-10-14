package generate

import (
	"strings"
	"testing"
)

// Every fixture renders as an example: its defaults, an example lab.
func TestExample(t *testing.T) {
	for _, name := range []string{"forgejo", "immich", "vaultwarden"} {
		p := loadExample(t, name)
		out, err := Service(ExampleLab(), p, ExampleInstance(p))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.CaddySite == nil || !strings.Contains(out.CaddySite.Content, "."+ExampleDomain) {
			t.Errorf("%s: the Caddy site should use the example domain", name)
		}
	}
}
