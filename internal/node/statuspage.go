package node

import "time"

// The public status page as this server's Caddy serves it (SPEC.md
// §6.15), for its card while it's being turned on or off: the switch
// only saves lab.yaml and starts an apply, which may wait behind another
// (an install's download), and until that apply has written Caddy's
// config, status.<domain> answers "nobody roosts here".

// StatusPageApplied is the status page as the last successful apply left
// it, and how applying is going.
type StatusPageApplied struct {
	// Known: an apply has succeeded since Roostack started, so On is
	// what Caddy serves.
	Known bool
	On    bool
	// At is when an apply last turned it on or off, zero if none has since
	// Roostack started.
	At time.Time
	// Running: an apply is running or waiting to; LastErr is why the last
	// one failed, "" if it didn't.
	Running bool
	LastErr string
}

// noteStatusPage records, after a successful apply, whether it left the
// status page on; the first since Roostack started only learns how
// things are.
func (n *Local) noteStatusPage(a Assignment) {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := &n.statusApplied
	if st.Known && st.On != a.Lab.StatusPage {
		st.At = time.Now()
	}
	st.Known, st.On = true, a.Lab.StatusPage
}

// StatusPageApplied is the status page as the last successful apply left
// it, and whether an apply is running or failed.
func (n *Local) StatusPageApplied() StatusPageApplied {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.statusApplied
	st.Running = n.status.Running
	if n.status.LastErr != nil {
		st.LastErr = n.status.LastErr.Error()
	}
	return st
}
