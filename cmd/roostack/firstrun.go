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
// §6.3.2), while setup lasts: the setup code hasn't been used, or the
// Owner hasn't finished the wizard. It makes the certificate and tells the
// node to open the port in the firewall from its first apply, and returns
// the listener for serveSetupPages to run; nil when setup is done (the
// certificate is deleted, if one was left).
func setupPages(ctx context.Context, lab config.Lab, st *store.Store, token string, local *node.Local,
	log *slog.Logger) *web.SetupListener {
	open := token != ""
	if !open {
		p, err := (&setup.Setup{Lab: lab, Store: st}).Progress(ctx)
		open = err == nil && p.Next() != ""
	}
	if !open {
		if err := web.RemoveSetupCert(lab.Paths); err != nil {
			log.Error("deleting the setup pages' certificate", "err", err)
		}
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
	log.Info("first-run setup is finished: the setup pages on the home network are closed")
	local.SetSetupPort(0)
	if err := web.RemoveSetupCert(lab.Paths); err != nil {
		log.Error("deleting the setup pages' certificate", "err", err)
	}
	local.Reapply("First-run setup is finished: closing the setup pages' port")
}
