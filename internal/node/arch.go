package node

import "runtime"

// Processor is a server that says what kind of processor it has (Local
// and Remote), so the controller checks a service has a build for the
// server it's placed on, in a lab mixing PCs and Raspberry Pis (SPEC.md
// §8.3.3, ARM64-PLAN.md 4.1).
type Processor interface {
	// Arch is the processor as Go names it (GOARCH: amd64, arm64), or ""
	// while it isn't known (a server that doesn't answer).
	Arch() string
}

var (
	_ Processor = (*Local)(nil)
	_ Processor = (*Remote)(nil)
)

// Arch is this server's processor: the one Roostack was built for.
func (n *Local) Arch() string { return runtime.GOARCH }

// Arch is the server's processor, from its status (WireStatus.Arch).
func (r *Remote) Arch() string {
	st, err := r.wireStatus()
	if err != nil {
		return ""
	}
	return st.Arch
}
