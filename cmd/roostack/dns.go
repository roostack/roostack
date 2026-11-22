package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"roostack.cloud/roostack/internal/labdns"
)

// dnsCmd is roostack-dns.service's command: Roostack's own DNS server for
// the home network and the lab's VPN devices (SPEC.md §6.5.3), apart from
// the daemon so that it sees none of Roostack's state and keeps answering
// while Roostack restarts. It stops once the names file is gone.
func dnsCmd(args []string) error {
	fs := flag.NewFlagSet("dns", flag.ExitOnError)
	names := fs.String("names", labdns.NamesFile, "the names file Roostack writes")
	port := fs.Int("port", labdns.Port, "the port to answer on")
	fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return labdns.Run(ctx, log, labdns.Options{NamesFile: *names, Port: *port})
}
