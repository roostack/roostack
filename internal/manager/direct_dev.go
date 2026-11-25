//go:build devdirect

package manager

import (
	"net"
	"os"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/labnet"
)

// A test build for dev VMs (SPEC.md §10.5, "Serving directly"), whose
// addresses are all private: the servers whose addresses on Roostack's
// network are listed in ROOSTACK_DEV_PUBLIC (comma-separated; all when
// it's unset) count as having a public address, the one the controller
// dials them at or saw them connect from, for serving directly and for
// being the lab's edge; and any home address counts as one on the
// controller's home network, as VMs behind one NAT share theirs. Release
// builds never contain it.
func init() {
	listed := strings.Split(os.Getenv("ROOSTACK_DEV_PUBLIC"), ",")
	any := func(p labnet.Peer) string {
		if a := publicAddr(p); a != "" {
			return a
		}
		if os.Getenv("ROOSTACK_DEV_PUBLIC") != "" && !slices.Contains(listed, p.Addr) {
			return ""
		}
		for _, ep := range []string{p.Dial, p.Seen} {
			if host, _, err := net.SplitHostPort(ep); err == nil {
				return host
			}
		}
		return ""
	}
	directAddr, edgeAddr = any, any
	homeAddrIn = func(addrs, _ []string) string {
		if len(addrs) > 0 {
			return addrs[0]
		}
		return ""
	}
}
