package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/web"
)

// setupPages prepares the first-run pages for the home network (SPEC.md
// §6.3.2), while setup lasts (web.FirstRunOpen): the setup code hasn't
// been used, or the Owner hasn't finished the wizard, for a day at most.
// It makes the certificate and tells the node to open the port in the
// firewall from its first apply, and returns the listener for
// serveSetupPages to run; nil when setup is done (anything left from the
// setup pages, their certificate and sessions, is deleted).
func setupPages(ctx context.Context, lab config.Lab, st *store.Store, token string, local *node.Local,
	log *slog.Logger) *web.SetupListener {
	if !web.FirstRunOpen(ctx, st, token != "", &setup.Setup{Lab: lab, Store: st}, time.Now()) {
		endSetupPages(ctx, lab, st, log)
		return nil
	}
	addrs := web.HomeAddrs()
	cert, fingerprint, err := web.SetupCert(lab.Paths, addrs, time.Now())
	if err != nil {
		log.Error("making the setup pages' certificate: they're only reachable through an SSH tunnel", "err", err)
		return nil
	}
	var urls []string
	for _, a := range addrs {
		urls = append(urls, web.SetupURL(a))
	}
	if len(urls) == 0 {
		urls = []string{"(none: this server has no address on a home network yet)"}
	}
	// The address to open, and the certificate to check in the browser
	// before typing the setup code. Neither is a secret.
	log.Info("first-run setup pages on the home network", "url", strings.Join(urls, " "),
		"certificate_sha256", fingerprint, "file", lab.Paths.SetupCertFile())
	local.SetSetupPort(config.SetupPort)
	return &web.SetupListener{Cert: cert, Addr: fmt.Sprintf(":%d", config.SetupPort), Log: log, Check: 30 * time.Second}
}

// serveSetupPages runs the setup listener until setup is finished, then
// closes its port in the firewall and deletes its certificate.
func serveSetupPages(ctx context.Context, l *web.SetupListener, srv *web.Server, lab config.Lab, local *node.Local,
	log *slog.Logger) {
	l.Server = srv
	if err := l.Run(ctx); err != nil {
		log.Error("serving the setup pages to the home network", "err", err)
		return
	}
	if ctx.Err() != nil {
		return // Roostack is stopping: they're back at its next start
	}
	log.Info("the setup pages on the home network are closed (setup is finished, or a day has passed since the Owner was made)")
	local.SetSetupPort(0)
	if err := srv.EndSetupPages(ctx); err != nil {
		log.Error("signing out of the setup pages", "err", err)
	}
	endSetupPages(ctx, lab, nil, log)
	local.Reapply("The setup pages are closed: closing their port")
}

// endSetupPages deletes what's left of the setup pages: their certificate
// and, given st, everything signed in on them.
func endSetupPages(ctx context.Context, lab config.Lab, st *store.Store, log *slog.Logger) {
	if err := web.RemoveSetupCert(lab.Paths); err != nil {
		log.Error("deleting the setup pages' certificate", "err", err)
	}
	if st != nil {
		if err := web.EndSetupPages(ctx, st); err != nil {
			log.Error("signing out of the setup pages", "err", err)
		}
	}
}
