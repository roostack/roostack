package web

// A service's status, as the dashboard shows it: what its server reports
// (systemd's view: running, starting, stopped, failed), unless something
// the controller knows comes first.

// The statuses the controller decides.
const (
	// StatusUnknown is a service whose server didn't say how it is.
	StatusUnknown = "unknown"
	// StatusFailed is a service that stopped working, or that Roostack
	// couldn't work out the files for.
	StatusFailed = "failed"
	// StatusRegistryStop is a service Roostack stopped because the registry
	// withdrew its version as dangerous (SPEC.md §8.3.4).
	StatusRegistryStop = "registry-stop"
	// StatusUpdating is a service updating to a newer version (SPEC.md §7).
	StatusUpdating = "updating"
	// StatusAwaitingData is a service installed to wait for its data, and
	// StatusSwitching one being switched to it (SPEC.md §8.4).
	StatusAwaitingData = "awaiting-data"
	StatusSwitching    = "switching"
	// StatusHeld is a service held stopped after a restore (or a move) that
	// didn't finish (SPEC.md §6.9).
	StatusHeld = "held"
	// StatusWaiting is a service waiting to be restored: its server is gone
	// (SPEC.md §10.10).
	StatusWaiting = "waiting"
)

// ServiceFacts are what decides a service's status besides its server's
// report.
type ServiceFacts struct {
	// Reported is what its server says (StatusUnknown if it didn't).
	Reported string
	// Stopped: the registry withdrew its version, and Roostack stopped it.
	Stopped bool
	// Updating, AwaitingData, Switching, Held and Waiting are the
	// statuses of the same names.
	Updating, AwaitingData, Switching, Held, Waiting bool
}

// ServiceStatus is a service's status from f: each later one in this
// list wins over those before it, so a service whose server is gone is
// waiting, whatever else is true of it.
func ServiceStatus(f ServiceFacts) string {
	status := f.Reported
	for _, s := range []struct {
		on     bool
		status string
	}{
		{f.Stopped, StatusRegistryStop},
		{f.Updating, StatusUpdating},
		{f.AwaitingData, StatusAwaitingData},
		{f.Switching, StatusSwitching},
		{f.Held, StatusHeld},
		{f.Waiting, StatusWaiting},
	} {
		if s.on {
			status = s.status
		}
	}
	return status
}
