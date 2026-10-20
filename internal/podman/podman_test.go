package podman

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeRun records what would have been run.
type fakeRun struct {
	stdin []byte
	args  []string
}

func (f *fakeRun) run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.stdin, f.args = stdin, args
	return nil, nil
}

func (f *fakeRun) runSplit(ctx context.Context, stdin []byte, args ...string) ([]byte, []byte, error) {
	out, err := f.run(ctx, stdin, args...)
	return out, nil, err
}

// Values from people (passwords, action inputs) go to podman only on
// stdin, never in its command line, where any process listing shows them.
func TestExecPassesEnvOnStdin(t *testing.T) {
	f := &fakeRun{}
	_, _, err := Exec(context.Background(), f.runSplit, "roostack-forgejo-app",
		map[string]string{"PASSWORD": "s3cret pass", "USER": "tyris"}, "forgejo", "admin", "user", "create")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--env-file", "/dev/stdin", "roostack-forgejo-app", "forgejo", "admin", "user", "create"}
	if !slices.Equal(f.args, want) {
		t.Errorf("args = %q", f.args)
	}
	if string(f.stdin) != "PASSWORD=s3cret pass\nUSER=tyris\n" {
		t.Errorf("env file = %q", f.stdin)
	}
	// A line break would start another variable in the env file.
	for _, bad := range []string{"a\nEVIL=1", "a\rb", "a\x00b"} {
		_, _, err := Exec(context.Background(), f.runSplit, "c", map[string]string{"X": bad}, "true")
		if !errors.Is(err, ErrBadEnvValue) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestSecretsValuesOnStdin(t *testing.T) {
	f := &fakeRun{}
	s := &Secrets{Run: f.run}
	ctx := context.Background()
	for _, op := range []func() error{
		func() error { return s.Create(ctx, "roostack-app-token", []byte("s3cret")) },
		func() error { return s.Replace(ctx, "roostack-app-token", []byte("s3cret")) },
	} {
		if err := op(); err != nil {
			t.Fatal(err)
		}
		if string(f.stdin) != "s3cret" || strings.Contains(strings.Join(f.args, " "), "s3cret") {
			t.Errorf("secret must go on stdin only: args %q", f.args)
		}
	}
}
