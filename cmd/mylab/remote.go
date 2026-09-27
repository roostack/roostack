package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/manager"
)

// forwardToRemote passes this lab's servers' backups to its storage at
// another lab (SPEC.md §10.10): only the controller has the link there, so
// it forwards on its own loopback and its address on MyLab's network.
// Everything is restic-encrypted before it leaves the server.
func forwardToRemote(ctx context.Context, lab config.Lab, r manager.RemoteStorage, log *slog.Logger) {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(r.HostAddr.String(), strconv.Itoa(r.Port))}
	proxy := httputil.NewSingleHostReverseProxy(target)
	addrs := []string{"127.0.0.1"}
	if m, ok, _ := labnet.LoadMembership(lab.Paths.StateDir); ok {
		addrs = append(addrs, m.Addr.String())
	}
	for _, a := range addrs {
		go func() {
			addr := net.JoinHostPort(a, strconv.Itoa(r.ProxyPort))
			var ln net.Listener
			for {
				var err error
				if ln, err = net.Listen("tcp", addr); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
			}
			srv := &http.Server{Handler: proxy, ReadHeaderTimeout: 30 * time.Second}
			go func() {
				<-ctx.Done()
				srv.Close()
			}()
			log.Info("forwarding backups to another lab", "host", r.HostName, "addr", addr)
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				log.Error("forwarding backups", "host", r.HostName, "err", err)
			}
		}()
	}
}
