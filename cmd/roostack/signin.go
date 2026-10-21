package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/web"
)

// startSignIn serves the sign-in page for services (single sign-on,
// SPEC.md §5.2) on its own listener, config.SignInPort on the UI's
// address: Caddy sends login.<domain> there, and asks it about requests
// to walled services. Its signing key is made the first time.
func startSignIn(ctx context.Context, lab config.Lab, st *store.Store, srv *web.Server, mgr *manager.Manager, local *node.Local, log *slog.Logger) error {
	key, err := sso.LoadKey(lab.Paths.SigningKeyFile())
	if err != nil {
		return fmt.Errorf("single sign-on's signing key: %w", err)
	}
	srv.SignIn = &sso.Provider{Store: st, Key: key, Log: log,
		Issuer:  func() string { return "https://" + lab.SignInHost() },
		Clients: conformanceClients(mgr.SSOClients),
		// The app's secret is the Podman secret Roostack made for it on
		// this server (only the controller's services have single sign-on
		// for now).
		ClientSecret: func(ctx context.Context, id string) ([]byte, error) {
			if local.Secrets == nil {
				return nil, errors.New("no secrets on this server")
			}
			return local.Secrets.Read(ctx, generate.SSOSecretName(id))
		},
	}
	srv.SSO, srv.SignInHost, srv.SSODir = mgr, lab.SignInHost(), lab.Paths.SSODir()
	// Roostack's own pages sign in there once Caddy serves it (SPEC.md §5.2).
	srv.SignInServed = srv.CaddyServesSignIn(manager.CaddyLocal)

	addr := fmt.Sprintf("%s:%d", lab.UIBind, config.SignInPort)
	httpSrv := &http.Server{Addr: addr, Handler: srv.SignInHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()
	go func() {
		log.Info("sign-in page listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error("sign-in page stopped", "err", err)
		}
	}()
	return nil
}
