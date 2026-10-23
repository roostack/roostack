package node

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/state"
)

// Moving a service between servers by copying its data directly (SPEC.md
// §10.10): the old server sends each data folder as a tar stream, the new
// one unpacks it next to where it goes, and when the copy is complete it
// takes its place. Files belong to container users, so both sides work
// inside `podman unshare`.

// Mover is what moving a service needs from a server.
type Mover interface {
	// DataDirs are where a service keeps data on this server.
	DataDirs(service string) []string
	// ExportData writes dir (an index into DataDirs) as a tar stream: all
	// of it, or only files changed since a time.
	ExportData(ctx context.Context, service string, dir int, since time.Time, w io.Writer) error
	// ListData lists dir's files and folders (relative paths).
	ListData(ctx context.Context, service string, dir int) ([]string, error)
	// ImportData unpacks a tar stream into dir's staging folder, dropping
	// the first strip path components (e.g. a backup's full paths).
	ImportData(ctx context.Context, service string, dir int, r io.Reader, strip int) error
	// PruneData removes from the staging folder what isn't in keep (files
	// deleted on the old server since the first copy).
	PruneData(ctx context.Context, service string, dir int, keep []string) error
	// CommitData moves the staging folders into place; data already there
	// is kept aside, for why (kept.BeforeMove or kept.BeforeRestore). It
	// returns where.
	CommitData(ctx context.Context, service string, why kept.Why) ([]string, error)
	// SetAsideData renames the service's folders after it moved away
	// (.moved-<note>-<time>), or, with the note FailedMove, what a move
	// that didn't finish copied here (.failed-move-<time>).
	SetAsideData(ctx context.Context, service, note string) ([]string, error)
	// StopService and StartService stop and start its containers: those
	// that run now (state.Output), so only its databases while it's
	// stopped or held (SPEC.md §6.9, "A restore that didn't finish").
	StopService(ctx context.Context, service string) error
	StartService(ctx context.Context, service string) error
	// ExportSecrets and ImportSecrets copy its Podman secrets.
	ExportSecrets(ctx context.Context, service string) (map[string][]byte, error)
	ImportSecrets(ctx context.Context, secrets map[string][]byte) error
	// DataSize is roughly how much data it has, in bytes.
	DataSize(ctx context.Context, service string) (int64, error)
	// LoadDumps stops the service, loads its databases from the dumps in
	// its data (after a restore or a move through a backup), and starts
	// what of it runs now again.
	LoadDumps(ctx context.Context, service string) error
	// KeptCopies lists the data set aside on this server (kept.go);
	// PutBackKept puts the copy a folder belongs to back in place of its
	// service's data, and RemoveKept deletes that copy for good.
	KeptCopies(ctx context.Context) ([]kept.Copy, error)
	PutBackKept(ctx context.Context, path string) error
	RemoveKept(ctx context.Context, path string) error
}

// Mover is this server, which moves its own services' data.
func (n *Local) Mover() Mover { return n }

// Launcher runs a command as the roostack user outside Roostack's sandbox
// (systemd-run --user), streaming stdin and stdout.
type Launcher func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd

const (
	staging     = ".incoming"
	timeLayout  = kept.TimeLayout
	safeService = "abcdefghijklmnopqrstuvwxyz0123456789-"
)

func (n *Local) DataDirs(service string) []string {
	return n.Lab.Paths.ServiceDataDirs(service)
}

func (n *Local) dataDir(service string, dir int) (string, error) {
	if !instance.ValidName(service) {
		return "", ErrNotFound
	}
	dirs := n.DataDirs(service)
	if dir < 0 || dir >= len(dirs) {
		return "", ErrNotFound
	}
	return dirs[dir], nil
}

// unshare runs a command inside podman unshare.
func (n *Local) unshare(ctx context.Context, args ...string) *exec.Cmd {
	return n.Launch(ctx, nil, "podman", append([]string{"unshare"}, args...)...)
}

func (n *Local) ExportData(ctx context.Context, service string, dir int, since time.Time, w io.Writer) error {
	d, err := n.dataDir(service, dir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
		return writeEmptyTar(w)
	}
	args := []string{"tar", "-C", d, "-cf", "-", "--numeric-owner"}
	if !since.IsZero() {
		args = append(args, "--newer-mtime=@"+strconv.FormatInt(since.Unix(), 10))
	}
	cmd := n.unshare(ctx, append(args, ".")...)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("reading %s: %w: %s", d, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// writeEmptyTar writes a tar stream with nothing in it (a service with no
// media folder).
func writeEmptyTar(w io.Writer) error {
	_, err := w.Write(make([]byte, 1024))
	return err
}

func (n *Local) ListData(ctx context.Context, service string, dir int) ([]string, error) {
	d, err := n.dataDir(service, dir)
	if err != nil {
		return nil, err
	}
	return n.list(ctx, d)
}

func (n *Local) list(ctx context.Context, d string) ([]string, error) {
	if _, err := os.Stat(d); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	out, err := n.unshare(ctx, "find", d, "-mindepth", "1", "-printf", "%P\\0").Output()
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", d, err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func (n *Local) ImportData(ctx context.Context, service string, dir int, r io.Reader, strip int) error {
	d, err := n.dataDir(service, dir)
	if err != nil {
		return err
	}
	in := d + staging
	if err := os.MkdirAll(in, 0o750); err != nil {
		return err
	}
	// Symbolic links left by an earlier pass of the copy, which tar would
	// follow (checkStream).
	have, err := n.unshare(ctx, "find", in, "-mindepth", "1", "-type", "l", "-printf", "%P\\0").Output()
	if err != nil {
		return fmt.Errorf("listing links in %s: %w", in, err)
	}
	links := map[string]bool{}
	for _, p := range strings.Split(string(have), "\x00") {
		if p != "" {
			links[p] = true
		}
	}
	args := []string{"tar", "-C", in, "-xpf", "-", "--numeric-owner"}
	if strip > 0 {
		args = append(args, "--strip-components="+strconv.Itoa(strip))
	}
	cmd := n.unshare(ctx, args...)
	pr, pw := io.Pipe()
	cmd.Stdin = pr
	checked := make(chan error, 1)
	go func() {
		err := checkStream(r, pw, strip, links)
		pw.CloseWithError(err)
		checked <- err
	}()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	pr.Close() // tar stopped: so does the check
	if cerr := <-checked; cerr != nil && !errors.Is(cerr, io.ErrClosedPipe) {
		return fmt.Errorf("unpacking into %s: %w", in, cerr)
	}
	if err != nil {
		return fmt.Errorf("unpacking into %s: %w: %s", in, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// checkStream passes a tar stream on to tar, member by member, refusing
// one that could reach outside the folder it's unpacked in: a name with
// "..", anything inside a symbolic link (one sent earlier in the stream,
// or in links: those already there from an earlier pass), a folder in
// place of a link, or a hard link to something inside one. GNU tar itself
// refuses ".." and delays links sent in the same stream, but follows a
// link already on disk (docs/ASSUMPTIONS.md L19, checked on VM1), and a
// copy sends its data more than once into the same folder. The stream
// stops at the member refused, which never reaches tar. It's read to its
// end, past tar's end-of-archive marker (readPadding). strip is tar's
// --strip-components; links is updated as the stream goes.
func checkStream(r io.Reader, w io.Writer, strip int, links map[string]bool) error {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(w)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if err := readPadding(r); err != nil {
				return err
			}
			return tw.Close()
		}
		if err != nil {
			return err
		}
		if err := checkMember(h, strip, links); err != nil {
			return err
		}
		// Written again as read: tar's format is chosen to fit, and a
		// sparse file is sent whole.
		h.Format = tar.FormatUnknown
		if h.Typeflag == tar.TypeGNUSparse {
			h.Typeflag = tar.TypeReg
		}
		for k := range h.PAXRecords {
			if strings.HasPrefix(k, "GNU.sparse.") {
				delete(h.PAXRecords, k)
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}

// readPadding reads what follows a tar stream's end-of-archive marker: GNU
// tar pads its stream to a whole record (10 KiB) with zeros. A sender
// across the lab API finishes only once all it sent has been read, so
// stopping at the marker would leave a copy waiting for ever. Anything but
// zeros is refused: tar would drop it, and it may be data.
func readPadding(r io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if slices.ContainsFunc(buf[:n], func(b byte) bool { return b != 0 }) {
			return errors.New("the data sent goes on after the end of its tar stream")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// checkMember checks one member of a stream for checkStream.
func checkMember(h *tar.Header, strip int, links map[string]bool) error {
	name, ok := strippedName(h.Name, strip)
	if !ok {
		return fmt.Errorf("the data sent has a file named %q, which would be outside the folder", h.Name)
	}
	if name == "" {
		return nil // tar skips it (too few components), or it's the folder itself
	}
	if l := linkAbove(name, links); l != "" {
		return fmt.Errorf("the data sent has %q inside the symbolic link %q, which could point anywhere", name, l)
	}
	switch h.Typeflag {
	case tar.TypeSymlink:
		links[name] = true
	case tar.TypeLink:
		// Checked as tar unpacks it, and as sent in case tar doesn't strip
		// it the same way.
		target, ok := strippedName(h.Linkname, strip)
		whole, _ := strippedName(h.Linkname, 0)
		if !ok {
			return fmt.Errorf("the data sent has a hard link to %q, which would be outside the folder", h.Linkname)
		}
		for _, t := range []string{target, whole} {
			if l := linkAbove(t, links); l != "" {
				return fmt.Errorf("the data sent has a hard link to %q inside the symbolic link %q, which could point anywhere", t, l)
			}
		}
		if links[target] || links[whole] {
			links[name] = true // another name for the link
		} else {
			delete(links, name)
		}
	case tar.TypeDir:
		if links[name] {
			return fmt.Errorf("the data sent has a folder %q in place of a symbolic link", name)
		}
	default:
		delete(links, name) // tar removes a link before writing a file there
	}
	return nil
}

// strippedName is a member's name as tar unpacks it: without leading
// slashes or its first strip components (as GNU tar counts them), and
// cleaned. "" if tar would skip it; false if it has a ".." component.
func strippedName(name string, strip int) (string, bool) {
	if slices.Contains(strings.Split(name, "/"), "..") {
		return "", false
	}
	p := strings.TrimLeft(name, "/")
	for range strip {
		_, rest, ok := strings.Cut(p, "/")
		if !ok {
			return "", true
		}
		p = strings.TrimLeft(rest, "/")
	}
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/"), true
}

// linkAbove returns the folder above name (or name's own path's parents)
// that is a symbolic link, or "".
func linkAbove(name string, links map[string]bool) string {
	for i := range len(name) {
		if name[i] == '/' && links[name[:i]] {
			return name[:i]
		}
	}
	return ""
}

func (n *Local) PruneData(ctx context.Context, service string, dir int, keep []string) error {
	d, err := n.dataDir(service, dir)
	if err != nil {
		return err
	}
	in := d + staging
	have, err := n.list(ctx, in)
	if err != nil {
		return err
	}
	var extra []string
	for _, p := range have {
		if !slices.Contains(keep, p) && !strings.Contains(p, "..") {
			extra = append(extra, p)
		}
	}
	if len(extra) == 0 {
		return nil
	}
	cmd := n.unshare(ctx, "sh", "-c", `cd "$1" && xargs -0 rm -rf --`, "sh", in)
	cmd.Stdin = strings.NewReader(strings.Join(extra, "\x00"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("removing deleted files: %w: %s", err, out)
	}
	return nil
}

func (n *Local) CommitData(ctx context.Context, service string, why kept.Why) ([]string, error) {
	if _, err := n.dataDir(service, 0); err != nil {
		return nil, err
	}
	if why != kept.BeforeRestore {
		why = kept.BeforeMove
	}
	var aside []string
	suffix := kept.Suffix(why, "", time.Now())
	for _, d := range n.DataDirs(service) {
		in := d + staging
		if _, err := os.Stat(in); err != nil {
			continue
		}
		if _, err := os.Stat(d); err == nil {
			if err := os.Rename(d, d+suffix); err != nil {
				return aside, err
			}
			aside = append(aside, d+suffix)
		}
		if err := os.Rename(in, d); err != nil {
			return aside, err
		}
	}
	return aside, nil
}

// FailedMove is SetAsideData's note for what a move that didn't finish
// copied to its new server (SPEC.md §10.10, kept.FailedMove).
const FailedMove = string(kept.FailedMove)

func (n *Local) SetAsideData(ctx context.Context, service, note string) ([]string, error) {
	if _, err := n.dataDir(service, 0); err != nil {
		return nil, err
	}
	note = strings.Map(func(r rune) rune {
		if strings.ContainsRune(safeService, r) {
			return r
		}
		return '-'
	}, strings.ToLower(note))
	var aside []string
	suffix := kept.Suffix(kept.MovedAway, note, time.Now())
	if note == FailedMove {
		suffix = kept.Suffix(kept.FailedMove, "", time.Now())
	}
	for _, d := range n.DataDirs(service) {
		if _, err := os.Stat(d); err != nil {
			continue
		}
		if err := os.Rename(d, d+suffix); err != nil {
			return aside, err
		}
		aside = append(aside, d+suffix)
	}
	return aside, nil
}

// output is a service's generated units and secrets on this server.
func (n *Local) output(service string) (*generate.Output, error) {
	s, ok := n.service(service)
	if !ok {
		return nil, ErrNotFound
	}
	return generate.Service(n.currentLab(), s.Package, s.Instance)
}

func (n *Local) StopService(ctx context.Context, service string) error {
	live, err := n.liveOutput(service)
	if err != nil {
		return err
	}
	return n.Reconciler.Systemd.Stop(ctx, live.Units...)
}

func (n *Local) StartService(ctx context.Context, service string) error {
	live, err := n.liveOutput(service)
	if err != nil {
		return err
	}
	return n.Reconciler.Systemd.Start(ctx, live.Units...)
}

func (n *Local) ExportSecrets(ctx context.Context, service string) (map[string][]byte, error) {
	out, err := n.output(service)
	if err != nil {
		return nil, err
	}
	secrets := map[string][]byte{}
	for _, s := range out.Secrets {
		v, err := n.Secrets.Read(ctx, s.Name)
		if err != nil {
			continue // e.g. an install-only secret, used and deleted
		}
		secrets[s.Name] = v
	}
	return secrets, nil
}

func (n *Local) ImportSecrets(ctx context.Context, secrets map[string][]byte) error {
	for name, v := range secrets {
		if !strings.HasPrefix(name, "roostack-") {
			return fmt.Errorf("unexpected secret %q", name)
		}
		n.Secrets.Remove(ctx, name) // replace any old one
		if err := n.Reconciler.Secrets.Create(ctx, name, v); err != nil {
			return err
		}
	}
	return nil
}

func (n *Local) DataSize(ctx context.Context, service string) (int64, error) {
	var dirs []string
	for _, d := range n.DataDirs(service) {
		if _, err := os.Stat(d); err == nil {
			dirs = append(dirs, d)
		}
	}
	return n.du(ctx, true, dirs...)
}

// du adds up the space the folders take on the disk (allocated blocks,
// as df counts them; 0 for none, or a folder that doesn't exist), at the
// lowest CPU and disk priority. unshared: in Podman's user namespace, for
// files owned by containers' users.
func (n *Local) du(ctx context.Context, unshared bool, dirs ...string) (int64, error) {
	var existing []string
	for _, d := range dirs {
		if _, err := os.Stat(d); err == nil {
			existing = append(existing, d)
		}
	}
	if len(existing) == 0 {
		return 0, nil
	}
	args := append([]string{"nice", "-n", "19", "ionice", "-c3", "du", "-s", "-B1", "--"}, existing...)
	var cmd *exec.Cmd
	switch {
	case unshared:
		cmd = n.unshare(ctx, args...)
	case n.Launch != nil:
		cmd = n.Launch(ctx, nil, args[0], args[1:]...)
	default:
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	}
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("du: %w", err)
	}
	var total int64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			v, err := strconv.ParseInt(f[0], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("du: %q", line)
			}
			total += v
		}
	}
	return total, nil
}

func (n *Local) LoadDumps(ctx context.Context, service string) error {
	out, err := n.output(service)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	live, err := n.liveOutput(service)
	if err != nil {
		return err
	}
	if err := n.Reconciler.Systemd.Stop(ctx, live.Units...); err != nil {
		return err
	}
	r := &backup.Restorer{Units: n.Reconciler.Systemd, Launch: n.Launch, RoostackBin: exe}
	if err := r.LoadDumps(ctx, out); err != nil {
		return err
	}
	return n.Reconciler.Systemd.Start(ctx, live.Units...)
}

// stateOutput is what runs of a service now (state.Output).
func stateOutput(n *Local, s state.Service) (*generate.Output, error) {
	return state.Output(n.currentLab(), s)
}
