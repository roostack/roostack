package agent

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// peerCred returns the UID and PID of the process on the other end of a
// Unix socket (when it connected).
func peerCred(c net.Conn) (uid uint32, pid int32, ok bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || credErr != nil {
		return 0, 0, false
	}
	return cred.Uid, cred.Pid, true
}

// Linux's numbers for what Go's syscall package doesn't name (the same on
// amd64 and arm64).
const (
	soPeerPidfd        = 77  // SO_PEERPIDFD, Linux 6.5
	sysPidfdSendSignal = 424 // pidfd_send_signal
)

// peerInHostNS reports whether the process that connected is in the
// host's own user namespace. A rootless container's root (or a keep-id
// user) is the roostack user to SO_PEERCRED, so a container given the
// socket in a bind mount could otherwise call the agent (docs/ASSUMPTIONS.md
// S6). Only the host's namespace maps every UID to itself, which no
// unprivileged process can arrange. The process is found through a pidfd
// (SO_PEERPIDFD), and checked to be still alive after its uid_map is read,
// so a PID reused meanwhile isn't mistaken for it. Without SO_PEERPIDFD
// (kernels before 6.5), it falls back to the PID from SO_PEERCRED.
func peerInHostNS(c net.Conn, pid int32) bool {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	pidfd := -1
	raw.Control(func(fd uintptr) {
		if v, err := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, soPeerPidfd); err == nil {
			pidfd = v
		}
	})
	if pidfd >= 0 {
		defer syscall.Close(pidfd)
		info, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", pidfd))
		if err != nil {
			return false
		}
		if pid = fdinfoPid(string(info)); pid <= 0 {
			return false
		}
	}
	uidMap, err := os.ReadFile(fmt.Sprintf("/proc/%d/uid_map", pid))
	if err != nil || !hostUIDMap(string(uidMap)) {
		return false
	}
	if pidfd >= 0 {
		// Signal 0 only checks. EPERM (no CAP_KILL to signal another user)
		// still means it's there; ESRCH that it's gone, and the PID may be
		// someone else's now.
		if _, _, errno := syscall.Syscall6(sysPidfdSendSignal, uintptr(pidfd), 0, 0, 0, 0, 0); errno != 0 && errno != syscall.EPERM {
			return false
		}
	}
	return true
}
