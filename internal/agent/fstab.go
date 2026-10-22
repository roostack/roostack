package agent

import (
	"os"
	"os/exec"
	"sync"
)

// fstab is the file mounts are recorded in (tests replace it).
var fstab = "/etc/fstab"

// reloadSystemd has systemd re-read fstab (tests replace it).
var reloadSystemd = func() { exec.Command("systemctl", "daemon-reload").Run() }

// fstabMu serialises reading and changing fstab: the agent serves requests
// at the same time, and two edits at once would each start from the old
// file and one would be lost.
var fstabMu sync.Mutex

// readFstab returns fstab's contents, never half-way through an edit.
func readFstab() (string, error) {
	fstabMu.Lock()
	defer fstabMu.Unlock()
	data, err := os.ReadFile(fstab)
	return string(data), err
}

// editFstab is the one way the agent changes fstab: edit gets its contents
// and returns what they should become, all under fstabMu (so edit must not
// read or edit fstab itself). If they changed, the file is written and
// synced to disk, and systemd reloads, since it makes mount units from
// fstab.
//
// The write is in place: the agent's sandbox can write /etc/fstab but not
// /etc (ReadWritePaths in roostack-agent.service), so it can't write a new
// file beside it and rename it over.
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
	if err := writeInPlace(fstab, []byte(tab)); err != nil {
		return err
	}
	reloadSystemd()
	return nil
}

// writeInPlace replaces a file's contents and syncs them to disk before
// returning.
func writeInPlace(file string, data []byte) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
