//go:build !linux

package agent

import "net"

// The agent runs only on Linux. Elsewhere (a developer's Mac) the package
// builds so that its types can be imported, and every caller is refused:
// with no peer credentials, checkCaller allows no one.

// peerCred finds no caller on systems other than Linux.
func peerCred(net.Conn) (uid uint32, pid int32, ok bool) {
	return 0, 0, false
}

// peerInHostNS is false on systems other than Linux.
func peerInHostNS(net.Conn, int32) bool {
	return false
}
