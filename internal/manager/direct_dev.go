//go:build devdirect

package manager

import (
	"net"

	"roostack.cloud/roostack/internal/labnet"
)

// A test build for dev VMs (SPEC.md §10.5, "Serving directly"): any
// address counts as one visitors reach a server at, and any home address
// as one on the controller's home network. Release builds never contain
// it.
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
	// VMs behind one NAT all have the same home address.
	homeAddrIn = func(addrs, _ []string) string {
		if len(addrs) > 0 {
			return addrs[0]
		}
		return ""
	}
}
