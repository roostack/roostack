package pkgspec

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/quadlet"
)

var (
	nameRe       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	settingKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	envNameRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// A single DNS label.
	subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// A version names the lab's copy of the package file
	// (<name>/<version>.yaml, SPEC.md §8.3.1), so it must be a safe file
	// name: e.g. 1.34.3-roostack.1.
	versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

// ValidName reports whether n can be a package's name.
func ValidName(n string) bool { return nameRe.MatchString(n) }

// ValidVersion reports whether v can be a package version.
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Quadlet sections a package may add raw keys to.
var quadletSections = []string{"Unit", "Container", "Service", "Install"}

// Supported GPU kinds (docs/ASSUMPTIONS.md A7).
var gpuKinds = []string{"intel"}

// Validate checks the package is internally consistent. It reports every
// problem found, not just the first.
func (p *Package) Validate() error {
	v := &validator{}

	if p.Schema != SchemaV1Alpha1 {
		v.addf("schema: must be %q, got %q", SchemaV1Alpha1, p.Schema)
	}
	if !nameRe.MatchString(p.Name) {
		v.addf("name: %q must be lowercase letters, digits and dashes, starting with a letter", p.Name)
	}
	if p.Title == "" {
		v.addf("title: required")
	}
	if p.Version == "" {
		v.addf("version: required")
	} else if !ValidVersion(p.Version) {
		v.addf("version: %q must be letters, digits, dots, dashes, underscores and plus signs, starting with a letter or digit", p.Version)
	}
	if len(p.Containers) == 0 {
		v.addf("containers: at least one container is required")
	}

	p.validateAbout(v)
	p.validateSettings(v)
	for _, name := range slices.Sorted(maps.Keys(p.Storage)) {
		p.validateStorage(v, name, p.Storage[name])
	}
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		p.validateDatabase(v, name, p.Databases[name])
	}
	for _, name := range slices.Sorted(maps.Keys(p.Containers)) {
		p.validateContainer(v, name, p.Containers[name])
	}
	for _, name := range slices.Sorted(maps.Keys(p.Routes)) {
		p.validateRoute(v, name, p.Routes[name])
	}
	for _, name := range slices.Sorted(maps.Keys(p.Ports)) {
		p.validatePort(v, name, p.Ports[name])
	}
	p.validateHooks(v)
	p.validateSSO(v)
	for i, l := range p.Links {
		at := fmt.Sprintf("links[%d]", i)
		if l.Title == "" {
			v.addf("%s.title: required", at)
		}
		if p.Routes[l.Route] == nil {
			v.addf("%s.route: unknown route %q", at, l.Route)
		}
		if !strings.HasPrefix(l.Path, "/") || strings.HasPrefix(l.Path, "//") {
			v.addf("%s.path: must start with a single /", at)
		}
	}
	return v.err()
}

func (p *Package) validateSettings(v *validator) {
	seen := map[string]bool{}
	for i, s := range p.Settings {
		at := fmt.Sprintf("settings[%d]", i)
		if !settingKeyRe.MatchString(s.Key) {
			v.addf("%s.key: %q must be lowercase letters, digits and underscores", at, s.Key)
		} else {
			at = "settings." + s.Key
		}
		if seen[s.Key] {
			v.addf("%s: duplicate key", at)
		}
		seen[s.Key] = true

		switch s.Type {
		case SettingBool:
			if _, ok := s.Default.(bool); !ok && s.Default != nil && s.Default != DefaultAuto {
				v.addf("%s.default: must be true, false or %q", at, DefaultAuto)
			}
		case SettingString:
			if _, ok := s.Default.(string); !ok && s.Default != nil {
				v.addf("%s.default: must be a string", at)
			}
		case SettingInt:
			if _, ok := s.Default.(int); !ok && s.Default != nil {
				v.addf("%s.default: must be an integer", at)
			}
		case SettingSecret:
			if s.Default != nil {
				v.addf("%s.default: secrets cannot have a default; use generate: true", at)
			}
		default:
			v.addf("%s.type: must be one of bool, string, int, secret; got %q", at, s.Type)
		}
		if s.Generate && s.Type != SettingSecret {
			v.addf("%s.generate: only valid for secret settings", at)
		}
		if s.InstallValue != nil {
			switch {
			case s.Type == SettingSecret || s.InstallOnly:
				v.addf("%s.install_value: only for settings that are kept and aren't secrets", at)
			case !installValueFits(s.Type, s.InstallValue):
				v.addf("%s.install_value: must be a %s, like the setting", at, s.Type)
			case !p.hasInstallHook():
				v.addf("%s.install_value: used only until the install hooks succeed, and there are none", at)
			}
		}
		if s.Reveal && (s.Type != SettingSecret || s.InstallOnly) {
			v.addf("%s.reveal: only for secrets that are kept (not install_only)", at)
		}
		if s.Generate && s.InstallOnly {
			v.addf("%s: a generated secret can't be install_only (nobody would know it)", at)
		}
		if s.FromHook {
			switch {
			case s.Type != SettingSecret:
				v.addf("%s.from_hook: only for secrets", at)
			case s.Generate || s.InstallOnly || s.Reveal || s.MinLength != 0:
				v.addf("%s.from_hook: a hook makes it, so it takes no generate, install_only, reveal or min_length", at)
			case !p.hasSavingAction(s.Key):
				v.addf("%s.from_hook: needs an action that saves it (saves: %s), for services installed before it, or whose key was lost", at, s.Key)
			}
		}
		if s.Required && s.Type != SettingString {
			v.addf("%s.required: only for strings (secrets are always required)", at)
		}
		if s.MinLength != 0 && s.Type != SettingString && s.Type != SettingSecret {
			v.addf("%s.min_length: only for strings and secrets", at)
		}
	}
}

func (p *Package) validateHooks(v *validator) {
	actions := map[string]bool{}
	for i, h := range p.Hooks {
		at := fmt.Sprintf("hooks[%d]", i)
		if h == nil {
			v.addf("%s: empty hook", at)
			continue
		}
		switch h.On {
		case HookInstall:
			if len(h.Inputs) > 0 {
				v.addf("%s.inputs: only actions take inputs; install hooks use settings", at)
			}
		case HookAction:
			if !nameRe.MatchString(h.Name) {
				v.addf("%s.name: %q must be lowercase letters, digits and dashes", at, h.Name)
			}
			if actions[h.Name] {
				v.addf("%s.name: duplicate action %q", at, h.Name)
			}
			actions[h.Name] = true
			if h.Title == "" {
				v.addf("%s.title: required (the button's label)", at)
			}
		case HookSSO:
			switch {
			case p.SSO == nil:
				v.addf("%s.on: an sso hook needs the package's sso part", at)
			case h.Name != "" || len(h.Inputs) > 0:
				v.addf("%s: an sso hook takes no name or inputs", at)
			}
		default:
			v.addf("%s.on: must be %q, %q or %q", at, HookInstall, HookAction, HookSSO)
		}
		if h.Saves != "" {
			s := p.Setting(h.Saves)
			switch {
			case h.On == HookSSO:
				v.addf("%s.saves: only install and action hooks save a secret", at)
			case s == nil || !s.FromHook:
				v.addf("%s.saves: %q is not a from_hook secret setting", at, h.Saves)
			}
		}
		if p.Containers[h.Container] == nil {
			v.addf("%s.container: unknown container %q", at, h.Container)
		}
		if len(h.Command) == 0 {
			v.addf("%s.command: required", at)
		}
		inputs := map[string]bool{}
		for j, in := range h.Inputs {
			iat := fmt.Sprintf("%s.inputs[%d]", at, j)
			if !settingKeyRe.MatchString(in.Key) || inputs[in.Key] {
				v.addf("%s.key: %q must be a unique lowercase key", iat, in.Key)
			}
			inputs[in.Key] = true
			if in.Type != SettingString && in.Type != SettingSecret {
				v.addf("%s.type: inputs are string or secret", iat)
			}
			if in.Default != nil || in.Generate || in.InstallOnly {
				v.addf("%s: inputs take no default, generate or install_only", iat)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(h.Env)) {
			if !envNameRe.MatchString(k) {
				v.addf("%s.env: invalid variable name %q", at, k)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(h.Secrets)) {
			if !envNameRe.MatchString(k) {
				v.addf("%s.secrets: invalid variable name %q", at, k)
			}
			if _, dup := h.Env[k]; dup {
				v.addf("%s.secrets.%s: also set in env", at, k)
			}
			ref := h.Secrets[k]
			if ref == SSOClientSecret {
				if h.On != HookSSO || p.SSO == nil || p.SSO.OIDC == nil {
					v.addf("%s.secrets.%s: %s is only for an sso hook of an oidc app", at, k, ref)
				}
				continue
			}
			if key, ok := strings.CutPrefix(ref, "inputs."); ok {
				if in := h.Input(key); in == nil || in.Type != SettingSecret {
					v.addf("%s.secrets.%s: %q is not a secret input", at, k, ref)
				}
				continue
			}
			s := p.Setting(ref)
			switch {
			case s == nil || s.Type != SettingSecret:
				v.addf("%s.secrets.%s: %q is not a secret setting", at, k, ref)
			case s.InstallOnly && h.On != HookInstall:
				v.addf("%s.secrets.%s: %q is install_only and is deleted after install", at, k, ref)
			}
		}
	}
}

func (p *Package) validateStorage(v *validator, name string, s *Storage) {
	at := "storage." + name
	if !nameRe.MatchString(name) {
		v.addf("%s: invalid name", at)
	}
	if s == nil {
		v.addf("%s: class is required", at)
		return
	}
	if s.Class != ClassCritical && s.Class != ClassReplaceable {
		v.addf("%s.class: must be %q or %q", at, ClassCritical, ClassReplaceable)
	}
	if s.Location != LocationService && s.Location != LocationMedia {
		v.addf("%s.location: must be %q or %q", at, LocationService, LocationMedia)
	}
	if len([]rune(s.Title)) > maxStorageTitle || strings.ContainsAny(s.Title, "\n\r") {
		v.addf("%s.title: must be one line of at most %d characters", at, maxStorageTitle)
	}
}

// maxStorageTitle keeps a storage's title to a few words, as the install
// summary lists them in a sentence.
const maxStorageTitle = 60

func (p *Package) validateDatabase(v *validator, name string, d *Database) {
	at := "databases." + name
	if !nameRe.MatchString(name) {
		v.addf("%s: invalid name", at)
	}
	if d == nil {
		v.addf("%s: type is required", at)
		return
	}
	switch d.Type {
	case DBSQLite:
		storage, rel, ok := strings.Cut(d.Path, "/")
		if !ok || rel == "" || path.IsAbs(d.Path) || strings.Contains(d.Path, "..") {
			v.addf("%s.path: must be \"<storage>/<file>\", got %q", at, d.Path)
		} else if p.Storage[storage] == nil {
			v.addf("%s.path: unknown storage %q", at, storage)
		}
		if d.Image != "" || len(d.Quadlet) > 0 || d.Memory != "" {
			v.addf("%s: sqlite runs inside the app; image, memory and quadlet are not allowed", at)
		}
	case DBPostgres, DBRedis, DBMySQL, DBMariaDB:
		if d.Path != "" {
			v.addf("%s.path: only valid for sqlite", at)
		}
		validateQuadlet(v, at+".quadlet", d.Quadlet)
		validateMemory(v, at+".memory", d.Memory)
	default:
		v.addf("%s.type: must be one of sqlite, postgres, mysql, mariadb, redis; got %q", at, d.Type)
	}
}

func (p *Package) validateContainer(v *validator, name string, c *Container) {
	at := "containers." + name
	if !nameRe.MatchString(name) {
		v.addf("%s: invalid name", at)
	}
	// Generated unit names would collide with database and network units
	// (roostack-<svc>-db-<name>, roostack-<svc>-network).
	if name == "network" || strings.HasPrefix(name, "db-") {
		v.addf("%s: name is reserved", at)
	}
	if c == nil {
		v.addf("%s: image is required", at)
		return
	}
	if c.Image == "" {
		v.addf("%s.image: required", at)
	}
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		if !envNameRe.MatchString(k) {
			v.addf("%s.env: invalid variable name %q", at, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(c.Secrets)) {
		if !envNameRe.MatchString(k) {
			v.addf("%s.secrets: invalid variable name %q", at, k)
		}
		if _, dup := c.Env[k]; dup {
			v.addf("%s.secrets.%s: also set in env", at, k)
		}
		if err := p.checkSecretRef(c.Secrets[k]); err != nil {
			v.addf("%s.secrets.%s: %v", at, k, err)
		} else if s := p.Setting(c.Secrets[k]); s != nil && s.InstallOnly {
			v.addf("%s.secrets.%s: %q is install_only, so only install hooks can use it", at, k, c.Secrets[k])
		} else if s != nil && s.FromHook {
			v.addf("%s.secrets.%s: %q is from_hook, so only hooks can use it", at, k, c.Secrets[k])
		}
	}
	for i, m := range c.Mounts {
		switch {
		case m.Libraries && m.Storage != "":
			v.addf("%s.mounts[%d]: set storage or libraries, not both", at, i)
		case m.Libraries:
		case p.Storage[m.Storage] == nil:
			v.addf("%s.mounts[%d]: unknown storage %q", at, i, m.Storage)
		}
		if !path.IsAbs(m.Path) {
			v.addf("%s.mounts[%d].path: must be absolute, got %q", at, i, m.Path)
		}
	}
	for i, d := range c.Devices {
		if !slices.Contains(gpuKinds, d.GPU) {
			v.addf("%s.devices[%d].gpu: must be one of %v, got %q", at, i, gpuKinds, d.GPU)
		}
	}
	if h := c.Health; h != nil {
		if (h.HTTP == nil) == (h.Cmd == "") {
			v.addf("%s.health: set exactly one of http or cmd", at)
		}
		if h.HTTP != nil && (!validPort(h.HTTP.Port) || !strings.HasPrefix(h.HTTP.Path, "/")) {
			v.addf("%s.health.http: needs a valid port and a path starting with /", at)
		}
	}
	for _, dep := range c.DependsOn {
		kind, target, _ := strings.Cut(dep, ".")
		switch {
		case kind == "databases" && p.Databases[target] != nil:
			if p.Databases[target].Type == DBSQLite {
				v.addf("%s.depends_on: %q is sqlite and has no container", at, dep)
			}
		case kind == "containers" && p.Containers[target] != nil:
			if target == name {
				v.addf("%s.depends_on: cannot depend on itself", at)
			}
		default:
			v.addf("%s.depends_on: unknown reference %q (use databases.<name> or containers.<name>)", at, dep)
		}
	}
	validateMemory(v, at+".memory", c.Memory)
	validateQuadlet(v, at+".quadlet", c.Quadlet)
}

func validateMemory(v *validator, at, memory string) {
	if memory == "" {
		return
	}
	if _, err := ParseMemory(memory); err != nil {
		v.addf("%s: %v", at, err)
	}
}

func (p *Package) validateRoute(v *validator, name string, r *Route) {
	at := "routes." + name
	if !nameRe.MatchString(name) {
		v.addf("%s: invalid name", at)
	}
	if r == nil {
		v.addf("%s: container and port are required", at)
		return
	}
	if p.Containers[r.Container] == nil {
		v.addf("%s.container: unknown container %q", at, r.Container)
	}
	if !validPort(r.Port) {
		v.addf("%s.port: must be 1-65535", at)
	}
	if !subdomainRe.MatchString(r.Subdomain) {
		v.addf("%s.subdomain: %q is not a valid DNS label", at, r.Subdomain)
	}
	if r.Exposure != ExposurePrivate && r.Exposure != ExposurePublic {
		v.addf("%s.exposure: must be %q or %q", at, ExposurePrivate, ExposurePublic)
	}
	if h := r.ClientIPHeader; h != "" && !ValidClientIPHeader(h) {
		v.addf("%s.client_ip_header: %q must be an X- header name other than X-Forwarded-* (e.g. X-Real-IP)", at, h)
	}
	if m := r.Monitor; m != nil {
		if m.Path != "" && m.Path != "/" && !ValidPath(m.Path) {
			v.addf("%s.monitor.path: %q must start with / and have only letters, digits and - _ . : /", at, m.Path)
		}
		for i, st := range m.Statuses {
			if st < 100 || st > 599 {
				v.addf("%s.monitor.statuses[%d]: %d isn't an HTTP status", at, i, st)
			}
		}
	}
	seen := map[string]bool{}
	for i, pp := range r.Public.PrivatePaths {
		if !ValidPath(pp.Path) {
			v.addf("%s.public.private_paths[%d].path: %q must start with / and have only letters, digits and - _ . : / (no * or trailing /)", at, i, pp.Path)
		}
		if strings.TrimSpace(pp.Reason) == "" {
			v.addf("%s.public.private_paths[%d].reason: say why it shouldn't be public", at, i)
		}
		if seen[pp.Path] {
			v.addf("%s.public.private_paths[%d].path: %q is listed twice", at, i, pp.Path)
		}
		seen[pp.Path] = true
	}
	for i, lp := range r.Public.LoginPaths {
		if !ValidPath(lp) {
			v.addf("%s.public.login_paths[%d]: %q must start with / and have only letters, digits and - _ . : / (no * or trailing /)", at, i, lp)
		}
	}
}

var clientIPHeaderRe = regexp.MustCompile(`^[Xx]-[A-Za-z0-9-]+$`)

// ValidClientIPHeader says whether h can carry the visitor's address to an
// app: an X- header, not one of the X-Forwarded-* headers Caddy already
// sets, so a package can't change what other apps rely on.
func ValidClientIPHeader(h string) bool {
	return clientIPHeaderRe.MatchString(h) && !strings.HasPrefix(strings.ToLower(h), "x-forwarded-")
}

var pathRe = regexp.MustCompile(`^(/[A-Za-z0-9._:-]+)+$`)

// ValidPath says whether p is a plain URL path Roostack can put in a
// Caddyfile matcher: it matches p and everything under it. Colons are
// allowed for routes like Lychee's /api/v2/Auth::login (a plain
// character to Caddy's path matcher, checked on a VM 2026-10-18).
func ValidPath(p string) bool { return pathRe.MatchString(p) }

func (p *Package) validatePort(v *validator, name string, r *Port) {
	at := "ports." + name
	if !nameRe.MatchString(name) {
		v.addf("%s: invalid name", at)
	}
	if r == nil {
		v.addf("%s: title, container, port and host_port are required", at)
		return
	}
	if r.Title == "" {
		v.addf("%s.title: required (shown in the UI)", at)
	}
	if p.Containers[r.Container] == nil {
		v.addf("%s.container: unknown container %q", at, r.Container)
	}
	if !validPort(r.Port) {
		v.addf("%s.port: must be 1-65535", at)
	}
	if r.HostPort < MinHostPort || r.HostPort > 65535 {
		v.addf("%s.host_port: must be %d-65535", at, MinHostPort)
	}
	if r.Proto() != "tcp" && r.Proto() != "udp" {
		v.addf("%s.protocol: must be tcp or udp", at)
	}
	if e := r.DefaultExposure(); e != ExposurePrivate && e != ExposurePublic {
		v.addf("%s.exposure: must be %q or %q", at, ExposurePrivate, ExposurePublic)
	}
}

// checkSecretRef validates a secret reference used in container.secrets.
func (p *Package) checkSecretRef(ref string) error {
	if db, ok := strings.CutPrefix(ref, "databases."); ok {
		name, field, _ := strings.Cut(db, ".")
		d := p.Databases[name]
		// .url only for Postgres so far: no MySQL app needs one yet
		// (the Owner, 2026-10-19).
		if d == nil || !d.HasPassword() || (field != "password" && field != "url") || (field == "url" && d.Type != DBPostgres) {
			return fmt.Errorf("%q: only databases.<postgres, mysql or mariadb db>.password, or .url for postgres, is supported", ref)
		}
		return nil
	}
	s := p.Setting(ref)
	if s == nil || s.Type != SettingSecret {
		return fmt.Errorf("%q is not a secret setting", ref)
	}
	return nil
}

func validateQuadlet(v *validator, at string, q Quadlet) {
	for _, section := range slices.Sorted(maps.Keys(q)) {
		if !slices.Contains(quadletSections, section) {
			v.addf("%s: section %q not allowed (use one of %v)", at, section, quadletSections)
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(q[section])) {
			// A key or value that wouldn't be read back as written could
			// add a line the package policy never saw (SPEC.md §8.3.4).
			if err := quadlet.CheckKey(key); err != nil {
				v.addf("%s.%s.%q: %v", at, section, key, err)
				continue
			}
			if !isScalarOrList(q[section][key]) {
				v.addf("%s.%s.%s: must be a scalar or a list of scalars", at, section, key)
				continue
			}
			for _, value := range quadletValues(q[section][key]) {
				if err := quadlet.CheckValue(value); err != nil {
					v.addf("%s.%s.%s: %v", at, section, key, err)
				}
			}
		}
	}
}

func isScalarOrList(val any) bool {
	switch x := val.(type) {
	case string, int, bool, float64:
		return true
	case []any:
		for _, e := range x {
			if !isScalarOrList(e) {
				return false
			}
			if _, nested := e.([]any); nested {
				return false
			}
		}
		return true
	}
	return false
}

func validPort(p int) bool { return p > 0 && p < 65536 }

type validator struct{ errs []error }

func (v *validator) addf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) err() error { return errors.Join(v.errs...) }

// installValueFits reports whether an install_value is of the setting's type.
func installValueFits(typ string, v any) bool {
	switch typ {
	case SettingBool:
		_, ok := v.(bool)
		return ok
	case SettingString:
		_, ok := v.(string)
		return ok
	case SettingInt:
		_, ok := v.(int)
		return ok
	}
	return false
}

// hasSavingAction reports whether an action hook saves the setting.
func (p *Package) hasSavingAction(key string) bool {
	for _, h := range p.Hooks {
		if h != nil && h.On == HookAction && h.Saves == key {
			return true
		}
	}
	return false
}

// hasInstallHook reports whether the package has an install hook.
func (p *Package) hasInstallHook() bool {
	for _, h := range p.Hooks {
		if h.On == HookInstall {
			return true
		}
	}
	return false
}
