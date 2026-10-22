package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"roostack.cloud/roostack/internal/fsutil"
)

// fstab is the file mounts are recorded in; fstabNew is where the agent
// writes what it should become, and fstabPrevious keeps it as it was
// before the last change, for anyone fixing a server by hand (tests
// replace all three).
var (
	fstab         = "/etc/fstab"
	fstabNew      = "/etc/roostack/fstab.new"
	fstabPrevious = "/etc/roostack/fstab.previous"
)

// reloadSystemd has systemd re-read fstab (tests replace it).
var reloadSystemd = func() { exec.Command("systemctl", "daemon-reload").Run() }

// CommitFstabCommand is roostack-agent's argument for CommitFstab, which a
// short systemd job runs outside the agent's sandbox.
const CommitFstabCommand = "commit-fstab"

// commitFstab puts fstabNew in place of fstab. The agent's sandbox can't
// write /etc (roostack-agent.service), so a transient systemd unit does it,
// as OS updates are run (tests replace it).
var commitFstab = func() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	out, err := exec.Command("systemd-run", "--unit=roostack-fstab", "--wait", "--quiet", "--collect",
		"--", exe, CommitFstabCommand).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CommitFstab renames /etc/roostack/fstab.new over /etc/fstab and syncs
// /etc, so a power cut leaves the old file or the new one, never part of
// one. Run as root outside the agent's sandbox (commitFstab).
func CommitFstab() error {
	return renameSynced(fstabNew, fstab)
}

// renameSynced renames from over to, both on the same filesystem (a
// rename across filesystems fails rather than copying), then syncs to's
// folder so the rename itself is on disk.
func renameSynced(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// fstabMu serialises reading and changing fstab: the agent serves requests
// at the same time, and two edits at once would each start from the old
// file and one would be lost.
var fstabMu sync.Mutex

// readFstab returns fstab's contents.
func readFstab() (string, error) {
	fstabMu.Lock()
	defer fstabMu.Unlock()
	data, err := os.ReadFile(fstab)
	return string(data), err
}

// editFstab is the one way the agent changes fstab: edit gets its contents
// and returns what they should become, all under fstabMu (so edit must not
// read or edit fstab itself). If they changed, the new contents are written
// to fstabNew and synced, the old ones kept as fstabPrevious, and the new
// file renamed over fstab in one step (commitFstab); then systemd reloads,
// since it makes mount units from fstab. If anything fails, fstab is as it
// was (SPEC.md §6.1, "Editing /etc/fstab").
func editFstab(edit func(tab string) (string, error)) error {
	fstabMu.Lock()
	defer fstabMu.Unlock()
	data, err := os.ReadFile(fstab)
	if err != nil {
		return err
	}
	tab, err := edit(string(data))
	if err != nil || tab == string(data) {
		return err
	}
	if err := fsutil.WriteFile(fstabPrevious, data, 0o644); err != nil {
		return err
	}
	if err := fsutil.WriteFile(fstabNew, []byte(tab), 0o644); err != nil {
		return err
	}
	if err := commitFstab(); err != nil {
		os.Remove(fstabNew)
		return fmt.Errorf("replacing /etc/fstab: %w", err)
	}
	reloadSystemd()
	return nil
}

// Roostack's blocks in fstab: each line it adds has a comment above it, a
// tag saying what it's for, so the agent finds its own lines again and
// leaves everyone else's alone. A block is the tag and the line after it.
// The tags are made only here, and read only through fstabBlocks.

// tagPrefix starts every tag.
const tagPrefix = "# Added by Roostack: "

// A tag is what follows tagPrefix: what a block is for.
type tag string

// line is the tag's comment line in fstab.
func (t tag) line() string { return tagPrefix + string(t) }

// The tags, one for each kind of block.
func backupDriveTag(label string) tag { return tag("a backup drive (" + printable(label) + ")") }
func mediaDriveTag(mp, label string) tag {
	return tag("a media drive (" + printable(label) + ") at " + mp)
}
func libraryTag(name string) tag { return tag("media library " + name) }
func shareTag(name string) tag   { return tag("media library " + name + " (a share)") }
func viewTag(service, library string) tag {
	return tag(service + " reads media library " + library)
}
func atTag(service, library, name string) tag {
	return tag(service + " reads media library " + library + " as " + name)
}
func importTag(service string) tag { return tag("bringing data into " + service) }
func photoTag(service, who string) tag {
	return tag("migrating photos into " + service + " for " + who)
}

// Each tag's words before what varies, for reading them back.
const (
	backupDriveWords = "a backup drive"
	mediaDriveWords  = "a media drive"
	libraryWords     = "media library "
	shareWords       = " (a share)"
	readsWords       = " reads media library "
	asWords          = " as "
)

// fstabBlock is one of Roostack's blocks.
type fstabBlock struct {
	tag tag
	// at is the tag's line number (from 0); the block's line is the next.
	at int
	// fields are the block's line's fields, still escaped (none if the
	// tag is fstab's last line).
	fields []string
}

// fstabBlocks reads Roostack's blocks in fstab, in order. Every tag line
// starts one, whatever follows it.
func fstabBlocks(tab string) []fstabBlock {
	var out []fstabBlock
	lines := strings.Split(tab, "\n")
	for i, l := range lines {
		t, ok := strings.CutPrefix(l, tagPrefix)
		if !ok {
			continue
		}
		b := fstabBlock{tag: tag(t), at: i}
		if i+1 < len(lines) {
			b.fields = strings.Fields(lines[i+1])
		}
		out = append(out, b)
	}
	return out
}

// withBlock appends a block to fstab.
func withBlock(tab string, t tag, line string) string {
	if tab != "" && !strings.HasSuffix(tab, "\n") {
		tab += "\n"
	}
	return tab + t.line() + "\n" + line + "\n"
}

// removeBlock removes every block with this tag: the tag and its line.
func removeBlock(tab string, t tag) string {
	lines := strings.Split(tab, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if lines[i] == t.line() {
			i++ // and its line
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}
