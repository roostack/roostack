package node

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/sleep"
)

// CaddyImageUnit downloads Roostack's Caddy image (generate.System).
const CaddyImageUnit = generate.CaddyUnit + "-image.service"

// How often the download's log and its unit's state are read, unless the
// node's imagePoll says otherwise.
const imagePoll = 2 * time.Second

// caddyDownloading reports whether Caddy's image is being downloaded.
func (n *Local) caddyDownloading(ctx context.Context) bool {
	if n.Units == nil {
		return false
	}
	st, err := n.Units.Status(ctx, CaddyImageUnit)
	return err == nil && len(st) == 1 && st[0].ActiveState == "activating"
}

// followCaddyImage shows the Caddy image's download, started in the
// background by an apply (reconcile.Plan.ImageUnits), as an
// activity.Build log: its output, copied from the log file the image unit
// writes (config.Paths.CaddyImageLog), until the unit stops. Without this,
// Caddy just looks stopped on a slow connection. Only one follower runs at
// a time.
func (n *Local) followCaddyImage(logFile string) {
	if n.Units == nil || n.Activity == nil {
		return
	}
	n.mu.Lock()
	if n.followingImage {
		n.mu.Unlock()
		return
	}
	n.followingImage = true
	n.mu.Unlock()

	act := n.Activity.Start(activity.Build, "Downloading Roostack's web proxy")
	act.Step("downloading %s", generate.CaddyImage)
	go func() {
		defer func() {
			n.mu.Lock()
			n.followingImage = false
			n.mu.Unlock()
		}()
		act.Finish(n.copyImageLog(context.Background(), logFile, act))
	}()
}

// clearImageLog empties the download's log before plan starts a new one,
// so what the follower reads afterwards is that download's: the unit
// truncates the log only once it runs, and until then an earlier one's log
// would look like the new one's, finished. A download already running
// keeps its log: it writes on where it was, and restarted by plan it
// truncates the log itself (copyImageLog notices).
func (n *Local) clearImageLog(ctx context.Context, plan *reconcile.Plan, logFile string) {
	if !slices.Contains(plan.ImageUnits, CaddyImageUnit) || n.caddyDownloading(ctx) {
		return
	}
	if err := os.Truncate(logFile, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		n.Log.Error("emptying the Caddy image's log", "err", err)
	}
}

// caddyImageFailed is the reason Caddy's image didn't download, for the
// Owner, from Podman's last "Error: …" line in its log, or "" if it
// didn't fail. No local build stands in for it (SPEC.md §6.5).
func (n *Local) caddyImageFailed(ctx context.Context, logFile string) string {
	if n.Units == nil {
		return ""
	}
	if st, err := n.Units.Status(ctx, CaddyImageUnit); err != nil || len(st) != 1 || st[0].ActiveState != "failed" {
		return ""
	}
	return imageFailure(logFile)
}

// imageFailure says why the download in logFile failed.
func imageFailure(logFile string) string {
	why := "its log on the Services page says why"
	data, _ := os.ReadFile(logFile)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if e, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "Error: "); ok {
			why = "Podman said: " + e
			break
		}
	}
	msg := fmt.Sprintf("Roostack couldn't download its web proxy's image, so Caddy can't start until it does: %s. Roostack will try again the next time it applies a change or restarts, so check that this server can reach ghcr.io.", why)
	// Podman's reason usually names the image already.
	if !strings.Contains(why, generate.CaddyImage) {
		msg += " (Image: " + generate.CaddyImage + ")"
	}
	return msg
}

// copyImageLog copies new lines of the download's log to w until the
// image unit has finished, and reports whether it failed. The unit is a
// oneshot that stays active once it has downloaded the image, goes to
// failed when it can't, and is skipped (inactive, with the result
// exec-condition) when the image was already here. Inactive before it has
// started (a background start is queued) means waiting, for a while.
func (n *Local) copyImageLog(ctx context.Context, logFile string, w io.Writer) error {
	var offset int64
	var partial []byte
	copyNew := func() {
		f, err := os.Open(logFile)
		if err != nil {
			return // not written yet
		}
		defer f.Close()
		if st, err := f.Stat(); err == nil && st.Size() < offset {
			offset, partial = 0, nil // a new build truncated it
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return
		}
		data, _ := io.ReadAll(f)
		offset += int64(len(data))
		data = append(partial, data...)
		// Only whole lines; the rest waits for the next read.
		i := bytes.LastIndexByte(data, '\n')
		if i < 0 {
			partial = data
			return
		}
		w.Write(data[:i+1])
		partial = append([]byte(nil), data[i+1:]...)
	}
	poll := cmp.Or(n.imagePoll, imagePoll)
	started := false
	for waited := time.Duration(0); ; waited += poll {
		copyNew()
		st, err := n.Units.Status(ctx, CaddyImageUnit)
		if err == nil && len(st) == 1 {
			switch st[0].ActiveState {
			case "activating":
				started = true
			case "failed":
				copyNew()
				return errors.New(imageFailure(logFile))
			case "active":
				copyNew()
				return nil
			default: // inactive: done, or not started yet
				// Skipped: the image was already here.
				if st[0].Result == "exec-condition" {
					copyNew()
					return nil
				}
				// A download too quick to see running has still written its log.
				if started || offset > 0 || len(partial) > 0 {
					copyNew()
					return nil
				}
				if waited > time.Minute {
					return errors.New("the download didn't start")
				}
			}
		}
		if !sleep.For(ctx, poll) {
			return ctx.Err()
		}
	}
}
