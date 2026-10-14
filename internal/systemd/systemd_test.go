package systemd

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	var calls []string
	m := &Manager{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}}
	ctx := context.Background()
	m.DaemonReload(ctx)
	m.Start(ctx, "a.service", "b.service")
	m.Stop(ctx) // no units: no call
	m.Reload(ctx, "roostack-caddy.service")

	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user start a.service b.service",
		"systemctl --user reload roostack-caddy.service",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}

func TestParseShow(t *testing.T) {
	out := "Id=a.service\nLoadState=loaded\nActiveState=active\nSubState=running\nNRestarts=3\n\n" +
		"Id=b.service\nLoadState=not-found\nActiveState=inactive\nSubState=dead\n"
	got := parseShow([]byte(out))
	want := []UnitStatus{
		{"a.service", "loaded", "active", "running", 3},
		{"b.service", "not-found", "inactive", "dead", 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
