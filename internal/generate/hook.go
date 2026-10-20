package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
)

// HookRun is a hook ready to run with podman exec.
type HookRun struct {
	Container string   // container name
	Command   []string // as written in the package
	// Env holds rendered environment values. Secret values are added by
	// the caller: see Secrets.
	Env map[string]string
	// Secrets maps an environment variable to a Podman secret name, for
	// secret settings.
	Secrets map[string]string
	// Inputs maps an environment variable to a secret input's key.
	Inputs map[string]string
	// Saves is the Podman secret the hook's standard output becomes
	// (pkgspec.Hook.Saves), "" for most hooks.
	Saves string
}

// Fingerprint identifies what an sso hook would do: its container,
// command, env and the names of its secrets, never their values. The
// server runs it again when this changes (SPEC.md §5.2).
func (r *HookRun) Fingerprint() string {
	h := sha256.New()
	fmt.Fprintf(h, "%q %q\n", r.Container, r.Command)
	for _, k := range slices.Sorted(maps.Keys(r.Env)) {
		fmt.Fprintf(h, "env %q=%q\n", k, r.Env[k])
	}
	for _, k := range slices.Sorted(maps.Keys(r.Secrets)) {
		fmt.Fprintf(h, "secret %q=%q\n", k, r.Secrets[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Hook prepares a hook for one installed service. inputs are the action's
// non-secret input values (nil for install hooks).
func Hook(lab config.Lab, p *pkgspec.Package, in *instance.Instance, h *pkgspec.Hook, inputs map[string]string) (*HookRun, error) {
	ctx := buildContext(lab, p, in)
	ctx.Inputs = map[string]string{}
	for _, i := range h.Inputs {
		if i.Type != pkgspec.SettingSecret {
			ctx.Inputs[i.Key] = inputs[i.Key]
		}
	}
	run := &HookRun{Container: containerUnitName(in.ID(), h.Container), Command: h.Command,
		Env: map[string]string{}, Secrets: map[string]string{}, Inputs: map[string]string{}}
	for _, k := range slices.Sorted(maps.Keys(h.Env)) {
		v, err := ctx.render(fmt.Sprintf("hooks.%s.env.%s", h.On, k), h.Env[k])
		if err != nil {
			return nil, err
		}
		run.Env[k] = v
	}
	for k, ref := range h.Secrets {
		switch key, ok := strings.CutPrefix(ref, "inputs."); {
		case ok:
			run.Inputs[k] = key
		case ref == pkgspec.SSOClientSecret && !ctx.SSO.Enabled:
			run.Env[k] = "" // gone once it's off
		case ref == pkgspec.SSOClientSecret:
			run.Secrets[k] = SSOSecretName(in.ID())
		default:
			run.Secrets[k] = secretName(in.ID(), ref)
		}
	}
	if h.On == pkgspec.HookSSO {
		run.Env["ROOSTACK_SSO"] = "off"
		if ctx.SSO.Enabled {
			run.Env["ROOSTACK_SSO"] = "on"
		}
	}
	if h.Saves != "" {
		run.Saves = secretName(in.ID(), h.Saves)
	}
	return run, nil
}
