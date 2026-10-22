package podman

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// Launch starts a command as the roostack user, outside Roostack's
// sandbox, like everything podman does (SPEC.md §6.1). Tests replace it.
type Launch func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd

// NoImageForArch is a Pull that failed because the image has no build for
// this server's kind of processor (ARM64-PLAN.md 1.3). Its message is for
// the Owner, in place of Podman's "no image found in image index".
type NoImageForArch struct {
	Image string
	Arch  string // Go's name for the processor, e.g. arm64
}

func (e *NoImageForArch) Error() string {
	return fmt.Sprintf("This service has no version for this server's kind of processor (%s), so it can't run here. (Image: %s)", e.Arch, e.Image)
}

// Pull downloads an image, unless it's already here, writing podman's
// output to w as it comes (SPEC.md §6.3.3).
func Pull(ctx context.Context, launch Launch, image string, w io.Writer) error {
	if launch(ctx, nil, "podman", "image", "exists", image).Run() == nil {
		fmt.Fprintf(w, "Already downloaded.\n")
		return nil
	}
	cmd := launch(ctx, nil, "podman", "pull", image)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.MultiWriter(w, &out), io.MultiWriter(w, &out)
	return pullError(cmd.Run(), out.String(), image, runtime.GOARCH)
}

// pullError says why a pull failed, from podman's output: an image with
// no build for arch is a *NoImageForArch, and otherwise podman's own
// "Error: …" line says why, rather than only its exit status (the
// install's notice shows it).
func pullError(err error, out, image, arch string) error {
	if err == nil {
		return nil
	}
	// Podman 5.4: "no image found in image index for architecture …";
	// older versions said "manifest list" (docs/ASSUMPTIONS.md R3).
	if strings.Contains(out, "no image found in") {
		return &NoImageForArch{Image: image, Arch: arch}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if why, ok := strings.CutPrefix(strings.TrimSpace(lines[len(lines)-1]), "Error: "); ok {
		return fmt.Errorf("%w: %s", err, why)
	}
	return err
}
