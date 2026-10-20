// Package activity keeps a live log of operations that act on the host
// (OS updates, backups, applying service changes, ...), so the UI can
// show a one-line status to everyone and the details to those who want
// them.
//
// Logs live in memory: the latest run of each kind is kept. When one
// finishes, Finished (if set) is called with it, so it can be kept as
// history (SPEC.md §6.14); a routine run (nothing changed) isn't.
package activity

import (
	"cmp"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Kinds of activity, one log each.
const (
	OSUpdates = "os-updates"
	Backup    = "backup"
	Apply     = "apply" // applying service changes
	Setup     = "setup" // a new service's install hooks
	Restore   = "restore"
	// Move: moving a service to another server (SPEC.md §10.10).
	Move = "move"
	// ServiceUpdate is updating a service to a newer version (SPEC.md §7).
	ServiceUpdate = "service-update"
	// BackupSetup: creating a server's backup repository (SPEC.md §10.10).
	BackupSetup = "backup-setup"
	// BackupKey: giving Roostack a new key for an online storage account,
	// and each server using it its new credentials (SPEC.md §10.10).
	BackupKey = "backup-key"
	// BackupRollback: checking what rolling a server's backups at online
	// storage back to an earlier time changes, then doing it (SPEC.md
	// §10.10).
	BackupRollback = "backup-rollback"
	// LabRestore is rebuilding a lab from its backups on a new server.
	LabRestore = "lab-restore"
	// Build is building Roostack's Caddy image in the background.
	Build = "build"
	// Import is bringing a service's data in from the person's own copy
	// (SPEC.md §8.4): one log per service (ImportFor).
	Import = "import"
	// LibraryRestart is restarting a service whose library, bound at a
	// path of its own, came back (SPEC.md §8.4).
	LibraryRestart = "library-restart"
)

// BackupSetupFor is the BackupSetup log of one server: the controller
// sets up every server's backups, and keeps a log for each.
func BackupSetupFor(serverID string) string { return BackupSetup + "." + serverID }

// ImportFor is the Import log of one service.
func ImportFor(service string) string { return Import + "." + service }

// BackupKeyFor is the BackupKey log of one account (its provider ID).
func BackupKeyFor(providerID string) string { return BackupKey + "." + providerID }

// BackupRollbackFor is the BackupRollback log of one server's backups at
// one place (a reference such as "provider:1").
func BackupRollbackFor(serverID, ref string) string {
	return BackupRollback + "." + serverID + "." + ref
}

// maxLines is how many lines a log keeps; older ones are dropped.
const maxLines = 2000

// Registry holds the latest log of each kind.
type Registry struct {
	mu   sync.Mutex
	logs map[string]*Log
	now  func() time.Time
	// Finished is called with each log as it finishes, unless it was
	// routine (Log.Routine); optional. Set it before logs start.
	Finished func(kind string, s Snapshot)
}

// New returns an empty registry.
func New() *Registry { return &Registry{logs: map[string]*Log{}, now: time.Now} }

// Start begins a new log for kind, replacing the previous one.
func (r *Registry) Start(kind, title string) *Log {
	if r == nil {
		return nil // logging is optional: a nil Log discards
	}
	l := &Log{title: title, started: r.now(), now: r.now, kind: kind, registry: r}
	r.mu.Lock()
	r.logs[kind] = l
	r.mu.Unlock()
	return l
}

// Latest returns a snapshot of kind's latest log, or false if none.
func (r *Registry) Latest(kind string) (Snapshot, bool) {
	if r == nil {
		return Snapshot{}, false
	}
	r.mu.Lock()
	l := r.logs[kind]
	r.mu.Unlock()
	if l == nil {
		return Snapshot{}, false
	}
	return l.Snapshot(), true
}

// Log is one operation's log. A nil *Log discards everything, so callers
// don't need to check.
type Log struct {
	mu       sync.Mutex
	title    string
	status   string
	started  time.Time
	finished time.Time
	err      string
	lines    []string
	dropped  int
	partial  string // an incomplete line from Write
	updated  time.Time
	now      func() time.Time
	kind     string
	registry *Registry
	routine  bool
}

// Routine marks a run that changed nothing (e.g. an apply with nothing to
// do): it's shown as the latest, but not kept as history.
func (l *Log) Routine() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.routine = true
	l.mu.Unlock()
}

// Snapshot is a copy of a log for display.
type Snapshot struct {
	Title    string
	Status   string // the latest step, for the one-line summary
	Started  time.Time
	Finished time.Time // zero while running
	Err      string
	Lines    []string
	Dropped  int // lines no longer kept
	// Updated is when it last had output: a long step whose log still
	// moves hasn't hung.
	Updated time.Time
}

// Running reports whether the operation hasn't finished.
func (s Snapshot) Running() bool { return s.Finished.IsZero() }

// Step records a step people should see in the status line, and logs it.
func (l *Log) Step(format string, args ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.status = msg
	l.add("==> " + msg)
	l.mu.Unlock()
}

// Printf adds a detail line.
func (l *Log) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.add(fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

// Write adds command output, line by line.
func (l *Log) Write(p []byte) (int, error) {
	if l == nil {
		return len(p), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(p) > 0 && l.now != nil {
		l.updated = l.now()
	}
	text := l.partial + string(p)
	lines := strings.Split(text, "\n")
	l.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		// Lines may end in \r\n (dpkg's do): the \r isn't a redraw.
		// Otherwise, progress bars redraw with \r: keep the last state.
		// Cutting at a trailing \r left every dpkg line empty.
		line = strings.TrimRight(line, "\r")
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		// Commands can announce steps the same way Step logs them.
		if step, ok := strings.CutPrefix(line, "==> "); ok {
			l.status = step
		}
		l.add(line)
	}
	return len(p), nil
}

// Finish ends the log; err is nil on success.
func (l *Log) Finish(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.partial != "" {
		l.add(l.partial)
		l.partial = ""
	}
	l.finished = l.now()
	if err != nil {
		l.err = err.Error()
		l.add("==> Failed: " + l.err)
	} else {
		l.add("==> Done")
	}
	if l.registry != nil && l.registry.Finished != nil && (!l.routine || err != nil) {
		snap := l.snapshotLocked()
		go l.registry.Finished(l.kind, snap)
	}
}

func (l *Log) add(line string) {
	if l.now != nil {
		l.updated = l.now()
	}
	l.lines = append(l.lines, line)
	if over := len(l.lines) - maxLines; over > 0 {
		l.lines = append(l.lines[:0:0], l.lines[over:]...)
		l.dropped += over
	}
}

// Snapshot copies the log.
func (l *Log) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked()
}

func (l *Log) snapshotLocked() Snapshot {
	lines := append([]string(nil), l.lines...)
	if l.partial != "" {
		lines = append(lines, l.partial)
	}
	return Snapshot{Title: l.title, Status: l.status, Started: l.started, Finished: l.finished,
		Err: l.err, Lines: lines, Dropped: l.dropped, Updated: cmp.Or(l.updated, l.started)}
}
