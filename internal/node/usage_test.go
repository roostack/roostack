package node

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
)

// statusCalls counts systemctl calls and answers each unit as active,
// but one service's as failed.
type statusCalls struct {
	calls  int
	failed string
}

func (s *statusCalls) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	s.calls++
	var out []systemd.UnitStatus
	for _, u := range units {
		st := "active"
		if strings.HasPrefix(u, s.failed) {
			st = "failed"
		}
		out = append(out, systemd.UnitStatus{Unit: u, LoadState: "loaded", ActiveState: st})
	}
	return out, nil
}

// TestServiceStatesOneCall: every service's state comes from one
// systemctl call, each from its own units.
func TestServiceStatesOneCall(t *testing.T) {
	p, err := pkgspec.Parse([]byte("schema: roostack/v1alpha1\nname: app\ntitle: App\nversion: \"1\"\ncontainers:\n  web: { image: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	units := &statusCalls{failed: "roostack-app-two-"}
	n := &Local{Lab: config.Default(), Units: units, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, name := range []string{"app", "app-two", "app-three"} {
		n.assignment.Services = append(n.assignment.Services, state.Service{Package: p,
			Instance: &instance.Instance{Name: name, Package: "app", Version: "1", Updates: instance.UpdatesNotify}})
	}
	got, err := n.ServiceStates(context.Background())
	if err != nil || units.calls != 1 {
		t.Fatalf("%v, %d calls", err, units.calls)
	}
	if got["app"] != "running" || got["app-two"] != "failed" || got["app-three"] != "running" {
		t.Errorf("states %v", got)
	}
}
