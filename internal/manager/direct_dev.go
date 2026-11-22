//go:build devdirect

package manager

import (
	"net"

	"roostack.cloud/roostack/internal/labnet"
)

// A test build for dev VMs (SPEC.md §10.5, "Serving directly"): any
// address counts as one visitors reach a server at. Release builds never
// contain it.
func init() {
	directAddr = func(p labnet.Peer) string {
		if a := publicAddr(p); a != "" {
			return a
		}
		for _, ep := range []string{p.Dial, p.Seen} {
			if host, _, err := net.SplitHostPort(ep); err == nil {
				return host
			}
		}
		return ""
	}
}
