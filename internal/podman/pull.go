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
	// One writer for both, so exec gives podman one pipe for its output
	// and its errors: the lines arrive in the order podman wrote them,
	// and only one goroutine writes to w and out. With two writers, the
	// "Error: …" line could come before the progress it followed, and the
	// reason was lost.
	both := io.MultiWriter(w, &out)
	cmd.Stdout, cmd.Stderr = both, both
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
	// The last "Error: …" line, wherever it is: podman may print more
	// after it.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if why, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "Error: "); ok {
			return fmt.Errorf("%w: %s", err, why)
		}
	}
	return err
}
