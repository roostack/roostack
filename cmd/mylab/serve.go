package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/state"
	"github.com/mylab/mylab/internal/store"
	"github.com/mylab/mylab/internal/systemd"
	"github.com/mylab/mylab/internal/web"
)

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	listen := fs.String("listen", "", "address to listen on (default :<ui_port> from lab config)")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	st, err := store.Open(dbPath(lab))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	token, err := setupToken(ctx, lab, st, log)
	if err != nil {
		return err
	}
	sd := systemd.New()
	srv, err := web.NewServer(st, func(ctx context.Context) ([]web.ServiceView, error) {
		return serviceViews(ctx, lab, sd)
	}, token, log)
	if err != nil {
		return err
	}
	srv.OnSetupComplete = func() { os.Remove(setupTokenPath(lab)) }

	go func() {
		for range time.Tick(time.Hour) {
			if err := st.DeleteExpiredSessions(ctx); err != nil {
				log.Error("cleaning sessions", "err", err)
			}
		}
	}()

	addr := *listen
	if addr == "" {
		addr = fmt.Sprintf(":%d", lab.UIPort)
	}
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()
	log.Info("mylab listening", "addr", addr)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func dbPath(lab config.Lab) string { return filepath.Join(lab.Paths.StateDir, "mylab.db") }

func setupTokenPath(lab config.Lab) string { return filepath.Join(lab.Paths.StateDir, "setup-token") }

// setupToken returns the first-run setup code, or "" if an account exists.
// The code is kept in a file readable only by mylab (and root), so it
// survives restarts until setup is done.
func setupToken(ctx context.Context, lab config.Lab, st *store.Store, log *slog.Logger) (string, error) {
	n, err := st.CountUsers(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	path := setupTokenPath(lab)
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		token := strings.TrimSpace(string(data))
		log.Info("waiting for first-run setup", "setup_code", token, "file", path)
		return token, nil
	}
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Info("waiting for first-run setup", "setup_code", token, "file", path)
	return token, nil
}

// serviceViews builds the dashboard list from desired state plus live
// systemd status.
func serviceViews(ctx context.Context, lab config.Lab, sd *systemd.Manager) ([]web.ServiceView, error) {
	services, err := state.LoadServices(lab)
	if err != nil {
		return nil, err
	}
	var views []web.ServiceView
	for _, s := range services {
		v := web.ServiceView{Name: s.Package.Name, Title: s.Package.Title, Status: "unknown"}
		out, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			v.Status = "failed"
			views = append(views, v)
			continue
		}
		for _, h := range out.Hosts {
			v.URLs = append(v.URLs, "https://"+h)
		}
		if statuses, err := sd.Status(ctx, out.Units...); err == nil {
			v.Status = summarize(statuses)
		}
		views = append(views, v)
	}
	return views, nil
}

// summarize turns unit states into one status for people.
func summarize(units []systemd.UnitStatus) string {
	if len(units) == 0 {
		return "unknown"
	}
	status := "running"
	for _, u := range units {
		switch {
		case u.ActiveState == "failed":
			return "failed"
		case u.ActiveState == "activating" || u.ActiveState == "reloading":
			status = "starting"
		case u.ActiveState != "active" && status == "running":
			status = "stopped"
		}
	}
	return status
}
