package manager

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// A service's extra environment variables (SPEC.md §8.1, "Extra
// environment variables"): the Owner's own, for options its package
// doesn't make settings, kept in its instance; a secret's value is a
// Podman secret on the server running it, handed over with the next
// assignment like a typed-in secret setting.

// ServiceEnv is what a service's Environment variables card shows.
type ServiceEnv struct {
	Title string
	// Containers are the package's containers, the main route's first.
	Containers []string
	Vars       []instance.EnvVar
	// Managed are the variables the package sets itself, by container.
	Managed map[string][]string
	// Docs is the project's list of the variables its app reads, if the
	// package gives one.
	Docs string
	// File is the service's instance file, which keeps them.
	File string
}

// ServiceEnv returns a service's extra environment variables.
func (m *Manager) ServiceEnv(name string) (ServiceEnv, error) {
	p, in, err := m.installedService(name)
	if err != nil {
		return ServiceEnv{}, err
	}
	var main string
	if r := p.Routes[MainRoute(p)]; r != nil {
		main = r.Container
	}
	containers := slices.Sorted(maps.Keys(p.Containers))
	slices.SortStableFunc(containers, func(a, b string) int {
		return cmp.Compare(boolInt(b == main), boolInt(a == main))
	})
	managed := map[string][]string{}
	for _, c := range containers {
		managed[c] = p.ManagedEnv(c)
	}
	return ServiceEnv{Title: cmp.Or(in.Title, p.Title), Containers: containers, Vars: in.Env, Managed: managed,
		Docs: p.About.EnvDocs, File: state.InstanceFile(m.Lab, name)}, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// EnvKey is how SetEnv's new secret values name a variable:
// <container>/<name>.
func EnvKey(container, name string) string { return container + "/" + name }

// SetEnv replaces a service's extra environment variables with vars, and
// applies the change, which restarts the containers they're in. A secret
// takes its new value from values (by EnvKey), or keeps the one it has: a
// secret without a value, new or changed from a plain variable, is
// refused.
func (m *Manager) SetEnv(ctx context.Context, name string, vars []instance.EnvVar, values map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
	if i < 0 {
		return ErrNotFound
	}
	p, in := services[i].Package, services[i].Instance
	if len(vars) > instance.MaxEnv {
		return usererr.New("A service can have at most %d extra variables.", instance.MaxEnv)
	}
	old := map[string]instance.EnvVar{}
	for _, e := range in.Env {
		old[EnvKey(e.Container, e.Name)] = e
	}
	secrets := map[string][]byte{}
	seen := map[string]bool{}
	env := make([]instance.EnvVar, 0, len(vars))
	for _, v := range vars {
		key := EnvKey(v.Container, v.Name)
		if seen[key] {
			return usererr.New("%s is given twice.", v.Name)
		}
		seen[key] = true
		e := instance.EnvVar{Container: v.Container, Name: v.Name}
		if v.Secret {
			e.Secret = true
			was := old[key]
			value, given := values[key]
			switch {
			case given && value == "":
				return usererr.New("%s is a secret, so it needs a value.", v.Name)
			case given:
				if strings.ContainsAny(value, "\n\r\x00") {
					return usererr.New("%s: a value can't have a line break in it.", v.Name)
				}
				if len(value) > instance.MaxEnvValue {
					return usererr.New("%s: a value can be at most 4 KB.", v.Name)
				}
				// A new value is a new Podman secret: the container file
				// changes, so the container restarts with it.
				e.Rev = was.Rev + 1
				secrets[node.SecretName(name, e.EnvSecretRef())] = []byte(value)
			case was.Secret:
				e.Rev = was.Rev
			default:
				return usererr.New("%s is a secret, so it needs a value.", v.Name)
			}
		} else {
			e.Value = v.Value
		}
		if err := instance.CheckEnvVar(p, e); err != nil {
			return usererr.New("%s.", strings.TrimSuffix(words.Capitalise(err.Error()), "."))
		}
		env = append(env, e)
	}
	in.Env = env
	if err := in.Validate(p); err != nil {
		return usererr.New("%s", err)
	}
	if _, err := state.Desired(m.Lab, state.OnNode(services, in.Host)); err != nil {
		return clashError(err)
	}
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	if len(secrets) > 0 {
		if m.secrets == nil {
			m.secrets = map[string]map[string][]byte{}
		}
		if m.secrets[name] == nil {
			m.secrets[name] = map[string][]byte{}
		}
		maps.Copy(m.secrets[name], secrets)
	}
	m.startApplyLocked("Changing " + cmp.Or(in.Title, p.Title) + "'s environment variables")
	return nil
}
