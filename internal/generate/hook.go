package generate

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/pkgspec"
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
	run := &HookRun{Container: containerUnitName(p.Name, h.Container), Command: h.Command,
		Env: map[string]string{}, Secrets: map[string]string{}, Inputs: map[string]string{}}
	for _, k := range slices.Sorted(maps.Keys(h.Env)) {
		v, err := ctx.render(fmt.Sprintf("hooks.%s.env.%s", h.On, k), h.Env[k])
		if err != nil {
			return nil, err
		}
		run.Env[k] = v
	}
	for k, ref := range h.Secrets {
		if key, ok := strings.CutPrefix(ref, "inputs."); ok {
			run.Inputs[k] = key
		} else {
			run.Secrets[k] = secretName(p.Name, ref)
		}
	}
	return run, nil
}
