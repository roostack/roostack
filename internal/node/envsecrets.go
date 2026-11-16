package node

import (
	"context"
	"maps"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
)

// removeOldEnvSecrets deletes the Podman secrets of extra environment
// variables no service here uses any more (SPEC.md §8.1, "Extra
// environment variables"): a secret's value before its latest, or one
// whose variable was removed. Only names in exactly the form an extra
// variable's secret has, for one of the service's containers, so another
// service's secrets are never taken for them.
func (n *Local) removeOldEnvSecrets(ctx context.Context, act *activity.Log, services []state.Service, desired reconcile.Desired) {
	if n.Secrets == nil {
		return
	}
	wanted := map[string]bool{}
	for _, out := range desired.Services {
		for _, s := range out.Secrets {
			wanted[s.Name] = true
		}
	}
	have, err := n.Secrets.List(ctx)
	if err != nil {
		n.Log.Warn("listing secrets to remove old ones", "err", err)
		return
	}
	for _, sv := range services {
		svc := sv.Name()
		for _, name := range slices.Sorted(maps.Keys(have)) {
			if wanted[name] || !envSecretOf(sv.Package, svc, name) {
				continue
			}
			if err := n.Secrets.Remove(ctx, name); err != nil {
				n.Log.Warn("removing an old secret", "secret", name, "err", err)
				continue
			}
			act.Printf("Removed %s, a value no longer used", name)
		}
	}
}

// envSecretOf says whether name is the Podman secret of an extra variable
// of svc: roostack-<svc>-env-<container>-<name>-<rev>, for one of the
// package's containers.
func envSecretOf(p *pkgspec.Package, svc, name string) bool {
	rest, ok := strings.CutPrefix(name, SecretName(svc, instance.EnvSecretPrefix))
	if !ok {
		return false
	}
	for c := range p.Containers {
		v, ok := strings.CutPrefix(rest, c+"-")
		if !ok {
			continue
		}
		i := strings.LastIndexByte(v, '-')
		if i > 0 && instance.ValidEnvName(v[:i]) && allDigits(v[i+1:]) {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
