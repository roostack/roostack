package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	osuser "os/user"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Commands that work on the roostack user's files run as that user, even
// when started with sudo (SPEC.md §6.1). Its state directory belongs to it,
// so a process running as roostack (the service, or something that broke
// out of a container) can put symlinks there; root following them would
// write wherever they point. These commands are re-run as the roostack user
// before they read anything, and root keeps only what needs it (node join's
// restart of the service).

// serviceUser owns Roostack's files and runs its service.
const serviceUser = "roostack"

// asServiceUserEnv is set for a command re-run by runAsRoostack, so it
// leaves what needs root to the root process that started it.
const asServiceUserEnv = "ROOSTACK_FROM_ROOT"

// asServiceUser are the commands re-run as the roostack user when started
// as root. node join is re-run by nodeCmd itself, which restarts the
// service afterwards.
var asServiceUser = map[string]bool{"user": true, "backup": true, "apply": true}

// exitStatus is the exit status of a command re-run as the roostack user,
// which has already said what went wrong.
type exitStatus int

func (e exitStatus) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// fromRoot reports whether this command was re-run as the roostack user by
// root (runAsRoostack).
func fromRoot() bool { return os.Getenv(asServiceUserEnv) != "" }

// runAsRoostack runs this program again with args as the roostack user,
// with its systemd session (XDG_RUNTIME_DIR), and waits for it.
//
// The new process gets no terminal: it runs in a session of its own, and
// its output comes back through pipes, with control characters removed. A
// process running as roostack could take it over (ptrace) and, through a
// terminal, type commands into root's shell (TIOCSTI) or send escape
// sequences to the terminal.
func runAsRoostack(args []string) error {
	u, err := osuser.Lookup(serviceUser)
	if err != nil {
		return fmt.Errorf("this command runs as the %s user, which isn't here: %w", serviceUser, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return err
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return err
	}
	var groups []uint32
	if ids, err := u.GroupIds(); err == nil {
		for _, id := range ids {
			if g, err := strconv.ParseUint(id, 10, 32); err == nil {
				groups = append(groups, uint32(g))
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "HOME="+u.HomeDir, "USER="+serviceUser, "LOGNAME="+serviceUser, asServiceUserEnv+"=1")
	for k, v := range userSession(int(uid)) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Wrapped, so exec makes pipes rather than passing the terminal on.
	cmd.Stdout = &controlFilter{w: os.Stdout}
	cmd.Stderr = &controlFilter{w: os.Stderr}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:     true,
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups},
	}
	// Something it started may keep the pipes open.
	cmd.WaitDelay = 10 * time.Second

	// Ctrl-C reaches only this process (the other has no terminal): pass
	// it on, so a backup stops as it would have.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("running as the %s user: %w", serviceUser, err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case s := <-sigs:
				cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code < 0 { // killed by a signal
			code = 1
		}
		return exitStatus(code)
	}
	return err
}

// userSession is the environment that finds a user's systemd manager:
// systemctl --user and rootless Podman look for it through XDG_RUNTIME_DIR
// and its D-Bus socket there.
func userSession(uid int) map[string]string {
	dir := fmt.Sprintf("/run/user/%d", uid)
	return map[string]string{"XDG_RUNTIME_DIR": dir, "DBUS_SESSION_BUS_ADDRESS": "unix:path=" + dir + "/bus"}
}

// ownSession is what a command run as the roostack user itself needs set
// to reach that user's systemd manager (userSession): sudo -u roostack
// passes on neither, and su keeps root's. Nothing when it's set already
// (the service has it from its unit), and nothing for root, which re-runs
// the commands that need it as roostack (runAsRoostack), or for another
// user, who has no business with roostack's manager.
func ownSession(uid int, serviceUID string, getenv func(string) string) map[string]string {
	if strconv.Itoa(uid) != serviceUID {
		return nil
	}
	want := userSession(uid)
	if getenv("XDG_RUNTIME_DIR") == want["XDG_RUNTIME_DIR"] && getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return nil
	}
	return want
}

// setOwnSession sets ownSession's variables for this process and the
// commands it runs, so "sudo -u roostack roostack backup snapshots" works
// as it is.
func setOwnSession() {
	u, err := osuser.Lookup(serviceUser)
	if err != nil {
		return // not installed here: nothing to find
	}
	for k, v := range ownSession(os.Geteuid(), u.Uid, os.Getenv) {
		os.Setenv(k, v)
	}
}

// controlFilter passes output on without control characters other than
// tab, newline and carriage return (progress lines use it), so nothing it
// passes is an escape sequence to the terminal. C1 controls (U+0080 to
// U+009F, two bytes in UTF-8) go too.
type controlFilter struct {
	mu sync.Mutex
	w  io.Writer
	// c2 is set when the last write ended in the first byte of a C1
	// control's encoding, or of another character.
	c2 bool
}

func (f *controlFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, 0, len(p)+1)
	for _, b := range p {
		if f.c2 {
			f.c2 = false
			if b >= 0x80 && b <= 0x9f {
				continue // a C1 control: both bytes dropped
			}
			out = append(out, 0xc2)
		}
		switch {
		case b == 0xc2:
			f.c2 = true
		case b == '\t' || b == '\n' || b == '\r':
			out = append(out, b)
		case b < 0x20 || b == 0x7f:
		default:
			out = append(out, b)
		}
	}
	if _, err := f.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
