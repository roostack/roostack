// Package maintenance is the maintenance coordinator (SPEC.md §3.2): it runs
// disruptive jobs (backups, restores, updates, upgrades) one at a time, and
// decides when the host may reboot.
package maintenance

import (
	"fmt"
	"time"
)

// RebootMode is the Owner's reboot preference.
type RebootMode string

const (
	// RebootAuto reboots in the maintenance window when needed (default).
	RebootAuto RebootMode = "auto"
	// RebootNotify never reboots automatically; the UI shows "reboot pending".
	RebootNotify RebootMode = "notify"
)

// Policy configures reboot behaviour.
type Policy struct {
	Mode RebootMode
	// WindowStart is the time of day the maintenance window opens (local).
	WindowStart time.Duration
	// WindowLength is how long the window stays open.
	WindowLength time.Duration
	// InterruptAfter is how long a reboot may wait for an interruptible job
	// (e.g. a multi-day first upload) before that job is paused.
	InterruptAfter time.Duration
}

// DefaultPolicy is the SPEC.md §3.2 default: reboot at 04:00, pause
// resumable jobs if a reboot has waited 24 hours.
func DefaultPolicy() Policy {
	return Policy{
		Mode:           RebootAuto,
		WindowStart:    4 * time.Hour,
		WindowLength:   2 * time.Hour,
		InterruptAfter: 24 * time.Hour,
	}
}

// InWindow reports whether t is inside the maintenance window. Windows may
// cross midnight.
func (p Policy) InWindow(t time.Time) bool {
	sinceMidnight := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute +
		time.Duration(t.Second())*time.Second
	end := p.WindowStart + p.WindowLength
	if end <= 24*time.Hour {
		return sinceMidnight >= p.WindowStart && sinceMidnight < end
	}
	return sinceMidnight >= p.WindowStart || sinceMidnight < end-24*time.Hour
}

// Action is what the coordinator should do about a pending reboot.
type Action int

const (
	// Wait: don't reboot yet (see Decision.Reason).
	Wait Action = iota
	// Reboot now.
	Reboot
	// InterruptAndReboot: pause the running job, which resumes after boot.
	InterruptAndReboot
)

// Running describes the job in progress, if any.
type Running struct {
	Name string
	// Interruptible is true only while the job can be safely paused and
	// resumed (e.g. uploading, not while services are stopped).
	Interruptible bool
	// Progress is 0-100, or -1 if unknown.
	Progress int
}

// Decision is the outcome of Decide, with a reason for people.
type Decision struct {
	Action Action
	Reason string
}

// Decide says what to do about a reboot that has been pending since
// pendingSince. It is pure so every rule can be tested with fixed times.
func Decide(p Policy, now, pendingSince time.Time, running *Running) Decision {
	switch {
	case pendingSince.IsZero():
		return Decision{Wait, "no reboot needed"}
	case p.Mode == RebootNotify:
		return Decision{Wait, "reboot pending: automatic reboots are off"}
	case !p.InWindow(now):
		return Decision{Wait, "reboot pending: waiting for the maintenance window"}
	case running == nil:
		return Decision{Reboot, "rebooting to finish installing updates"}
	case running.Interruptible && now.Sub(pendingSince) >= p.InterruptAfter:
		return Decision{InterruptAndReboot, fmt.Sprintf(
			"reboot has waited %s; pausing %s, which will resume after the reboot",
			roundHours(now.Sub(pendingSince)), running.Name)}
	}
	reason := "reboot pending: waiting for " + running.Name + " to finish"
	if running.Progress >= 0 {
		reason += fmt.Sprintf(" (%d%%)", running.Progress)
	}
	return Decision{Wait, reason}
}

func roundHours(d time.Duration) string {
	return fmt.Sprintf("%.0f hours", d.Hours())
}
