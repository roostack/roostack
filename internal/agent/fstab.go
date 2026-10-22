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
