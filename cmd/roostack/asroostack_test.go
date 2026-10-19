package main

import (
	"maps"
	"strings"
	"testing"
)

// Output from a command re-run as the roostack user reaches root's
// terminal without escape sequences, however it's split into writes.
func TestControlFilter(t *testing.T) {
	in := "Temporary password: abc\x1b]0;title\x07\r\n\tBacking up: 50%\rné \u009b31m\x7fend\x00\n"
	want := "Temporary password: abc]0;title\r\n\tBacking up: 50%\rné 31mend\n"
	for _, size := range []int{1, 2, 3, len(in)} {
		var out strings.Builder
		f := &controlFilter{w: &out}
		for i := 0; i < len(in); i += size {
			f.Write([]byte(in[i:min(i+size, len(in))]))
		}
		if out.String() != want {
			t.Errorf("writes of %d: got %q, want %q", size, out.String(), want)
		}
	}
}

// Run as the roostack user (sudo -u roostack), a command finds that
// user's systemd manager by itself; root, other users and a session that
// is already set are left as they are.
func TestOwnSession(t *testing.T) {
	env := func(vars ...string) func(string) string {
		return func(k string) string {
			for i := 0; i < len(vars); i += 2 {
				if vars[i] == k {
					return vars[i+1]
				}
			}
			return ""
		}
	}
	want := map[string]string{"XDG_RUNTIME_DIR": "/run/user/999", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/999/bus"}
	for _, c := range []struct {
		name   string
		uid    int
		getenv func(string) string
		want   map[string]string
	}{
		{"sudo -u roostack", 999, env(), want},
		{"su, keeping root's", 999, env("XDG_RUNTIME_DIR", "/run/user/0", "DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/0/bus"), want},
		{"no bus", 999, env("XDG_RUNTIME_DIR", "/run/user/999"), want},
		{"the service", 999, env("XDG_RUNTIME_DIR", "/run/user/999", "DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/999/bus"), nil},
		{"root", 0, env(), nil},
		{"another user", 1000, env(), nil},
	} {
		if got := ownSession(c.uid, "999", c.getenv); !maps.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
