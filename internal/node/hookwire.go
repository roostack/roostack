package node

import (
	"context"
	"net/http"
	"net/url"
)

// Package hooks (hooks.go) on the lab API: a service's setup, its actions
// and its secrets.

// actionReply is what an action printed.
type actionReply struct {
	Output string `json:"output"`
}

// secretReply is a secret's value.
type secretReply struct {
	Value string `json:"value"`
}

func (r *Remote) InstallStatus(ctx context.Context, service string) HookStatus {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var st HookStatus
	if err := r.call(ctx, http.MethodGet, servicePath(service, "/setup"), nil, &st); err != nil {
		return HookStatus{Failed: err.Error()}
	}
	return st
}

func (r *Remote) RetryInstall(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, servicePath(service, "/setup/retry"), struct{}{}, nil)
}

func (r *Remote) RunAction(ctx context.Context, service, action string, inputs map[string]string) (string, error) {
	var out actionReply
	err := r.call(ctx, http.MethodPost, servicePath(service, "/actions/"+url.PathEscape(action)), inputs, &out)
	return out.Output, err
}

func (r *Remote) RunScheduled(ctx context.Context, service, hook string) error {
	return r.call(ctx, http.MethodPost, servicePath(service, "/scheduled/"+url.PathEscape(hook)+"/run"), struct{}{}, nil)
}

func (r *Remote) RevealSecret(ctx context.Context, service, key string) (string, error) {
	var out secretReply
	err := r.call(ctx, http.MethodPost, servicePath(service, "/secrets/"+url.PathEscape(key)), struct{}{}, &out)
	return out.Value, err
}

// hookRoutes serves package hooks on the lab API.
func (s *Server) hookRoutes(handle handleFunc) {
	n := s.Node
	handle("GET /v1/services/{service}/setup", func(r *http.Request) (any, error) {
		return n.InstallStatus(r.Context(), svcOf(r)), nil
	})
	handle("POST /v1/services/{service}/setup/retry", func(r *http.Request) (any, error) {
		return nil, n.RetryInstall(r.Context(), svcOf(r))
	})
	handle("POST /v1/services/{service}/actions/{action}", func(r *http.Request) (any, error) {
		inputs, err := body[map[string]string](r)
		if err != nil {
			return nil, err
		}
		out, err := n.RunAction(r.Context(), svcOf(r), r.PathValue("action"), inputs)
		return actionReply{Output: out}, err
	})
	handle("POST /v1/services/{service}/scheduled/{hook}/run", func(r *http.Request) (any, error) {
		return nil, n.RunScheduled(r.Context(), svcOf(r), r.PathValue("hook"))
	})
	handle("POST /v1/services/{service}/secrets/{key}", func(r *http.Request) (any, error) {
		v, err := n.RevealSecret(r.Context(), svcOf(r), r.PathValue("key"))
		return secretReply{Value: v}, err
	})
}
