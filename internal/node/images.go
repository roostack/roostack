package node

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/units"
)

// Cleaning up (SPEC.md §6.7, "Cleaning up"; docs/mockups/lab-notes):
// Roostack never deleted container images, so every earlier version of
// every service, and the images of removed ones, stayed for good. This
// lists the images nothing uses, by what they were, and deletes the ones
// chosen, or by itself every night those a week unused.
//
// An image is in use while a container uses it or a Quadlet file here
// names it (a service's, or Roostack's own), or it's a tool Roostack runs
// now and then (photo migrations' immich-go). The version a service used
// before its last update stays a week after it was last named (keepFor),
// so that going back still works: the node notes when each image was last
// named, and for which service and version (seenKey).

// CleanUp is a server that can list and delete images nothing uses.
type CleanUp interface {
	CleanUpReport(ctx context.Context) (CleanUpReport, error)
	// RemoveImages deletes the images with these IDs, if they're still
	// ones the report offers.
	RemoveImages(ctx context.Context, ids []string) (RemovedImages, error)
	// SetAutoClean turns the nightly clean-up on or off.
	SetAutoClean(ctx context.Context, on bool) error
}

// CleanUp is this server.
func (n *Local) CleanUp() CleanUp { return n }

var (
	_ CleanUp = (*Local)(nil)
	_ CleanUp = (*Remote)(nil)
)

// Image kinds in a report.
const (
	// ImageService is an earlier version of a service, or a removed one's.
	ImageService = "service"
	// ImageOwn is left from building Roostack's own images, or one of
	// its own versions no longer used.
	ImageOwn = "own"
)

// ImageInfo is an image nothing uses.
type ImageInfo struct {
	ID string `json:"id"`
	// Names are its repository and tag ("ghcr.io/immich-app/immich-server:v3.3.3"),
	// none for one left from a build.
	Names []string `json:"names,omitempty"`
	Size  int64    `json:"size"`
	Kind  string   `json:"kind"`
	// Service, Version and Component say what it was when Roostack last
	// saw it named (empty if never: from before, or pulled by hand), and
	// LastUsed when.
	Service   string    `json:"service,omitempty"`
	Version   string    `json:"version,omitempty"`
	Component string    `json:"component,omitempty"`
	LastUsed  time.Time `json:"last_used,omitzero"`
	// KeptUntil: kept for going back until then, so not offered.
	KeptUntil time.Time `json:"kept_until,omitzero"`
}

// Repository is its first name without the tag, or "".
func (i ImageInfo) Repository() string {
	if len(i.Names) == 0 {
		return ""
	}
	name := i.Names[0]
	if at := strings.LastIndex(name, ":"); at > strings.LastIndex(name, "/") {
		name = name[:at]
	}
	return name
}

// CleanUpReport is what could be deleted on a server.
type CleanUpReport struct {
	// Old are images nothing uses, offered; Kept are the versions kept
	// for going back.
	Old  []ImageInfo `json:"old"`
	Kept []ImageInfo `json:"kept"`
	// AutoClean: old versions of services are deleted every night a week
	// after nothing uses them (on unless turned off).
	AutoClean bool `json:"auto_clean"`
}

// RemovedImages is what deleting freed, and what couldn't be deleted.
type RemovedImages struct {
	Removed int   `json:"removed"`
	Freed   int64 `json:"freed"`
	// Failed says why an image couldn't be deleted, by ID.
	Failed map[string]string `json:"failed,omitempty"`
}

// keepFor is how long an image a service stopped using is kept, for going
// back after an update.
const keepFor = 7 * 24 * time.Hour

// State keys: when each image was last named (seenKey), and the nightly
// clean-up turned off (autoCleanKey "off").
const (
	seenKey      = "images.seen"
	autoCleanKey = "images.auto_clean"
)

// imageSeen is when an image was last named, and by what.
type imageSeen struct {
	Service   string    `json:"service"`
	Version   string    `json:"version,omitempty"`
	Component string    `json:"component,omitempty"`
	At        time.Time `json:"at"`
}

// podmanImage is one image as `podman images --format json` lists it.
type podmanImage struct {
	ID          string   `json:"Id"`
	Names       []string `json:"Names"`
	RepoDigests []string `json:"RepoDigests"`
	Size        int64    `json:"Size"`
	Containers  int      `json:"Containers"`
}

// quadletImage is an image a Quadlet file here names, with the service
// and component its labels say (none for Roostack's own).
type quadletImage struct {
	Ref, Service, Component string
}

// quadletImages reads the Image= lines of the Quadlet files here.
func (n *Local) quadletImages() []quadletImage {
	files, _ := filepath.Glob(filepath.Join(n.Lab.Paths.QuadletDir(), "*"))
	var out []quadletImage
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var q quadletImage
		var refs []string
		sc := bufio.NewScanner(strings.NewReader(string(data)))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch {
			case strings.HasPrefix(line, "Image="):
				refs = append(refs, strings.TrimPrefix(line, "Image="))
			case strings.HasPrefix(line, "ImageTag="):
				refs = append(refs, strings.TrimPrefix(line, "ImageTag="))
			case strings.HasPrefix(line, "Label=io.roostack.service="):
				q.Service = strings.TrimPrefix(line, "Label=io.roostack.service=")
			case strings.HasPrefix(line, "Label=io.roostack.component="):
				q.Component = strings.TrimPrefix(line, "Label=io.roostack.component=")
			}
		}
		for _, ref := range refs {
			out = append(out, quadletImage{Ref: ref, Service: q.Service, Component: q.Component})
		}
	}
	return out
}

// assignedImages are the images the services assigned here run, from
// their packages: a service that's stopped (turned off, or by the
// registry) has no container files, and keeps its images all the same.
func (n *Local) assignedImages() []quadletImage {
	lab, services := n.Assigned()
	var out []quadletImage
	for _, s := range services {
		if s.Package == nil || s.Instance == nil {
			continue
		}
		o, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			continue
		}
		for _, ref := range o.Images {
			out = append(out, quadletImage{Ref: ref, Service: s.Name()})
		}
	}
	return out
}

// namedImages are the images in use: the Quadlet files', then the
// assigned services'.
func (n *Local) namedImages() []quadletImage {
	return append(n.quadletImages(), n.assignedImages()...)
}

// matches says whether an image is the one ref names: by its digest when
// ref has one (a package's pinned image), else by name.
func (img podmanImage) matches(ref string) bool {
	if _, digest, ok := strings.Cut(ref, "@"); ok {
		return slices.ContainsFunc(img.RepoDigests, func(d string) bool { return strings.HasSuffix(d, "@"+digest) })
	}
	return slices.Contains(img.Names, ref) || slices.Contains(img.Names, "localhost/"+ref)
}

// images lists the images here, as the roostack user.
func (n *Local) images(ctx context.Context) ([]podmanImage, error) {
	if n.Launch == nil {
		return nil, errors.New("this server can't list its images")
	}
	out, err := n.Launch(ctx, nil, "podman", "images", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("podman images: %w", err)
	}
	var imgs []podmanImage
	if err := json.Unmarshal(out, &imgs); err != nil {
		return nil, fmt.Errorf("podman images: %w", err)
	}
	return imgs, nil
}

// noteImagesInUse records, for each image a Quadlet file names now, that
// it was named now and by what, so that it's known once it isn't (after
// each apply, and before a report).
func (n *Local) noteImagesInUse(ctx context.Context) map[string]imageSeen {
	seen := n.imagesSeen(ctx)
	_, services := n.Assigned()
	versions := map[string]string{}
	for _, s := range services {
		if s.Instance != nil {
			versions[s.Name()] = s.Instance.Version
		}
	}
	now := time.Now()
	for _, q := range n.namedImages() {
		if old, ok := seen[q.Ref]; ok && old.At.Equal(now) && old.Component != "" {
			continue // the Quadlet file's, with its component, came first
		}
		seen[q.Ref] = imageSeen{Service: q.Service, Version: versions[q.Service], Component: q.Component, At: now}
	}
	if n.State != nil {
		if b, err := json.Marshal(seen); err == nil {
			n.State.SetSetting(ctx, seenKey, string(b))
		}
	}
	return seen
}

// imagesSeen reads when each image was last named.
func (n *Local) imagesSeen(ctx context.Context) map[string]imageSeen {
	seen := map[string]imageSeen{}
	if n.State != nil {
		if v, _ := n.State.Setting(ctx, seenKey); v != "" {
			json.Unmarshal([]byte(v), &seen)
		}
	}
	return seen
}

// CleanUpReport lists the images nothing uses here.
func (n *Local) CleanUpReport(ctx context.Context) (CleanUpReport, error) {
	imgs, err := n.images(ctx)
	if err != nil {
		return CleanUpReport{}, err
	}
	seen := n.noteImagesInUse(ctx)
	inUse := n.namedImages()
	tools := []string{photoimport.ToolImage}
	var rep CleanUpReport
	rep.AutoClean = n.autoClean(ctx)
	now := time.Now()
	for _, img := range imgs {
		if img.Containers > 0 || slices.ContainsFunc(inUse, func(q quadletImage) bool { return img.matches(q.Ref) }) ||
			slices.ContainsFunc(tools, img.matches) {
			continue
		}
		info := ImageInfo{ID: img.ID, Names: img.Names, Size: img.Size, Kind: ImageService}
		if len(img.Names) == 0 || slices.ContainsFunc(img.Names, func(name string) bool { return strings.HasPrefix(name, "localhost/") }) {
			info.Kind = ImageOwn
		}
		// What it was when last named: the latest of its refs seen.
		for ref, s := range seen {
			if img.matches(ref) && s.At.After(info.LastUsed) {
				info.Service, info.Version, info.Component, info.LastUsed = s.Service, s.Version, s.Component, s.At
			}
		}
		if info.Kind == ImageService && !info.LastUsed.IsZero() && now.Sub(info.LastUsed) < keepFor {
			info.KeptUntil = info.LastUsed.Add(keepFor)
			rep.Kept = append(rep.Kept, info)
			continue
		}
		rep.Old = append(rep.Old, info)
	}
	slices.SortFunc(rep.Old, func(a, b ImageInfo) int { return cmp.Compare(b.Size, a.Size) })
	return rep, nil
}

// RemoveImages deletes images the report offers now, one at a time so
// that one Podman refuses (a container started using it meanwhile)
// doesn't stop the rest.
func (n *Local) RemoveImages(ctx context.Context, ids []string) (RemovedImages, error) {
	rep, err := n.CleanUpReport(ctx)
	if err != nil {
		return RemovedImages{}, err
	}
	var out RemovedImages
	for _, id := range ids {
		i := slices.IndexFunc(rep.Old, func(img ImageInfo) bool { return img.ID == id })
		if i < 0 {
			continue
		}
		if msg, err := n.Launch(ctx, nil, "podman", "image", "rm", "--", id).CombinedOutput(); err != nil {
			if out.Failed == nil {
				out.Failed = map[string]string{}
			}
			out.Failed[id] = strings.TrimSpace(string(msg))
			continue
		}
		out.Removed++
		out.Freed += rep.Old[i].Size
	}
	return out, nil
}

func (n *Local) autoClean(ctx context.Context) bool {
	if n.State == nil {
		return false
	}
	v, _ := n.State.Setting(ctx, autoCleanKey)
	return v != "off"
}

// SetAutoClean turns the nightly clean-up on or off.
func (n *Local) SetAutoClean(ctx context.Context, on bool) error {
	if n.State == nil {
		return errors.New("this server keeps no settings")
	}
	return n.State.SetSetting(ctx, autoCleanKey, map[bool]string{true: "", false: "off"}[on])
}

// CleanNightly deletes old versions of services a week unused, while the
// nightly clean-up is on (after the nightly backup), saying what it
// deleted in History.
func (n *Local) CleanNightly() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if !n.autoClean(ctx) || n.Launch == nil {
		return
	}
	rep, err := n.CleanUpReport(ctx)
	if err != nil {
		if n.Log != nil {
			n.Log.Warn("cleaning up old images", "err", err)
		}
		return
	}
	var ids []string
	for _, img := range rep.Old {
		if img.Kind == ImageService {
			ids = append(ids, img.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	var l *activity.Log
	if n.Activity != nil {
		l = n.Activity.Start("clean-up", "Deleting old versions of services")
	}
	res, err := n.RemoveImages(ctx, ids)
	if l != nil {
		for _, img := range rep.Old {
			if slices.Contains(ids, img.ID) {
				l.Printf("%s", img.Describe())
			}
		}
		l.Printf("Deleted %d, freeing up to %s", res.Removed, units.Bytes(res.Freed))
		for id, why := range res.Failed {
			l.Printf("Couldn't delete %s: %s", id, why)
		}
		l.Finish(err)
	}
}

// Describe names an image in a line: what it was, else its name.
func (i ImageInfo) Describe() string {
	what := i.Repository()
	if what == "" {
		what = "an unnamed image (" + shortID(i.ID) + ")"
	}
	if i.Service != "" {
		what = strings.TrimSpace(capitalise(i.Service)+" "+i.AppVersion()) + " (" + what + ")"
	}
	return what
}

// AppVersion is the version of the service it was, without Roostack's
// revision ("3.3.3").
func (i ImageInfo) AppVersion() string {
	v, _, _ := strings.Cut(i.Version, "-roostack.")
	return v
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
