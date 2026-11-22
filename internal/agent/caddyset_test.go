package agent

import (
	"errors"
	"os"
	"slices"
	"testing"
)

// TestCaddySet: the agent puts Caddy's cgroup in the set after a load and
// whenever Caddy's cgroup is made again, and nothing while Caddy isn't
// running or the ruleset has no set.
func TestCaddySet(t *testing.T) {
	ids := map[string]uint64{}
	var ran []string
	c := &caddyCgroup{root: "/cg", path: CaddyCgroup(999),
		stat: func(p string) (uint64, error) {
			if id, ok := ids[p]; ok {
				return id, nil
			}
			return 0, os.ErrNotExist
		},
		nft: func(args ...string) error { ran = append(ran, args...); return nil }}
	full := "/cg/" + CaddyCgroup(999)
	add := `flush set inet roostack caddy; add element inet roostack caddy { "` + CaddyCgroup(999) + `" }`

	// A node's ruleset: nothing to do, whatever runs.
	ids[full] = 7
	c.loaded("table inet roostack {\n}\n")
	c.check()
	if len(ran) != 0 {
		t.Fatalf("ran %q without the set", ran)
	}
	// The controller's: filled at once, then only when Caddy's cgroup is
	// made again.
	ruleset := "table inet roostack {\n\tset caddy {\n\t\ttypeof socket cgroupv2 level 6\n\t}\n}\n"
	c.loaded(ruleset)
	c.check()
	if !slices.Equal(ran, []string{add}) {
		t.Fatalf("after a load ran %q", ran)
	}
	ids[full] = 8
	c.check()
	c.check()
	if !slices.Equal(ran, []string{add, add}) {
		t.Fatalf("after Caddy restarted ran %q", ran)
	}
	// Another load empties the set: filled again.
	c.loaded(ruleset)
	if len(ran) != 3 {
		t.Fatalf("after another load ran %q", ran)
	}
	// Caddy stopped: the set is emptied once.
	delete(ids, full)
	c.check()
	c.check()
	if !slices.Equal(ran[3:], []string{"flush set inet roostack caddy"}) {
		t.Fatalf("after Caddy stopped ran %q", ran[3:])
	}
	// nft failing: tried again next time.
	ids[full] = 9
	c.nft = func(args ...string) error { ran = append(ran, args...); return errors.New("no table") }
	c.check()
	c.check()
	if len(ran) != 6 || c.inSet != 0 {
		t.Errorf("with nft failing ran %q, in the set %d", ran[4:], c.inSet)
	}
}
