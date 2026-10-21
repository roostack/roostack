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

// A detached run's unit: started without waiting, kept after its command
// ends so how it went can be read, and cleared.
func TestRunDetached(t *testing.T) {
	var calls []string
	show := ""
	m := &Manager{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte(show), nil
	}}
	ctx := context.Background()
	m.RunDetached(ctx, "roostack-photos-immich", "podman", "run", "--rm", "x")
	if want := "systemd-run --user --quiet --unit=roostack-photos-immich --property=RemainAfterExit=yes --property=KillMode=mixed --property=TimeoutStopSec=60 -- podman run --rm x"; calls[0] != want {
		t.Errorf("started with %q", calls[0])
	}
	for _, c := range []struct {
		show           string
		running, found bool
		code           int
	}{
		{"LoadState=loaded\nActiveState=active\nSubState=running\nExecMainStatus=0\n", true, true, 0},
		{"LoadState=loaded\nActiveState=active\nSubState=exited\nExecMainStatus=0\n", false, true, 0},
		{"LoadState=loaded\nActiveState=failed\nSubState=failed\nExecMainStatus=3\n", false, true, 3},
		{"LoadState=not-found\nActiveState=inactive\nSubState=dead\nExecMainStatus=0\n", false, false, 0},
	} {
		show = c.show
		running, code, found, err := m.Exited(ctx, "roostack-photos-immich")
		if err != nil || running != c.running || code != c.code || found != c.found {
			t.Errorf("%q: running %v code %d found %v %v", c.show, running, code, found, err)
		}
	}
	calls = nil
	m.Forget(ctx, "roostack-photos-immich")
	if want := []string{"systemctl --user stop roostack-photos-immich.service", "systemctl --user reset-failed roostack-photos-immich.service"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("forget: %q", calls)
	}
}
