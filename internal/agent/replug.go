package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/words"
)

// Erasing a USB drive with something on it waits for a person to unplug it
// and plug it back in (SPEC.md §6.1): the roostack user asks, but only
// someone at the server can take a drive out, so a taken-over Roostack
// can't erase it by itself. The agent watches /sys/block for the drive to
// go and for the same drive (by identity, whatever name it comes back
// under) to come back within ReplugWindow, then erases it itself: nothing
// the roostack user sends afterwards is needed or trusted. The page shows
// where it's got to through Drives (Drive.Erase).

// ReplugWindow is how long a person has to unplug the drive and plug it
// back in once asked.
const ReplugWindow = 2 * time.Minute

// replugKept is how long a finished or expired erase stays on the drive
// list, so the page can say how it went.
const replugKept = 10 * time.Minute

// replugSettle is how long a drive that has just appeared is looked at
// again while udev fills in what it says about itself.
const replugSettle = 10 * time.Second

// replugWait is one erase waiting for its drive to be plugged back in.
type replugWait struct {
	host   LinuxHost
	drive  Drive // as it was listed when asked
	label  string
	name   string // its name in /sys/block when asked
	status EraseStatus
	ended  time.Time // when it finished or expired
}

// replugWatch keeps the waiting erases, and watches /sys/block while any
// is waiting.
type replugWatch struct {
	mu      sync.Mutex
	waits   map[string]*replugWait // by the drive's identity
	polling bool
	// appeared are the names that have turned up in /sys/block since the
	// last look but whose identity hasn't matched yet, and since when.
	appeared map[string]time.Time
	last     []string // the names at the last look

	// interval is how often /sys/block is looked at (0: only by tick, in
	// tests); blocks lists it, identify says which disk a name is (its
	// identity and device path), and erase erases a drive once it's back
	// (nil: eraseReplugged).
	interval time.Duration
	blocks   func() []string
	identify func(name string) (ident, path string)
	erase    func(w *replugWait) error
}

var replugs = &replugWatch{interval: 500 * time.Millisecond, blocks: sysBlocks, identify: identifyBlock}

// ask starts waiting for a drive to be unplugged and plugged back in
// (or keeps the wait already started), and says until when.
func (r *replugWatch) ask(h LinuxHost, d Drive, label string, now time.Time) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.waits == nil {
		r.waits = map[string]*replugWait{}
	}
	if w := r.waits[d.ident]; w != nil && w.ended.IsZero() {
		return w.status.Until
	}
	name := filepath.Base(d.Disk)
	r.waits[d.ident] = &replugWait{host: h, drive: d, label: label, name: name, status: EraseStatus{Until: now.Add(ReplugWindow)}}
	if r.last == nil {
		r.last = r.blocks()
	}
	if !r.polling && r.interval > 0 {
		r.polling = true
		go r.loop()
	}
	return now.Add(ReplugWindow)
}

func (r *replugWatch) loop() {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for now := range t.C {
		if !r.tick(now) {
			return
		}
	}
}

// tick looks at /sys/block once and moves each wait on. It reports whether
// any is still waiting (otherwise the watching stops).
func (r *replugWatch) tick(now time.Time) bool {
	names := r.blocks()
	r.mu.Lock()
	if r.appeared == nil {
		r.appeared = map[string]time.Time{}
	}
	for _, n := range names {
		if !slices.Contains(r.last, n) {
			r.appeared[n] = now
		}
	}
	for n, since := range r.appeared {
		if now.Sub(since) > replugSettle || !slices.Contains(names, n) {
			delete(r.appeared, n)
		}
	}
	r.last = names
	var looking []*replugWait
	for _, w := range r.waits {
		switch {
		case !w.ended.IsZero() || w.status.Erasing:
		case now.After(w.status.Until):
			w.status.Expired, w.ended = true, now
		case !w.status.Unplugged:
			w.status.Unplugged = !slices.Contains(names, w.name)
		default:
			looking = append(looking, w)
		}
	}
	candidates := make([]string, 0, len(r.appeared))
	for n := range r.appeared {
		candidates = append(candidates, n)
	}
	r.mu.Unlock()

	// The drive is back once a name that has just appeared is the same
	// disk (lsblk runs without the lock).
	for _, n := range candidates {
		ident, path := r.identify(n)
		for _, w := range looking {
			if ident == "" || ident != w.drive.ident {
				continue
			}
			r.mu.Lock()
			if !w.status.Erasing {
				w.status.Erasing = true
				w.drive.Disk = path
				delete(r.appeared, n)
				go r.finish(w)
			}
			r.mu.Unlock()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.waits {
		if w.ended.IsZero() {
			return true
		}
	}
	r.polling = false
	return false
}

// finish erases a drive that's been plugged back in, and keeps how it went.
func (r *replugWatch) finish(w *replugWait) {
	erase := r.erase
	if erase == nil {
		erase = eraseReplugged
	}
	err := erase(w)
	r.mu.Lock()
	defer r.mu.Unlock()
	w.status.Erasing, w.ended = false, time.Now()
	if err != nil {
		w.status.Failed = words.FirstLine(err.Error())
	} else {
		w.status.Done = true
	}
}

// annotate puts each wait on its drive in a list of drives, and a drive
// that's unplugged while it waits back on the list, so the page can say
// where its erase has got to. Waits that ended a while ago are forgotten.
func (r *replugWatch) annotate(drives []Drive) []Drive {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.waits {
		if !w.ended.IsZero() && time.Since(w.ended) > replugKept {
			delete(r.waits, id)
		}
	}
	for id, w := range r.waits {
		found := false
		for i := range drives {
			if drives[i].ident == id && drives[i].Kind != "network" && drives[i].Kind != "system" {
				st := w.status
				drives[i].Erase, found = &st, true
			}
		}
		if !found && w.ended.IsZero() {
			st := w.status
			drives = append(drives, Drive{ID: "erase:" + w.name, Kind: w.drive.Kind, Name: w.drive.Name, Model: w.drive.Model,
				Size: w.drive.Size, Free: -1, Holds: w.drive.Holds, Erase: &st, ident: id})
		}
	}
	return drives
}

// eraseReplugged erases a drive that's back, if it's still one Roostack
// may erase (Erasable: not mounted or added to /etc/fstab meanwhile).
func eraseReplugged(w *replugWait) error {
	drives, err := w.host.Drives()
	if err != nil {
		return err
	}
	for _, d := range drives {
		if d.ident == w.drive.ident && d.Disk == w.drive.Disk && d.Erasable {
			return eraseDisk(d.Disk, w.label)
		}
	}
	return refuse("it can't be erased any more: it's mounted, in /etc/fstab, or has a filesystem backups can use")
}

// sysBlocks are the block devices' names in /sys/block.
func sysBlocks() []string {
	entries, _ := os.ReadDir("/sys/block")
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// identifyBlock is a block device's identity and path, as lsblk sees it
// ("" while udev hasn't filled it in, or for anything that isn't a disk).
func identifyBlock(name string) (ident, path string) {
	if name == "" || mountName.MatchString(name) {
		return "", "" // anything but letters, digits, - and _ isn't a disk's name
	}
	out, err := exec.Command("lsblk", "--json", "--bytes", "--nodeps", "-o", lsblkColumns, "/dev/"+name).Output()
	if err != nil {
		return "", ""
	}
	var l struct {
		Blockdevices []lsblkDevice `json:"blockdevices"`
	}
	if json.Unmarshal(out, &l) != nil || len(l.Blockdevices) != 1 || l.Blockdevices[0].Type != "disk" {
		return "", ""
	}
	return l.Blockdevices[0].identity(), l.Blockdevices[0].Path
}
