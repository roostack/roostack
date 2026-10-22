package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/web"
)

// How often a node fetches its assignment: until it has one, then as a
// fallback to the controller sending it (SPEC.md §10.3).
const (
	pullRetry = 30 * time.Second
	pullEvery = 15 * time.Minute
)

// serveNode runs a server that another server (the controller) manages
// (SPEC.md §10.3): it serves the lab API over Roostack's network, and no UI.
func serveNode(ctx context.Context, lab config.Lab, local *node.Local, ag *agent.Client, restart func(), log *slog.Logger) error {
	m, _, err := labnet.LoadMembership(lab.Paths.StateDir)
	if err != nil {
		return err
	}
	log.Info("running as a node", "id", local.ID.ID, "name", local.ID.Name, "addr", m.Addr, "controller", m.ControllerID)
	saved := filepath.Join(labnet.IdentityDir(lab.Paths.StateDir), "assignment.json")
	api := &node.Server{Node: local, ControllerID: m.ControllerID, Saved: func(w node.WireAssignment) {
		data, _ := json.Marshal(w)
		fsutil.WritePrivate(saved, data)
	}}
	// Removed from the lab: after replying (the reply needs WireGuard),
	// drop the network and restart as a lab of its own.
	api.Leave = func() {
		go func() {
			time.Sleep(time.Second)
			log.Info("leaving the lab", "controller", m.ControllerID)
			if err := labnet.Leave(context.Background(), lab.Paths.StateDir, ag); err != nil {
				log.Error("leaving the lab", "err", err)
			}
			restart()
		}()
	}
	api.Rename = func(name string) error {
		id := local.ID
		id.Name = name
		return id.Save(lab.Paths.StateDir)
	}
	// Keep running the last assignment while the controller is away.
	if data, err := os.ReadFile(saved); err == nil {
		var w node.WireAssignment
		if err := json.Unmarshal(data, &w); err == nil {
			w.Reason = "Roostack started (after a restart or an update)"
			if err := api.Take(w); err != nil {
				log.Error("applying the saved assignment", "err", err)
			}
		}
	}
	go pullAssignments(ctx, lab, m, api, log)
	// Offering storage for other labs: their requests go to the controller.
	go serveGuestAPI(ctx, local, m, labnet.Identity{Dir: labnet.IdentityDir(lab.Paths.StateDir)}, log)
	// Renew this server's lab certificate before it expires.
	labID := labnet.Identity{Dir: labnet.IdentityDir(lab.Paths.StateDir)}
	go labnet.KeepRenewed(ctx, labID, local.ID.ID, labnet.RenewFromController(labnet.ControllerURL(m), m.ControllerID, labID), log)
	// Roostack Cloud backup's keys, for this server's folder only, through
	// the controller (which asks Cloud).
	setCloudBackupKeys(labnet.StorageKeysFromController(labnet.ControllerURL(m), m.ControllerID, labID))

	// Its own web address says which lab it's part of, and where to manage
	// it (SPEC.md §10.2).
	fenced := func() *backup.Fence {
		v, _ := local.State.Setting(context.Background(), backup.FenceKey)
		return backup.ParseFence(v)
	}
	if page, err := web.NodePage(local.ID.Name, m.LabName, m.UI, fenced); err == nil {
		ui := &http.Server{Addr: fmt.Sprintf("%s:%d", lab.UIBind, lab.UIPort), Handler: page, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			ui.Close()
		}()
		go func() {
			if err := ui.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Warn("this server's page", "err", err)
			}
		}()
	} else {
		log.Error("this server's page", "err", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/v1/", api.Handler())
	labnet.Serve(ctx, lab.Paths.StateDir, mux, log)
	return nil
}

// pullAssignments fetches the node's assignment from the controller.
func pullAssignments(ctx context.Context, lab config.Lab, m labnet.Membership, api *node.Server, log *slog.Logger) {
	tlsConf, err := labnet.Identity{Dir: labnet.IdentityDir(lab.Paths.StateDir)}.ClientTLS(m.ControllerID)
	if err != nil {
		log.Error("reaching the controller", "err", err)
		return
	}
	client := &http.Client{Timeout: time.Minute, Transport: &http.Transport{TLSClientConfig: tlsConf}}
	for {
		wait := pullEvery
		if err := pull(ctx, client, m, api); err != nil {
			log.Warn("fetching this server's assignment", "err", err)
			wait = pullRetry
		}
		if !sleep.For(ctx, wait) {
			return
		}
	}
}

func pull(ctx context.Context, client *http.Client, m labnet.Membership, api *node.Server) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.ControllerURL()+"/v1/assignment", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var w node.WireAssignment
	if err := labnet.DecodeResponse(resp, &w); err != nil {
		return err
	}
	if w.Version == 0 {
		return errors.New("empty assignment")
	}
	return api.Take(w)
}

// serveGuestAPI forwards other labs' requests (joining, usage) from the
// guests' link to the controller, which keeps their records, while this
// server offers storage for others (it follows the assignment).
func serveGuestAPI(ctx context.Context, local *node.Local, m labnet.Membership, id labnet.Identity, log *slog.Logger) {
	tlsConf, err := id.ClientTLS(m.ControllerID)
	if err != nil {
		log.Error("forwarding other labs' requests", "err", err)
		return
	}
	target, _ := url.Parse(labnet.ControllerURL(m))
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/v1/guest-api" + pr.In.URL.Path
			pr.Out.URL.RawPath = ""
		},
		Transport: &http.Transport{TLSClientConfig: tlsConf},
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/guest/join", proxy)
	mux.Handle("GET /v1/guest/usage", proxy)
	for {
		if addr := local.GuestAddr(); addr != "" {
			if ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(labnet.GuestAPIPort))); err == nil {
				serveWhile(ctx, ln, mux, func() bool { return local.GuestAddr() == addr }, log)
			}
		}
		if !sleep.For(ctx, 10*time.Second) {
			return
		}
	}
}

// serveWhile serves on ln until ctx ends or still reports false.
func serveWhile(ctx context.Context, ln net.Listener, h http.Handler, still func() bool, log *slog.Logger) {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer srv.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-time.After(10 * time.Second):
				if !still() {
					log.Info("no longer offering storage for others")
					return
				}
			}
		}
	}()
	log.Info("forwarding other labs' requests to the controller", "addr", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		log.Error("forwarding other labs' requests", "err", err)
	}
	close(done)
}
