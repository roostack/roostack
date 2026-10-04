package manager

import (
	"context"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// InstallSettings are a package's settings asked for at install and used
// only by its install hooks (e.g. Forgejo's administrator): the ones worth
// changing when those hooks failed because of what was typed.
func InstallSettings(p *pkgspec.Package) []pkgspec.Setting {
	var out []pkgspec.Setting
	for _, s := range p.Settings {
		if s.InstallOnly && !s.Generate {
			out = append(out, s)
		}
	}
	return out
}

// RetryInstallWith changes a service's install settings and runs its
// install hooks again (after they failed, e.g. on an invalid username).
// An empty secret keeps the one typed before.
func (m *Manager) RetryInstallWith(ctx context.Context, name string, settings, secrets map[string]string) error {
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
	if in.Settings == nil {
		in.Settings = map[string]any{}
	}
	typed := map[string][]byte{}
	for _, s := range InstallSettings(p) {
		if s.Type != pkgspec.SettingSecret {
			raw, given := settings[s.Key]
			v, err := parseSetting(s, raw, given)
			if err != nil {
				return err
			}
			in.Settings[s.Key] = v
			continue
		}
		v := secrets[s.Key]
		switch {
		case v == "":
			continue // keep what was typed before
		case s.MinLength > 0 && len([]rune(v)) < s.MinLength:
			return formErrorf("%s must be at least %d characters.", s.Title, s.MinLength)
		case strings.ContainsAny(v, "\n\r\x00"):
			return formErrorf("%s can't contain line breaks.", s.Title)
		}
		typed[node.SecretName(name, s.Key)] = []byte(v)
	}
	if err := in.Validate(p); err != nil {
		return formErrorf("%s", err)
	}
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	// The next assignment runs its install hooks again, with the new
	// values (typed secrets replace the ones it has).
	m.setUp = append(m.setUp, name)
	if m.secrets == nil {
		m.secrets = map[string]map[string][]byte{}
	}
	m.secrets[name] = typed
	m.applyReason = "Trying " + name + "'s setup again"
	m.startApplyLocked()
	return nil
}
