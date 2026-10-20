// Package importer brings a service's data in from the person's own copy
// of it (SPEC.md §8.4, docs/MIGRATION-PLAN.md §3): reading the old setup
// (pasted `docker inspect` output, or a Compose file read in the browser),
// matching its folders to the package's storage by where they were inside
// the container, the check's rules, and the copy. It knows no app: what
// it matches comes from the package and the old setup.
//
// The server where the service runs does the work (internal/node,
// import.go); this package has no state and touches only the folders it's
// given.
package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Setup is what an old setup says about each of its containers, as much as
// matching and the check need: kept for the import only (never the text
// pasted), with secrets masked.
type Setup struct {
	// From is where it was read: FromInspect or FromCompose.
	From string `json:"from"`
	// Project is its Compose project's name, if known.
	Project    string      `json:"project,omitempty"`
	Containers []Container `json:"containers"`
}

// Where a setup was read from.
const (
	FromInspect = "docker inspect"
	FromCompose = "compose"
)

// Container is one of an old setup's containers.
type Container struct {
	Name string `json:"name"`
	// Service is its Compose service's name, if known (immich-server).
	Service string            `json:"service,omitempty"`
	Image   string            `json:"image"`
	Mounts  []Mount           `json:"mounts,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// NVIDIA: it used an NVIDIA GPU, which Roostack doesn't support.
	NVIDIA bool `json:"nvidia,omitempty"`
}

// Mount is a folder (or file) an old container had.
type Mount struct {
	// Source is where it was on the old machine: a path, relative to the
	// Compose file's folder when it starts with "." (a Compose file read
	// without docker inspect), or for a named volume its folder under
	// /var/lib/docker/volumes when known.
	Source string `json:"source"`
	Target string `json:"target"`
	// Volume is a named volume's name.
	Volume   string `json:"volume,omitempty"`
	ReadOnly bool   `json:"ro,omitempty"`
}

// Masked is shown for a secret's value: it's never kept.
const Masked = "•••"

// secretLike are names of variables whose values are secrets.
var secretLike = regexp.MustCompile(`(?i)pass|secret|token|key|private|credential|auth|salt|cookie|jwt|dsn|_url$`)

// IsSecret says whether a variable's value should be masked.
func IsSecret(name string) bool { return secretLike.MatchString(name) }

// Limits on a setup, which comes from the person (so from the browser).
const (
	maxContainers = 64
	maxMounts     = 256
	maxEnv        = 512
	maxText       = 4096
)

// inspected is the part of one container in `docker inspect` output that
// matters here.
type inspected struct {
	Name   string `json:"Name"`
	Config struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	HostConfig struct {
		Runtime        string `json:"Runtime"`
		DeviceRequests []struct {
			Driver       string     `json:"Driver"`
			Capabilities [][]string `json:"Capabilities"`
		} `json:"DeviceRequests"`
	} `json:"HostConfig"`
}

// ParseInspect reads the output of `docker inspect` for an old setup's
// containers (a JSON list; several pasted one after another are read too).
func ParseInspect(text []byte) (Setup, error) {
	s := Setup{From: FromInspect}
	dec := json.NewDecoder(strings.NewReader(string(text)))
	for {
		var list []inspected
		err := dec.Decode(&list)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Setup{}, errors.New("that isn't what docker inspect prints: paste all of it, from the first [ to the last ]")
		}
		for _, c := range list {
			oc := Container{Name: strings.TrimPrefix(c.Name, "/"), Image: c.Config.Image,
				Service: c.Config.Labels["com.docker.compose.service"], Env: map[string]string{}}
			if p := c.Config.Labels["com.docker.compose.project"]; p != "" {
				s.Project = p
			}
			for _, kv := range c.Config.Env {
				k, v, _ := strings.Cut(kv, "=")
				oc.Env[k] = v
			}
			for _, m := range c.Mounts {
				oc.Mounts = append(oc.Mounts, Mount{Source: m.Source, Target: m.Destination, ReadOnly: !m.RW,
					Volume: map[bool]string{true: m.Name}[m.Type == "volume"]})
			}
			oc.NVIDIA = c.HostConfig.Runtime == "nvidia"
			for _, d := range c.HostConfig.DeviceRequests {
				for _, caps := range d.Capabilities {
					oc.NVIDIA = oc.NVIDIA || d.Driver == "nvidia" || slices.Contains(caps, "gpu")
				}
			}
			s.Containers = append(s.Containers, oc)
		}
	}
	if len(s.Containers) == 0 {
		return Setup{}, errors.New("there are no containers in that: paste what docker inspect printed for the old containers")
	}
	return s, s.Clean()
}

// Clean checks a setup (from ParseInspect, or the browser's Compose
// reader), within limits, and masks secrets' values, so none is kept.
func (s *Setup) Clean() error {
	if s.From != FromInspect && s.From != FromCompose {
		return errors.New("an old setup is read from docker inspect or a Compose file")
	}
	if len(s.Containers) == 0 || len(s.Containers) > maxContainers {
		return fmt.Errorf("an old setup has 1 to %d containers", maxContainers)
	}
	if !plain(s.Project) {
		return errors.New("the old setup's project name isn't plain text")
	}
	for i := range s.Containers {
		c := &s.Containers[i]
		if !plain(c.Name) || !plain(c.Service) || !plain(c.Image) || c.Image == "" {
			return fmt.Errorf("container %d of the old setup has no image, or odd text in its name or image", i+1)
		}
		if c.Name == "" {
			c.Name = cmpOr(c.Service, Repository(c.Image))
		}
		if len(c.Mounts) > maxMounts || len(c.Env) > maxEnv {
			return fmt.Errorf("%s has more folders or settings than Roostack reads", c.Name)
		}
		for _, m := range c.Mounts {
			if !plain(m.Source) || !plain(m.Volume) || !plain(m.Target) || !path.IsAbs(m.Target) {
				return fmt.Errorf("%s has a folder Roostack can't read (%q)", c.Name, printable(m.Target))
			}
		}
		for k, v := range c.Env {
			if !plain(k) || !plain(v) || k == "" {
				return fmt.Errorf("%s has a setting Roostack can't read", c.Name)
			}
			if IsSecret(k) && v != "" {
				c.Env[k] = Masked
			}
		}
	}
	return nil
}

// plain says whether text is short enough and has no control characters.
func plain(s string) bool {
	return len(s) <= maxText && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// printable shortens text for an error.
func printable(s string) string {
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Repository is an image's repository, as registries name it: without its
// tag or digest, with docker.io's defaults filled in
// (docker.io/library/postgres), so two spellings of one image compare
// equal.
func Repository(image string) string {
	rest, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		rest = rest[:i]
	}
	first, _, hasSlash := strings.Cut(rest, "/")
	if !hasSlash || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		if !hasSlash {
			rest = "library/" + rest
		}
		rest = "docker.io/" + rest
	}
	return strings.ToLower(rest)
}

// Tag is an image's tag ("" for none: latest).
func Tag(image string) string {
	rest, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		return rest[i+1:]
	}
	return ""
}

// databaseKinds tell a database's image from an app's (as the package
// editor's Compose import does).
var databaseKinds = []struct {
	re   *regexp.Regexp
	kind string
}{
	{regexp.MustCompile(`postgres|postgis|pgvecto|timescale`), "postgres"},
	{regexp.MustCompile(`mariadb`), "mariadb"},
	{regexp.MustCompile(`mysql`), "mysql"},
	{regexp.MustCompile(`redis|valkey|keydb|dragonfly`), "redis"},
}

// DatabaseKind is the kind of database an image runs (postgres, mariadb,
// mysql, redis), or "" for an app.
func DatabaseKind(image string) string {
	repo := Repository(image)
	for _, k := range databaseKinds {
		if k.re.MatchString(repo) {
			return k.kind
		}
	}
	return ""
}
