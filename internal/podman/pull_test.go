package podman

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// fakePodman runs a shell script for each podman command, by its
// arguments before the image ("image exists", "pull").
func fakePodman(scripts map[string]string) Launch {
	return func(ctx context.Context, _ []string, _ string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", scripts[strings.Join(args[:len(args)-1], " ")])
	}
}

func TestPull(t *testing.T) {
	const image = "docker.io/library/caddy:2"
	const noArch = `Trying to pull docker.io/library/caddy:2...
Error: choosing an image from manifest list docker://docker.io/library/caddy:2: no image found in image index for architecture "arm64", variant "v8", OS "linux"`
	for _, c := range []struct {
		name, exists, pull string
		out                string
		err                string // "" for none; "arch" for a *NoImageForArch
	}{
		{name: "already here", exists: "exit 0", out: "Already downloaded.\n"},
		{name: "downloaded", exists: "exit 1", pull: "echo 'Copying blob sha256:1'; echo 'Writing manifest'",
			out: "Copying blob sha256:1\nWriting manifest\n"},
		{name: "no build for this processor", exists: "exit 1", pull: "echo '" + noArch + "' >&2; exit 125",
			out: noArch + "\n", err: "arch"},
		{name: "podman's reason", exists: "exit 1",
			pull: "echo 'Trying to pull x...'; echo 'Error: initializing source docker://x: reading manifest 2: manifest unknown' >&2; exit 125",
			out:  "Trying to pull x...\nError: initializing source docker://x: reading manifest 2: manifest unknown\n",
			err:  "exit status 125: initializing source docker://x: reading manifest 2: manifest unknown"},
		{name: "no reason given", exists: "exit 1", pull: "echo 'killed'; exit 137", out: "killed\n", err: "exit status 137"},
	} {
		var out strings.Builder
		err := Pull(t.Context(), fakePodman(map[string]string{"image exists": c.exists, "pull": c.pull}), image, &out)
		if out.String() != c.out {
			t.Errorf("%s: output %q, want %q", c.name, out.String(), c.out)
		}
		var na *NoImageForArch
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.err == "arch" && (!errors.As(err, &na) || na.Image != image || na.Arch != runtime.GOARCH):
			t.Errorf("%s: %v, want a NoImageForArch", c.name, err)
		case c.err != "" && c.err != "arch" && (err == nil || err.Error() != c.err):
			t.Errorf("%s: %v, want %q", c.name, err, c.err)
		}
	}
}
