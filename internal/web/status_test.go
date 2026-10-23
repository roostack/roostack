package web

import "testing"

// What the controller knows comes before the server's report, a gone
// server before everything.
func TestServiceStatus(t *testing.T) {
	for _, c := range []struct {
		facts ServiceFacts
		want  string
	}{
		{ServiceFacts{Reported: StatusUnknown}, StatusUnknown},
		{ServiceFacts{Reported: "running"}, "running"},
		{ServiceFacts{Reported: "running", Stopped: true}, StatusRegistryStop},
		{ServiceFacts{Reported: "running", Stopped: true, Updating: true}, StatusUpdating},
		{ServiceFacts{Reported: "stopped", AwaitingData: true, Updating: true}, StatusAwaitingData},
		{ServiceFacts{Reported: "running", AwaitingData: true, Switching: true}, StatusSwitching},
		{ServiceFacts{Reported: "stopped", Switching: true, Held: true}, StatusHeld},
		{ServiceFacts{Reported: StatusUnknown, Held: true, Waiting: true, Stopped: true}, StatusWaiting},
	} {
		if got := ServiceStatus(c.facts); got != c.want {
			t.Errorf("%+v: %q, want %q", c.facts, got, c.want)
		}
	}
}
