// Command roostack-agent is Roostack's small root helper (SPEC.md §6.1). It runs
// as root under systemd and accepts requests only from root and the roostack
// user, over a local Unix socket.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"roostack.cloud/roostack/internal/agent"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	socket := flag.String("socket", agent.DefaultSocket, "Unix socket to listen on")
	userName := flag.String("user", "roostack", "unprivileged user allowed to call the agent")
	firewallFile := flag.String("firewall-file", agent.DefaultFirewallFile, "where the applied firewall is saved")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, *socket, *userName, *firewallFile); err != nil {
		log.Error("roostack-agent stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, socket, userName, firewallFile string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root")
	}
	u, err := user.Lookup(userName)
	if err != nil {
		return err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}

	host := agent.LinuxHost{FirewallFile: firewallFile, UID: int(uid), GID: gid}
	if err := host.RestoreFirewall(); err != nil {
		// Keep running: the roostack service can re-apply the firewall.
		log.Error("restoring firewall failed", "err", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s := &agent.Server{Host: host, AllowedUIDs: []uint32{0, uint32(uid)}, Version: version, Log: log}
	log.Info("roostack-agent listening", "socket", socket, "user", userName, "version", version)
	return s.Serve(ctx, socket, gid)
}
